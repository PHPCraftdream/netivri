package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func TestServersCoverKeyHints(t *testing.T) {
	s := newServersScreen(t.TempDir(), tui.Env{}).(*serversScreen)
	if got := s.KeyHints(); !strings.Contains(got, "Read-only") {
		t.Fatalf("key hints: %q", got)
	}
}

func TestServersCoverDeliverWithoutBackground(t *testing.T) {
	s := newServersScreen(t.TempDir(), tui.Env{}).(*serversScreen)
	c := s.deliver("lbl", func() tea.Msg { return "work" })
	if c == nil {
		t.Fatal("deliver returned nil cmd")
	}
	m := c()
	b, ok := m.(tui.BackgroundMsg)
	if !ok || b.Label != "lbl" || b.Value != "work" {
		t.Fatalf("inline deliver produced %T %+v", m, m)
	}
}

func TestServersCoverServerRowsCountersByState(t *testing.T) {
	rows := []serverView{
		{Name: "run", Protocol: "ss", Bind: "127.0.0.1", Port: 1, Unclassified: 7, SlowFirstByte: 5, Replayed: 3, Ambiguous: 1},
		{Name: "idle", Protocol: "ss", Bind: "127.0.0.1", Port: 2},
	}
	got := serverRows(rows, true, map[string]bool{"run": true})
	if got[0].Cells[5] != "running" || got[0].Cells[6] != "u:7 s:5 r:3 a:1" {
		t.Fatalf("running row: %#v", got[0])
	}
	if got[1].Cells[5] != "not applied" || got[1].Cells[6] != "" {
		t.Fatalf("not-applied row: %#v", got[1])
	}
	stopped := serverRows(rows, false, map[string]bool{"run": true})
	if stopped[0].Cells[5] != "angel stopped" || stopped[0].Cells[6] != "" || stopped[1].Cells[5] != "angel stopped" {
		t.Fatalf("stopped rows: %#v %#v", stopped[0], stopped[1])
	}
}

func TestServersCoverSafeServersErrorEdges(t *testing.T) {
	plain := safeServersError(errors.New("raw failure"))
	var ce *cli.Error
	if !errors.As(plain, &ce) || ce.Code != "INTERNAL" || ce.Message == "raw failure" {
		t.Fatalf("non-cli error not wrapped safely: %#v", plain)
	}
	typed := safeServersError(&cli.Error{Code: "CONFIG_INVALID", Message: "boom-secret", Hint: "h", Exit: cli.ExitInternal})
	if !errors.As(typed, &ce) || ce.Message != "could not load server configuration" {
		t.Fatalf("CONFIG_INVALID not sanitized: %#v", typed)
	}
	passthrough := safeServersError(&cli.Error{Code: "SERVER_NOT_FOUND", Message: "m", Hint: "h"})
	if !errors.As(passthrough, &ce) || ce.Code != "SERVER_NOT_FOUND" || ce.Message != "m" {
		t.Fatalf("other codes should pass through: %#v", passthrough)
	}
}

func TestServersCoverServersErrorTextUnknown(t *testing.T) {
	if got := serversErrorText(errors.New("mystery")); got != "could not load servers" {
		t.Fatalf("unknown error text: %q", got)
	}
	if got := serversErrorText(&cli.Error{Code: "WEIRD", Message: "msg", Hint: "hint"}); got != "msg — hint" {
		t.Fatalf("unmapped code text: %q", got)
	}
}

