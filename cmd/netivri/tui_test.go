package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func TestScanInteractiveArgs(t *testing.T) {
	cases := []struct {
		in, want []string
		enabled  bool
		bad      bool
	}{
		{[]string{"--interactive", "sub", "--home", "{DIR}"}, []string{"sub", "--home", "{DIR}"}, true, false},
		{[]string{"account", "-i=true"}, []string{"account"}, true, false},
		{[]string{"-i", "--interactive=false", "server"}, []string{"server"}, false, false},
		{[]string{"sub", "--", "-i"}, []string{"sub", "--", "-i"}, false, false},
		{[]string{"-i=oops"}, nil, false, true},
	}
	for _, tc := range cases {
		got, on, err := scanInteractiveArgs(tc.in)
		if !reflect.DeepEqual(got, tc.want) || on != tc.enabled || (err != nil) != tc.bad {
			t.Errorf("scan(%v)=(%v,%v,%v)", tc.in, got, on, err)
		}
	}
	if _, enabled, err := scanInteractiveArgs([]string{"-i", "--", "command"}); err != nil || !enabled {
		t.Fatalf("flag before --: enabled=%v err=%v", enabled, err)
	}
}

func TestDispatchInteractiveRoutingAndValidation(t *testing.T) {
	t.Setenv("NETIVRI_HOME", t.TempDir())
	t.Setenv("NETIVRI_OUTPUT", "")
	homeDir := t.TempDir()
	oldIn, oldOut, oldRunner := interactiveInputTerminal, interactiveOutputTerminal, interactiveRunner
	t.Cleanup(func() {
		interactiveInputTerminal, interactiveOutputTerminal, interactiveRunner = oldIn, oldOut, oldRunner
	})
	interactiveInputTerminal = func(io.Reader) bool { return true }
	interactiveOutputTerminal = func(io.Writer) bool { return true }
	var gotHome string
	var gotStart int
	interactiveRunner = func(_ io.Writer, home string, start int) error { gotHome, gotStart = home, start; return nil }
	cases := []struct {
		args  []string
		start int
	}{{[]string{"-i"}, 1}, {[]string{"sub", "-i"}, 0}, {[]string{"account", "-i"}, 1}, {[]string{"server", "-i"}, 2}}
	for _, tc := range cases {
		if err := dispatch(tc.args, io.Discard); err != nil {
			t.Errorf("dispatch(%v): %v", tc.args, err)
		}
		if gotStart != tc.start {
			t.Errorf("start for %v = %d", tc.args, gotStart)
		}
	}
	for _, args := range [][]string{{"wat", "-i"}, {"sub", "add", "-i"}, {"account", "list", "-i"}, {"server", "list", "-i"}, {"-i", "extra"}} {
		if err := dispatch(args, io.Discard); err == nil {
			t.Errorf("dispatch(%v) succeeded", args)
		}
	}
	if err := dispatch([]string{"-i", "--home", homeDir}, io.Discard); err != nil || gotHome != homeDir {
		t.Errorf("home routing = %q, %v", gotHome, err)
	}
	if err := dispatch([]string{"sub", "-i", "--home", homeDir}, io.Discard); err != nil || gotHome != homeDir || gotStart != 0 {
		t.Errorf("home after command routing = %q start=%d err=%v", gotHome, gotStart, err)
	}
	if err := dispatch([]string{"account", "-i", "--home=" + homeDir}, io.Discard); err != nil || gotHome != homeDir || gotStart != 1 {
		t.Errorf("equals home routing = %q start=%d err=%v", gotHome, gotStart, err)
	}
	if err := dispatch([]string{"server", "-i", "--local=false"}, io.Discard); err != nil || gotHome == "" {
		t.Errorf("local false routing = %q, %v", gotHome, err)
	}
	for i, want := range []string{"Providers", "Accounts", "Servers"} {
		if got := interactiveOptions(io.Discard, homeDir, 0).Tabs[i].Title; got != want {
			t.Errorf("tab[%d]=%q want %q", i, got, want)
		}
	}
}

func TestInteractiveHeaderStatus(t *testing.T) {
	t.Setenv("NETIVRI_HOME", t.TempDir())
	t.Setenv("NETIVRI_OUTPUT", "")
	startedAt := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(_ context.Context, _ string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	h, err := (tuiHeader{home: "{DIR}"}).Header()
	if err != nil || h.Running || h.Version != version {
		t.Fatalf("stopped header = %+v, %v", h, err)
	}
	angelProbeLive = func(_ context.Context, home string) (angel.State, angel.Status, error) {
		if home != "{DIR}" {
			t.Errorf("home=%q", home)
		}
		return angel.State{}, angel.Status{Listen: "127.0.0.1:7890", Version: "v1", StartedAt: startedAt.Format(time.RFC3339)}, nil
	}
	now := startedAt.Add(time.Hour + 2*time.Minute + 3*time.Second)
	h, err = (tuiHeader{home: "{DIR}", now: func() time.Time { return now }}).Header()
	if err != nil || !h.Running || h.Listen != "127.0.0.1:7890" || h.Version != "v1" || h.Uptime != time.Hour+2*time.Minute+3*time.Second {
		t.Fatalf("running header = %+v, %v", h, err)
	}
	if h.Uptime.String() != "1h2m3s" {
		t.Fatalf("uptime = %s", h.Uptime)
	}
	options := interactiveOptions(io.Discard, "{DIR}", 0)
	options.Header = tuiHeader{home: "{DIR}", now: func() time.Time { return now }}
	frame := tui.NewFrame(options)
	init := frame.Init()
	if init == nil {
		t.Fatal("frame init command is nil")
	}
	// Frame.Init batches screen init and the header poll; feed every returned
	// message so the polled header actually lands in the frame.
	if batch, ok := init().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				frame.Update(c())
			}
		}
	}
	content := frame.View().Content
	for _, want := range []string{"netivri", "running", "127.0.0.1:7890", "up 1h2m3s", "v1"} {
		if !strings.Contains(content, want) {
			t.Fatalf("header frame missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "Uptime:") {
		t.Fatalf("legacy header format: %q", content)
	}
	for _, tc := range []struct {
		name      string
		startedAt string
		want      time.Duration
	}{
		{name: "invalid", startedAt: "not-rfc3339", want: 0},
		{name: "future", startedAt: now.Add(time.Second).Format(time.RFC3339), want: 0},
		{name: "subsecond", startedAt: now.Add(-1500 * time.Millisecond).Format(time.RFC3339Nano), want: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
				return angel.State{}, angel.Status{StartedAt: tc.startedAt}, nil
			}
			got, err := (tuiHeader{home: "{DIR}", now: func() time.Time { return now }}).Header()
			if err != nil || got.Uptime != tc.want {
				t.Fatalf("header uptime = %s, %v; want %s", got.Uptime, err, tc.want)
			}
		})
	}
}

