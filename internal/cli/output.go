// Package cli implements the output and error contract from docs/cli.md.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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

// dumpsFunc renders a value as a Ktav document.
type dumpsFunc func(v any) (string, error)

var (
	formatMu  sync.RWMutex
	ktavDumps dumpsFunc // nil: JSON
)

// UseKtav makes Success and Render write Ktav, rendered by dumps, instead of
// JSON. The package cannot call the Ktav binding itself: the binding needs the
// native library that internal/config locates, and config imports this
// package. If dumps fails (the library is missing, a value cannot be
// represented) the JSON form is written, so that a result and above all an
// error is never lost.
func UseKtav(dumps func(v any) (string, error)) {
	formatMu.Lock()
	ktavDumps = dumps
	formatMu.Unlock()
}

// UseJSON makes Success and Render write JSON, the default.
func UseJSON() { UseKtav(nil) }

// Success writes the success envelope to w: {"ok":true,"data":...} as JSON, or
// the same keys as Ktav.
func Success(w io.Writer, data any) {
	write(w, envelope{SchemaVersion: SchemaVersion, OK: true, Data: data})
}

// Render writes the error envelope to w and returns the process exit code.
// A wrapped *Error (errors.As) keeps its code.
func Render(w io.Writer, err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: "INTERNAL", Message: err.Error(), Exit: ExitInternal}
	}
	write(w, envelope{SchemaVersion: SchemaVersion, OK: false, Error: e})
	return e.Exit
}

func write(w io.Writer, e envelope) {
	formatMu.RLock()
	dumps := ktavDumps
	formatMu.RUnlock()
	if dumps != nil {
		if text, err := ktavText(dumps, e); err == nil {
			_, _ = io.WriteString(w, text)
			return
		}
	}
	writeJSON(w, e)
}

// ktavText renders an envelope as Ktav. The two fixed keys come first, then
// the data or the error, so that every document starts the same way.
func ktavText(dumps dumpsFunc, e envelope) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version: %d\nok: %t\n", e.SchemaVersion, e.OK)
	var body map[string]any
	switch {
	case e.Error != nil:
		fields := map[string]any{"code": e.Error.Code, "message": e.Error.Message}
		if e.Error.Hint != "" {
			fields["hint"] = e.Error.Hint
		}
		body = map[string]any{"error": fields}
	case e.Data != nil:
		body = map[string]any{"data": e.Data}
	}
	if body != nil {
		text, err := dumps(body)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// Fail writes the error envelope to stderr and exits with its code.
func Fail(err error) {
	os.Exit(Render(os.Stderr, err))
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
