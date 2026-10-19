package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func decodeEnvelope(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, buf.String())
	}
	return m
}

func TestSuccessEnvelopeShape(t *testing.T) {
	var buf bytes.Buffer
	Success(&buf, map[string]string{"version": "1"})
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Errorf("output must end with a newline, got %q", buf.String())
	}
	if bytes.Count(buf.Bytes(), []byte("\n")) != 1 {
		t.Errorf("expected exactly one JSON line, got %q", buf.String())
	}
	m := decodeEnvelope(t, &buf)
	if m["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], SchemaVersion)
	}
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
	data, ok := m["data"].(map[string]any)
	if !ok || data["version"] != "1" {
		t.Errorf("data = %v, want {version: 1}", m["data"])
	}
	if _, present := m["error"]; present {
		t.Errorf("success output must not contain an error key: %v", m)
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	var buf bytes.Buffer
	e := &Error{Code: "SUB_FETCH_FAILED", Message: "GET https://x: 403", Exit: ExitNetwork}
	code := Render(&buf, e)
	if code != ExitNetwork {
		t.Errorf("Render exit = %d, want %d", code, ExitNetwork)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Errorf("output must end with a newline, got %q", buf.String())
	}
	m := decodeEnvelope(t, &buf)
	if m["schema_version"] != float64(SchemaVersion) || m["ok"] != false {
		t.Errorf("bad envelope header: %v", m)
	}
	if _, present := m["data"]; present {
		t.Errorf("error output must not contain a data key: %v", m)
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("error key missing or not an object: %v", m)
	}
	if errObj["code"] != "SUB_FETCH_FAILED" {
		t.Errorf("error.code = %v", errObj["code"])
	}
	if errObj["message"] != "GET https://x: 403" {
		t.Errorf("error.message = %v", errObj["message"])
	}
	if _, present := errObj["hint"]; present {
		t.Errorf("empty hint must be omitted: %v", errObj)
	}
	if _, present := errObj["Exit"]; present {
		t.Errorf("Exit must never be serialized: %v", errObj)
	}
}

func TestErrorEnvelopeHintPresent(t *testing.T) {
	var buf bytes.Buffer
	e := &Error{Code: "AUTH_REQUIRED", Message: "m", Hint: "h", Exit: ExitUsage}
	Render(&buf, e)
	m := decodeEnvelope(t, &buf)
	errObj := m["error"].(map[string]any)
	if errObj["hint"] != "h" {
		t.Errorf("hint = %v, want h", errObj["hint"])
	}
}

func TestRenderPlainErrorBecomesInternal(t *testing.T) {
	var buf bytes.Buffer
	code := Render(&buf, errors.New("boom"))
	if code != ExitInternal {
		t.Errorf("exit = %d, want %d", code, ExitInternal)
	}
	m := decodeEnvelope(t, &buf)
	errObj := m["error"].(map[string]any)
	if errObj["code"] != "INTERNAL" {
		t.Errorf("code = %v, want INTERNAL", errObj["code"])
	}
	if errObj["message"] != "boom" {
		t.Errorf("message = %v, want boom", errObj["message"])
	}
}

func TestRenderWrappedErrorKeepsCode(t *testing.T) {
	var buf bytes.Buffer
	wrapped := fmt.Errorf("fetch: %w", &Error{Code: "NODE_UNREACHABLE", Message: "timeout", Exit: ExitNetwork})
	code := Render(&buf, wrapped)
	if code != ExitNetwork {
		t.Errorf("exit = %d, want %d", code, ExitNetwork)
	}
	m := decodeEnvelope(t, &buf)
	errObj := m["error"].(map[string]any)
	if errObj["code"] != "NODE_UNREACHABLE" {
		t.Errorf("wrapped *Error must keep its code, got %v", errObj["code"])
	}
}

func TestUsage(t *testing.T) {
	e := Usage("bad flag %q", "--x")
	if e.Code != "USAGE" {
		t.Errorf("code = %q, want USAGE", e.Code)
	}
	if e.Exit != ExitUsage {
		t.Errorf("exit = %d, want %d", e.Exit, ExitUsage)
	}
	if e.Message != `bad flag "--x"` {
		t.Errorf("message = %q", e.Message)
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{Code: "PORT_IN_USE", Message: "port busy"}
	if got, want := e.Error(), "PORT_IN_USE: port busy"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestHTMLEscapingDisabled(t *testing.T) {
	var buf bytes.Buffer
	Success(&buf, map[string]string{"raw": `<b>&amp;</b>`})
	if !bytes.Contains(buf.Bytes(), []byte(`<b>&amp;</b>`)) {
		t.Errorf("HTML characters must not be escaped, got %q", buf.String())
	}
}

// TestExitCodeConstants pins the exit codes documented in docs/cli.md.
func TestExitCodeConstants(t *testing.T) {
	cases := map[string]struct{ got, want int }{
		"ExitOK":       {ExitOK, 0},
		"ExitInternal": {ExitInternal, 1},
		"ExitUsage":    {ExitUsage, 2},
		"ExitNetwork":  {ExitNetwork, 3},
		"ExitNoDaemon": {ExitNoDaemon, 4},
		"ExitNotFound": {ExitNotFound, 5},
		"ExitConflict": {ExitConflict, 6},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (docs/cli.md)", name, c.got, c.want)
		}
	}
}
