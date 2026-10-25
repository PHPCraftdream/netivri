// Package angel implements netivri's background angel: the state file
// under <home>/run, the loopback control server and client, the spawn
// handshake, and the detached child process. See docs/design/angel.md.
package angel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// maxStateSize caps the state file read so a corrupt or hostile file
// cannot exhaust memory.
const maxStateSize = 64 << 10

// ErrNotRunning reports that no usable angel state exists: the state
// file is missing, the angel did not answer, or the answered pid did
// not match the state file.
var ErrNotRunning = errors.New("angel not running")

// State is the content of run/netivri.json. Token is a local secret and
// must never appear in any output or log.
type State struct {
	PID        int      `json:"pid"`
	Listen     string   `json:"listen"`
	Control    string   `json:"control"`
	Token      string   `json:"token"`
	Foreground bool     `json:"foreground"`
	StartedAt  string   `json:"started_at"`
	Version    string   `json:"version"`
	Auth       string   `json:"auth"`
	Protocols  []string `json:"protocols"`
	Route      string   `json:"route"`
}

// RunDir returns <home>/run: the state file, the log and the handshake files.
func RunDir(home string) string { return filepath.Join(home, "run") }

// StatePath returns the state file path inside a home directory.
func StatePath(home string) string { return filepath.Join(RunDir(home), "netivri.json") }

// LogPath returns the angel log file path inside a home directory.
func LogPath(home string) string { return filepath.Join(RunDir(home), "netivri.log") }

// WriteState writes st to the state file atomically (mode 0600, run dir
// mode 0700).
func WriteState(home string, st State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	return config.WriteFileAtomic(StatePath(home), data)
}

// ReadState reads and strictly decodes the state file. A missing file is
// ErrNotRunning; a corrupt, empty, oversized or incomplete file is a plain
// error that callers treat as stale state.
func ReadState(home string) (State, error) {
	var st State
	data, err := readCapped(StatePath(home), maxStateSize)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, ErrNotRunning
		}
		return st, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return st, fmt.Errorf("decode state file: %w", err)
	}
	if dec.More() {
		return st, fmt.Errorf("decode state file: trailing data")
	}
	if st.PID <= 0 || st.Control == "" || st.Token == "" {
		return st, fmt.Errorf("state file is incomplete")
	}
	return st, nil
}

// readCapped reads a whole file and refuses anything bigger than max.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, more than the %d byte limit", filepath.Base(path), info.Size(), max)
	}
	data := make([]byte, info.Size())
	if len(data) > 0 {
		if _, err := io.ReadFull(f, data); err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
	}
	return data, nil
}

// RemoveStateIf removes the state file only while it still records exactly
// pid and token, so an exiting angel never deletes a successor's state.
func RemoveStateIf(home string, pid int, token string) {
	st, err := ReadState(home)
	if err != nil || st.PID != pid || st.Token != token {
		return
	}
	_ = os.Remove(StatePath(home))
}