func TestInteractiveEnvironmentHomeResolution(t *testing.T) {
	t.Setenv("NETIVRI_HOME", t.TempDir())
	t.Setenv("NETIVRI_OUTPUT", "")
	oldIn, oldOut, oldRunner := interactiveInputTerminal, interactiveOutputTerminal, interactiveRunner
	t.Cleanup(func() {
		interactiveInputTerminal, interactiveOutputTerminal, interactiveRunner = oldIn, oldOut, oldRunner
	})
	interactiveInputTerminal = func(io.Reader) bool { return true }
	interactiveOutputTerminal = func(io.Writer) bool { return true }
	var home string
	interactiveRunner = func(_ io.Writer, h string, _ int) error { home = h; return nil }
	env := t.TempDir()
	t.Setenv("NETIVRI_HOME", env)
	if err := dispatch([]string{"server", "-i"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if home != env {
		t.Fatalf("home=%q want %q", home, env)
	}
}
func TestInteractiveUsageErrorsHaveEntryHint(t *testing.T) {
	t.Setenv("NETIVRI_HOME", t.TempDir())
	t.Setenv("NETIVRI_OUTPUT", "")
	for _, args := range [][]string{{"wat", "-i"}, {"sub", "add", "-i"}, {"account", "list", "-i"}, {"server", "list", "-i"}, {"-i", "--home"}, {"-i", "--local=maybe"}, {"-i", "--home", "{DIR}", "--local"}, {"-i", "--", "--local"}, {"-i", "--json"}, {"-i", "--interactive=bad"}, {"-i", "--quiet"}, {"-i", "--no-color"}} {
		err := interactiveUsageHint(dispatch(args, io.Discard))
		ce, ok := err.(*cli.Error)
		if !ok || ce.Code != "USAGE" || !strings.Contains(ce.Hint, interactiveEntryHint) {
			t.Errorf("dispatch(%v) error=%#v", args, err)
		}
	}
}
func TestInteractiveRequiresBothTerminalsAndJSONErrors(t *testing.T) {
	t.Setenv("NETIVRI_HOME", t.TempDir())
	t.Setenv("NETIVRI_OUTPUT", "")
	oldIn, oldOut := interactiveInputTerminal, interactiveOutputTerminal
	t.Cleanup(func() { interactiveInputTerminal, interactiveOutputTerminal = oldIn, oldOut })
	interactiveInputTerminal = func(io.Reader) bool { return false }
	interactiveOutputTerminal = func(io.Writer) bool { return true }
	cli.UseJSON()
	t.Cleanup(cli.UseJSON)
	err := dispatch([]string{"-i"}, io.Discard)
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "NOT_A_TERMINAL" || ce.Exit != 2 {
		t.Fatalf("stdin terminal error=%#v", err)
	}
	interactiveInputTerminal = func(io.Reader) bool { return true }
	interactiveOutputTerminal = func(io.Writer) bool { return false }
	err = dispatch([]string{"-i"}, io.Discard)
	ce, ok = err.(*cli.Error)
	if !ok || ce.Code != "NOT_A_TERMINAL" || ce.Exit != 2 {
		t.Fatalf("stdout terminal error=%#v", err)
	}
	var out bytes.Buffer
	if cli.Render(&out, err) != 2 {
		t.Fatal("wrong terminal error exit")
	}
	var terminalEnvelope map[string]any
	if json.Unmarshal(out.Bytes(), &terminalEnvelope) != nil || terminalEnvelope["error"].(map[string]any)["code"] != "NOT_A_TERMINAL" {
		t.Fatalf("bad terminal JSON: %s", out.String())
	}
	t.Setenv("NETIVRI_OUTPUT", "json")
	err = dispatch([]string{"-i"}, io.Discard)
	ce, ok = err.(*cli.Error)
	if !ok || ce.Code != "USAGE" {
		t.Fatalf("json error=%#v", err)
	}
	out.Reset()
	if cli.Render(&out, err) != 2 {
		t.Fatal("wrong JSON usage error exit")
	}
	var envelope map[string]any
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope["ok"] != false || envelope["error"].(map[string]any)["code"] != "USAGE" {
		t.Fatalf("bad JSON error: %s", out.String())
	}
}
