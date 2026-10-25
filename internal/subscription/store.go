package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// validNameRe restricts subscription names to a single safe path element.
var validNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Snapshot is a stored subscription: when it was fetched and what it
// contained.
type Snapshot struct {
	FetchedAt time.Time `json:"fetched_at"`
	Nodes     []Node    `json:"nodes"`
	Skipped   int       `json:"skipped"`
	Quota     *Quota    `json:"quota,omitempty"`
}

// Store persists subscriptions as Ktav files in dir ({NAME}.ktav). Files carry
// the raw proxy mappings (credentials) and are therefore secret: mode 0600.
// Snapshots written by earlier versions as {NAME}.json are still read and are
// replaced by a .ktav file the next time they are saved.
type Store struct {
	dir string
}

// storeMu serialises in-process writers across all Store values: several
// stores may point at one directory.
var storeMu sync.Mutex

// NewStore returns a store rooted at dir (normally <home>/subscriptions).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

type diskNode struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Server  string         `json:"server"`
	Port    int            `json:"port"`
	Country string         `json:"country,omitempty"`
	Raw     map[string]any `json:"raw"`
}

type diskSnapshot struct {
	FetchedAt time.Time  `json:"fetched_at"`
	Nodes     []diskNode `json:"nodes"`
	Skipped   int        `json:"skipped"`
	Quota     *Quota     `json:"quota,omitempty"`
}

func validName(name string) error {
	if !validNameRe.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid subscription name %q: must match [A-Za-z0-9._-]{1,64}", name)
	}
	return nil
}

const (
	ktavExt   = ".ktav"
	legacyExt = ".json"
)

func (s *Store) path(name string) string { return filepath.Join(s.dir, name+ktavExt) }

// legacyPath is where snapshots of earlier versions live, and where a snapshot
// that Ktav cannot hold without loss is kept.
func (s *Store) legacyPath(name string) string { return filepath.Join(s.dir, name+legacyExt) }

// snapshotHeader opens every stored file; Ktav comments are whole lines.
const snapshotHeader = "## A subscription snapshot stored by netivri. It holds node credentials: keep it private.\n" +
	"## The file is rewritten as a whole on every update; comments are not preserved.\n"

// ktavText renders d as Ktav, and only when reading the text back gives
// exactly d again: Ktav trims the whitespace around a value and has no place for
// every string, and a stored snapshot must never change on the way to the disk.
func ktavText(d diskSnapshot) (string, error) {
	text, err := config.KtavDumps(d)
	if err != nil {
		return "", err
	}
	text = snapshotHeader + text
	var back diskSnapshot
	if err := config.KtavLoadsInto(text, &back); err != nil {
		return "", err
	}
	want, _ := json.Marshal(d)
	got, _ := json.Marshal(back)
	if !bytes.Equal(want, got) {
		return "", errors.New("the snapshot does not survive a round trip through Ktav")
	}
	return text, nil
}

// Save atomically writes the snapshot to {NAME}.ktav and removes the file of an
// earlier version ({NAME}.json). A snapshot that Ktav cannot hold without loss
// stays in the JSON form instead of being damaged; Load reads both.
func (s *Store) Save(name string, snap Snapshot) error {
	if err := validName(name); err != nil {
		return err
	}
	d := diskSnapshot{FetchedAt: snap.FetchedAt, Skipped: snap.Skipped, Quota: snap.Quota, Nodes: make([]diskNode, 0, len(snap.Nodes))}
	for _, n := range snap.Nodes {
		d.Nodes = append(d.Nodes, diskNode{ID: n.ID, Name: n.Name, Type: n.Type, Server: n.Server, Port: n.Port, Country: n.Country, Raw: n.Raw})
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if text, err := ktavText(d); err == nil {
		if err := writeFileAtomic(s.path(name), []byte(text)); err != nil {
			return err
		}
		_ = os.Remove(s.legacyPath(name))
		return nil
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode subscription %q: %w", name, err)
	}
	if err := writeFileAtomic(s.legacyPath(name), data); err != nil {
		return err
	}
	_ = os.Remove(s.path(name))
	return nil
}

// Load reads a snapshot, from {NAME}.ktav or else from the {NAME}.json of an
// earlier version; a missing subscription reports os.ErrNotExist.
func (s *Store) Load(name string) (*Snapshot, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	var d diskSnapshot
	data, err := os.ReadFile(s.path(name))
	switch {
	case err == nil:
		// The error text is the structured summary only: the source holds credentials.
		if err := config.KtavLoadsInto(string(data), &d); err != nil {
			return nil, fmt.Errorf("subscription %q is corrupt: %s", name, err)
		}
	case errors.Is(err, os.ErrNotExist):
		data, err = os.ReadFile(s.legacyPath(name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("subscription %q: %w", name, os.ErrNotExist)
			}
			return nil, fmt.Errorf("read subscription %q: %w", name, err)
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, fmt.Errorf("subscription %q is corrupt: %w", name, err)
		}
	default:
		return nil, fmt.Errorf("read subscription %q: %w", name, err)
	}
	snap := &Snapshot{FetchedAt: d.FetchedAt, Skipped: d.Skipped, Quota: d.Quota, Nodes: make([]Node, 0, len(d.Nodes))}
	for _, n := range d.Nodes {
		snap.Nodes = append(snap.Nodes, Node{ID: n.ID, Name: n.Name, Type: n.Type, Server: n.Server, Port: n.Port, Country: n.Country, Raw: n.Raw})
	}
	return snap, nil
}

// List returns the stored subscription names, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	seen := make(map[string]bool, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		ext := filepath.Ext(n)
		if ext != ktavExt && ext != legacyExt {
			continue
		}
		name := n[:len(n)-len(ext)]
		if validName(name) != nil || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes a stored subscription in either form; a missing one reports
// os.ErrNotExist.
func (s *Store) Remove(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	removed := false
	for _, path := range []string{s.path(name), s.legacyPath(name)} {
		err := os.Remove(path)
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("remove subscription %q: %w", name, err)
		}
	}
	if !removed {
		return fmt.Errorf("subscription %q: %w", name, os.ErrNotExist)
	}
	return nil
}

// writeFileAtomic writes data to path atomically: a temporary file is
// created in the same directory with mode 0600, fsynced and renamed over
// the target. The directory is created with mode 0700 when missing.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create subscriptions directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".netivri-sub-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write subscription: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync subscription: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close subscription: %w", err)
	}
	if err := renameReplace(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace subscription: %w", err)
	}
	return nil
}
