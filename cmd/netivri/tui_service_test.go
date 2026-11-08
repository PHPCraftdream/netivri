package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

var serviceNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type serviceFixture struct {
	s       *serviceScreen
	running bool
	starts  int
	stops   int
	order   []string
	toasts  []string
	checks  []doctorCheck
	startE  error
}

func newServiceFixture(t *testing.T, running bool) *serviceFixture {
	t.Helper()
	f := &serviceFixture{running: running}
	env := tui.Env{
		Home:       t.TempDir(),
		Now:        func() time.Time { return serviceNow },
		Background: inlineBackground,
		Toast:      func(text string) tea.Cmd { f.toasts = append(f.toasts, text); return nil },
	}
	f.s = newServiceScreen(env.Home, env).(*serviceScreen)
	f.s.wait = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	f.s.probe = func(string) (angel.Status, error) {
		if !f.running {
			return angel.Status{}, angel.ErrNotRunning
		}
		return angel.Status{Angel: "running", PID: 4242, Listen: "127.0.0.1:7890", Version: "1.2.3",
			StartedAt: serviceNow.Add(-90 * time.Second).Format(time.RFC3339),
			Activity: []angel.AccountActivity{
				{Login: "alice", Active: true, Connections: 3, DownloadBytes: 2048, UploadBytes: 512},
				{Login: "bob", Active: false}}}, nil
	}
	f.s.start = func(string) error {
		f.starts++
		f.order = append(f.order, "start")
		if f.startE == nil {
			f.running = true
		}
		return f.startE
	}
	f.s.stop = func(string) error {
		f.stops++
		f.order = append(f.order, "stop")
		f.running = false
		return nil
	}
	f.s.doctor = func(string) []doctorCheck { return f.checks }
	f.drain(f.s.Init())
	return f
}

func (f *serviceFixture) drain(c tea.Cmd) {
	f.s = parityDrain(f.s, c).(*serviceScreen)
}

func (f *serviceFixture) key(k string) {
	r := []rune(k)
	var c tea.Cmd
	var sc tui.Screen
	sc, c = f.s.Update(tea.KeyPressMsg{Code: r[0], Text: k})
	f.s = sc.(*serviceScreen)
	f.drain(c)
}

func cardLine(c *tui.Card, id string) (tui.CardLine, bool) {
	for _, l := range c.Lines {
		if l.ID == id {
			return l, true
		}
	}
	return tui.CardLine{}, false
}

func TestServiceStoppedAngel(t *testing.T) {
	f := newServiceFixture(t, false)
	if got := f.s.list.Rows[0].Cells[1]; got != "stopped" {
		t.Fatalf("angel row state = %q", got)
	}
	c := f.s.workspace.Card
	if l, _ := cardLine(c, "state"); l.Value != "stopped" {
		t.Fatalf("state line = %+v", l)
	}
	if l, _ := cardLine(c, "start"); l.Disabled {
		t.Fatal("Start must be enabled while stopped")
	}
	for _, id := range []string{"stop", "restart"} {
		if l, _ := cardLine(c, id); !l.Disabled {
			t.Fatalf("%s must be disabled while stopped", id)
		}
	}
}

func TestServiceRunningAngelShowsStateAndActivity(t *testing.T) {
	f := newServiceFixture(t, true)
	c := f.s.workspace.Card
	for id, want := range map[string]string{"state": "running", "listen": "127.0.0.1:7890", "process": "4242", "uptime": "1m30s", "version": "1.2.3"} {
		if l, _ := cardLine(c, id); l.Value != want {
			t.Errorf("%s = %q, want %q", id, l.Value, want)
		}
	}
	if l, _ := cardLine(c, "start"); !l.Disabled {
		t.Fatal("Start must be disabled while running")
	}
	if got := f.s.list.Rows[2].Cells[1]; got != "1 active" {
		t.Fatalf("accounts row = %q", got)
	}
	f.s.list.Selected = 2
	f.s.selected = "accounts"
	f.s.refresh()
	c = f.s.workspace.Card
	if l, ok := cardLine(c, "account-alice"); !ok || !strings.Contains(l.Value, "3 conn") || !strings.Contains(l.Value, "2.0 KiB") {
		t.Fatalf("alice line = %+v", l)
	}
	if l, _ := cardLine(c, "account-bob"); l.Value != "idle" {
		t.Fatalf("bob line = %+v", l)
	}
}

