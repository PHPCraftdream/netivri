package main

import (
	"bytes"
	"encoding/json"
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

func TestDispatchNoCommand(t *testing.T) {
	var out bytes.Buffer
	err := dispatch(nil, &out)
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
		t.Errorf("code/exit = %q/%d, want USAGE/%d", ce.Code, ce.Exit, cli.ExitUsage)
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
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
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
}

func TestDispatchUpWithoutForeground(t *testing.T) {
	var out bytes.Buffer
	err := dispatch([]string{"up"}, &out)
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
		t.Errorf("code/exit = %q/%d, want USAGE/%d", ce.Code, ce.Exit, cli.ExitUsage)
	}
	const wantPrefix = "daemon mode is not implemented yet"
	if len(ce.Message) < len(wantPrefix) || ce.Message[:len(wantPrefix)] != wantPrefix {
		t.Errorf("message = %q, want prefix %q", ce.Message, wantPrefix)
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
}
