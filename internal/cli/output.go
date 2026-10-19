// Package cli implements the output and error contract from docs/cli.md.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const SchemaVersion = 1

// Exit codes, see docs/cli.md.
const (
	ExitOK       = 0
	ExitInternal = 1
	ExitUsage    = 2
	ExitNetwork  = 3
	ExitNoDaemon = 4
	ExitNotFound = 5
	ExitConflict = 6
)

// Error is a machine-readable failure with a stable Code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Exit    int    `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Usage(format string, args ...any) *Error {
	return &Error{Code: "USAGE", Message: fmt.Sprintf(format, args...), Exit: ExitUsage}
}

type envelope struct {
	SchemaVersion int    `json:"schema_version"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data,omitempty"`
	Error         *Error `json:"error,omitempty"`
}

// Success writes {"ok":true,"data":...} to stdout.
func Success(w io.Writer, data any) {
	writeJSON(w, envelope{SchemaVersion: SchemaVersion, OK: true, Data: data})
}

// Fail writes the error envelope to stderr and exits with its code.
func Fail(err error) {
	e, ok := err.(*Error)
	if !ok {
		e = &Error{Code: "INTERNAL", Message: err.Error(), Exit: ExitInternal}
	}
	writeJSON(os.Stderr, envelope{SchemaVersion: SchemaVersion, OK: false, Error: e})
	os.Exit(e.Exit)
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