func TestServiceStartKeyStartsAndReprobes(t *testing.T) {
	f := newServiceFixture(t, false)
	f.key("s")
	if f.starts != 1 || !f.running {
		t.Fatalf("starts=%d running=%v", f.starts, f.running)
	}
	if !f.s.running {
		t.Fatal("the screen did not pick up the new state")
	}
	if len(f.toasts) == 0 || f.toasts[len(f.toasts)-1] != "Angel started" {
		t.Fatalf("toasts = %v", f.toasts)
	}
	f.key("s") // already running
	if f.starts != 1 || f.toasts[len(f.toasts)-1] != "The angel is already running" {
		t.Fatalf("starts=%d toasts=%v", f.starts, f.toasts)
	}
}

func TestServiceStartErrorIsShownWithoutDetails(t *testing.T) {
	f := newServiceFixture(t, false)
	f.startE = &cli.Error{Code: "PORT_IN_USE", Message: "the port is in use", Hint: "pick another", Exit: cli.ExitConflict}
	f.key("s")
	if got := f.toasts[len(f.toasts)-1]; got != "the port is in use (pick another)" {
		t.Fatalf("toast = %q", got)
	}
	f.startE = errors.New("dial tcp 10.0.0.1:1: secret-looking text")
	f.key("s")
	if got := f.toasts[len(f.toasts)-1]; strings.Contains(got, "secret") || got != "service operation failed" {
		t.Fatalf("a plain error must not be echoed: %q", got)
	}
}

func TestServiceStopAsksAndNamesConnections(t *testing.T) {
	f := newServiceFixture(t, true)
	f.key("x")
	if f.s.dialog == nil || f.s.confirm == nil {
		t.Fatal("Stop must ask first")
	}
	if p := f.s.confirm.Prompt; !strings.Contains(p, "Stop the angel?") || !strings.Contains(p, "3 open connections") {
		t.Fatalf("prompt = %q", p)
	}
	if !strings.Contains(ansi.Strip(f.s.View(80, 24)), "Stop") {
		t.Fatal("the confirm button must read Stop")
	}
	f.key("n")
	if f.stops != 0 || f.s.dialog != nil {
		t.Fatalf("cancel must stop nothing: stops=%d", f.stops)
	}
	f.key("x")
	f.key("y")
	if f.stops != 1 || f.s.running {
		t.Fatalf("stops=%d running=%v", f.stops, f.s.running)
	}
	if f.toasts[len(f.toasts)-1] != "Angel stopped" {
		t.Fatalf("toasts = %v", f.toasts)
	}
}

func TestServiceStopWhenStoppedDoesNotAsk(t *testing.T) {
	f := newServiceFixture(t, false)
	f.key("x")
	if f.s.dialog != nil || f.stops != 0 || f.toasts[len(f.toasts)-1] != "The angel is not running" {
		t.Fatalf("dialog=%v stops=%d toasts=%v", f.s.dialog, f.stops, f.toasts)
	}
}

func TestServiceRestartStopsThenStarts(t *testing.T) {
	f := newServiceFixture(t, true)
	f.key("t")
	if f.s.confirm == nil || !strings.Contains(f.s.confirm.Prompt, "Restart the angel?") || f.s.confirm.ConfirmLabel != "Restart" {
		t.Fatalf("confirm = %+v", f.s.confirm)
	}
	f.key("y")
	if strings.Join(f.order, ",") != "stop,start" || !f.s.running {
		t.Fatalf("order = %v running=%v", f.order, f.s.running)
	}
	if f.toasts[len(f.toasts)-1] != "Angel restarted" {
		t.Fatalf("toasts = %v", f.toasts)
	}
	// restarting a stopped angel just starts it
	f2 := newServiceFixture(t, false)
	f2.key("t")
	if f2.starts != 1 || f2.stops != 0 || f2.s.dialog != nil {
		t.Fatalf("starts=%d stops=%d", f2.starts, f2.stops)
	}
}

