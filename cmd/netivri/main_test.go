package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestDispatchVersion(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch([]string{"version"}, &out); err != nil {
		t.Fatalf("dispatch(version) = %v, want nil", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out.String())
	}
	if m["schema_version"] != float64(1) || m["ok"] != true {
		t.Errorf("bad envelope: %v", m)
	}
	data, ok := m["data"].(map[string]any)
	if !ok || data["version"] != version {
		t.Errorf("data = %v, want version %q", m["data"], version)
	}
}

// No command is a request for help: the overview goes to stdout and the
// exit code is 0.
func TestDispatchNoCommand(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch(nil, &out); err != nil {
		t.Fatalf("dispatch(nil) = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "COMMANDS") {
		t.Errorf("stdout is not the help overview: %q", out.String())
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	err := dispatch([]string{"frobnicate"}, &out)
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
		t.Errorf("code/exit = %q/%d, want USAGE/%d", ce.Code, ce.Exit, cli.ExitUsage)
	}
	if !strings.Contains(ce.Hint, "netivri help") {
		t.Errorf("hint = %q, want it to point to netivri help", ce.Hint)
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
}