func TestServersCoverSafeDetailValue(t *testing.T) {
	if got := serversSafeDetailValue(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
	if got := serversSafeDetailValue("a\tb\nc"); got != "a b c" {
		t.Fatalf("control chars: %q", got)
	}
	if got := serversSafeDetailValue("plain"); got != "plain" {
		t.Fatalf("plain: %q", got)
	}
}

func TestServersCoverWindowSizeAndTheme(t *testing.T) {
	s := newServersScreen(t.TempDir(), tui.Env{}).(*serversScreen)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if s.workspace.Width != 120 || s.workspace.Height != 40 {
		t.Fatal("window size not applied to servers workspace")
	}
	light := tui.NewThemeFor(false)
	_, _ = s.Update(tui.ThemeMsg{Theme: light})
	if s.workspace.Theme.Dark {
		t.Fatal("theme not applied to servers workspace")
	}
}

func TestTUIHeaderRunningAndStopped(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	h := tuiHeader{home: t.TempDir(), now: func() time.Time { return time.Now().Add(-2 * time.Second) }}
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{Version: "9.9", Listen: "127.0.0.1:1", StartedAt: time.Now().Format(time.RFC3339)}, nil
	}
	hdr, err := h.Header()
	if err != nil || !hdr.Running || hdr.Version != "9.9" || hdr.Uptime < 0 {
		t.Fatalf("running header: %+v %v", hdr, err)
	}
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	hdr, err = h.Header()
	if err != nil || hdr.Running || hdr.Version != version {
		t.Fatalf("stopped header: %+v %v", hdr, err)
	}
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, context.DeadlineExceeded
	}
	if _, err = h.Header(); err == nil {
		t.Fatal("probe error should surface")
	}
}

func TestCoverScanInteractiveArgs(t *testing.T) {
	out, enabled, err := scanInteractiveArgs([]string{"-i", "sub", "--home", "/h"})
	if err != nil || !enabled || strings.Join(out, " ") != "sub --home /h" {
		t.Fatalf("scan: %v %v %v", out, enabled, err)
	}
	out, enabled, err = scanInteractiveArgs([]string{"--interactive=false", "list"})
	if err != nil || enabled || strings.Join(out, " ") != "list" {
		t.Fatalf("scan disabled: %v %v %v", out, enabled, err)
	}
	if _, _, err = scanInteractiveArgs([]string{"--interactive=maybe"}); err == nil {
		t.Fatal("invalid bool should error")
	}
	out, enabled, _ = scanInteractiveArgs([]string{"a", "--", "-i"})
	if enabled || strings.Join(out, " ") != "a -- -i" {
		t.Fatalf("dash dash: %v %v", out, enabled)
	}
}

func TestInteractiveCommandParsing(t *testing.T) {
	cmd, rest, err := interactiveCommand([]string{"server", "--home", "/h"})
	if err != nil || cmd != "server" || strings.Join(rest, " ") != "--home /h" {
		t.Fatalf("command: %q %v %v", cmd, rest, err)
	}
	if _, _, err = interactiveCommand([]string{"sub", "--"}); err == nil {
		t.Fatal("args after -- should error")
	}
	if _, _, err = interactiveCommand([]string{"--home"}); err == nil {
		t.Fatal("dangling --home should error")
	}
	if _, _, err = interactiveCommand([]string{"--json"}); err == nil {
		t.Fatal("mode flag should error")
	}
	if _, _, err = interactiveCommand([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag should error")
	}
	cmd, rest, err = interactiveCommand([]string{"--local", "sub", "extra"})
	if err != nil || cmd != "sub" || strings.Join(rest, " ") != "--local extra" {
		t.Fatalf("local passthrough: %q %v %v", cmd, rest, err)
	}
}

func TestInteractiveUsageHint(t *testing.T) {
	err := interactiveUsageHint(cli.Usage("boom"))
	var ce *cli.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Hint, "netivri -i") {
		t.Fatalf("usage hint not attached: %#v", err)
	}
	plain := errors.New("plain")
	if interactiveUsageHint(plain) != plain {
		t.Fatal("non-usage errors should pass through")
	}
}

func TestInteractiveOptionsBuildsTabs(t *testing.T) {
	opts := interactiveOptions(io.Discard, t.TempDir(), 1)
	if len(opts.Tabs) != 3 || opts.Tabs[0].Title != "Providers" || opts.Tabs[1].Title != "Accounts" || opts.Tabs[2].Title != "Servers" {
		t.Fatalf("tabs: %+v", opts.Tabs)
	}
	if opts.Start != 1 || opts.Home == "" {
		t.Fatal("options start/home not set")
	}
	for _, tab := range opts.Tabs {
		if tab.New(tui.Env{}) == nil {
			t.Fatalf("tab %q built nil screen", tab.Title)
		}
	}
}