func TestServiceRestartFailureOnStopDoesNotStart(t *testing.T) {
	f := newServiceFixture(t, true)
	f.s.stop = func(string) error {
		return &cli.Error{Code: "INTERNAL", Message: "stop request failed", Exit: cli.ExitInternal}
	}
	f.key("t")
	f.key("y")
	if f.starts != 0 {
		t.Fatal("start must not run after a failed stop")
	}
	if got := f.toasts[len(f.toasts)-1]; got != "stop request failed" {
		t.Fatalf("toast = %q", got)
	}
}

func TestServiceChecksRunAndShowResults(t *testing.T) {
	f := newServiceFixture(t, true)
	f.checks = []doctorCheck{{Name: "config", Status: doctorOK}, {Name: "angel", Status: doctorOK}, {Name: "account alice", Status: doctorFail, Detail: "no nodes in the route"}, {Name: "log", Status: doctorWarn, Detail: "node down"}}
	f.key("c")
	if got := f.s.list.Rows[1].Cells[1]; got != "2 ok, 1 warn, 1 failed" {
		t.Fatalf("checks row = %q", got)
	}
	if got := f.toasts[len(f.toasts)-1]; got != "Checks: 2 ok, 1 warn, 1 failed" {
		t.Fatalf("toast = %q", got)
	}
	f.s.list.Selected = 1
	f.s.selected = "checks"
	f.s.refresh()
	c := f.s.workspace.Card
	if l, _ := cardLine(c, "check-2"); l.Label != "account alice" || l.Value != "fail" {
		t.Fatalf("check-2 = %+v", l)
	}
	if l, ok := cardLine(c, "detail-2"); !ok || l.Value != "no nodes in the route" {
		t.Fatalf("detail-2 = %+v", l)
	}
	if _, ok := cardLine(c, "detail-0"); ok {
		t.Fatal("an ok check has no detail line")
	}
}

func TestServiceTickReprobes(t *testing.T) {
	f := newServiceFixture(t, false)
	f.running = true
	f.drain(func() tea.Msg { return serviceTick{} })
	if !f.s.running {
		t.Fatal("a tick must read the state again")
	}
}

func TestServiceFrameContract(t *testing.T) {
	for _, running := range []bool{false, true} {
		f := newServiceFixture(t, running)
		f.checks = []doctorCheck{{Name: "config", Status: doctorOK}, {Name: "account alice", Status: doctorFail, Detail: strings.Repeat("a long detail ", 12)}}
		f.key("c")
		for _, row := range []int{0, 1, 2} {
			f.s.list.Selected = row
			f.s.selected = []string{"angel", "checks", "accounts"}[row]
			f.s.refresh()
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
				f.s.workspace.Drilled = row%2 == 0 // below 100 columns the card shows only when drilled
				v := ansi.Strip(f.s.View(size[0], size[1]))
				lines := strings.Split(v, "\n")
				if len(lines) != size[1] {
					t.Fatalf("running=%v row=%d %dx%d: %d lines", running, row, size[0], size[1], len(lines))
				}
				for _, l := range lines {
					if ansi.StringWidth(l) > size[0] {
						t.Fatalf("running=%v row=%d %dx%d: line wider than the screen: %q", running, row, size[0], size[1], l)
					}
				}
			}
		}
	}
}

func TestServiceConfirmDialogFitsEverySize(t *testing.T) {
	f := newServiceFixture(t, true)
	f.key("t")
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(f.s.View(size[0], size[1]))
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		if !strings.Contains(v, "Restart the angel?") {
			t.Fatalf("%dx%d: dialog text missing:\n%s", size[0], size[1], v)
		}
	}
}

func TestServiceParityRefs(t *testing.T) {
	f := newServiceFixture(t, false)
	got := map[string]bool{}
	for _, r := range f.s.ParityRefs() {
		got[r] = true
	}
	for _, want := range []string{"service/list", "service/start", "service/stop", "service/restart", "service/checks"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, f.s.ParityRefs())
		}
	}
}
