package daemon

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// HandshakeError is the wire form of a failed daemon start. Exit is
// included so the parent can report the exact same exit code (e.g.
// PORT_IN_USE keeps exit 6).
type HandshakeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Exit    int    `json:"exit"`
}

// CLIErr converts the handshake error into a *cli.Error.
func (h *HandshakeError) CLIErr() *cli.Error {
	return &cli.Error{Code: h.Code, Message: h.Message, Hint: h.Hint, Exit: h.Exit}
}

// NewHandshakeError converts any error into a HandshakeError, keeping
// the code and exit of a *cli.Error.
func NewHandshakeError(err error) *HandshakeError {
	var ce *cli.Error
	if errors.As(err, &ce) {
		return &HandshakeError{Code: ce.Code, Message: ce.Message, Hint: ce.Hint, Exit: ce.Exit}
	}
	return &HandshakeError{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
}

type handshake struct {
	OK    bool            `json:"ok"`
	State *State          `json:"state,omitempty"`
	Error *HandshakeError `json:"error,omitempty"`
}

// WriteHandshake writes the success handshake atomically.
func WriteHandshake(path string, st State) error {
	return writeHandshake(path, handshake{OK: true, State: &st})
}

// WriteHandshakeError writes the failure handshake atomically.
func WriteHandshakeError(path string, herr *HandshakeError) error {
	return writeHandshake(path, handshake{OK: false, Error: herr})
}

func writeHandshake(path string, h handshake) error {
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode handshake: %w", err)
	}
	return config.WriteFileAtomic(path, data)
}

// readHandshake reads and validates a handshake file.
func readHandshake(path string) (handshake, error) {
	var h handshake
	data, err := readCapped(path, 1<<20)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return h, fmt.Errorf("decode handshake: %w", err)
	}
	if h.OK && h.State == nil {
		return h, fmt.Errorf("success handshake without state")
	}
	if !h.OK && h.Error == nil {
		return h, fmt.Errorf("error handshake without error")
	}
	return h, nil
}

// handshakeResult maps a handshake file to the spawn outcome:
// (state, nil) on success, (zero, *cli.Error) on a reported failure.
func handshakeResult(h handshake) (State, error) {
	if h.OK {
		return *h.State, nil
	}
	return State{}, h.Error.CLIErr()
}
