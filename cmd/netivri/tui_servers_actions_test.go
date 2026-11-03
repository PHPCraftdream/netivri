package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// serversActionScreen builds a servers screen whose env records toasts and
// opened confirm modals so hotkey-driven confirms can be inspected.
func serversActionScreen(t *testing.T, home string) (*serversScreen, *[]string, func() *tui.Confirm) {
	t.Helper()
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	toasts := []string{}
	var modal *tui.Confirm
	s := newServersScreen(home, tui.Env{Home: home,
		Toast: func(text string) tea.Cmd { toasts = append(toasts, text); return nil },
		OpenModal: func(m tui.Modal) tea.Cmd {
			if c, ok := m.(*tui.Confirm); ok {
				modal = c
			}
			return func() tea.Msg { return m }
		},
		Background: func(label string, work func() tea.Msg) tea.Cmd {
			return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
		},
	}).(*serversScreen)
	s.status = func(ctx context.Context, h string) (angel.State, angel.Status, error) { return angelProbeLive(ctx, h) }
	return s, &toasts, func() *tui.Confirm { return modal }
}

func TestServersEnterOnAddLineOpensWizard(t *testing.T) {
	s := newServersTestScreen(t, serversWizardHome(t))
	serversRun(t, s, s.Init())
	// End selects the trailing "+ Add a server" row (list.go count()-1).
	_, _ = s.Update(serversKey(tea.KeyEnd, ""))
	if !s.workspace.List.IsAdd() {
		t.Fatal("End did not select the add row")
	}
	_, cmd := s.Update(serversKey(tea.KeyEnter, ""))
	if cmd != nil {
		t.Fatal("opening the wizard produced a command")
	}
	if s.dialog == nil || s.dialog.wizard == nil || s.dialog.wizard.Index != 0 {
		t.Fatal("Enter on the add line did not open the add wizard")
	}
}

func TestServersCardActionHotkeysEnumerateAndOpenDialogs(t *testing.T) {
	home := serversFixture(t)
	s, _, confirm := serversActionScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	frame := s.View(120, 30)

	// Every card action line carries a visible hotkey label; the servers card
	// exposes exactly add(a)/link(l)/rotate(k)/remove(d)/reload(r).
	want := map[string]string{"add": "a", "link": "l", "rotate": "k", "remove": "d", "reload": "r"}
	seen := map[string]bool{}
	for _, l := range s.workspace.Card.Lines {
		if l.Kind != tui.CardAction || !l.Action {
			continue
		}
		if l.Hotkey == "" || want[l.ID] != l.Hotkey {
			t.Fatalf("action line %q hotkey = %q", l.ID, l.Hotkey)
		}
		if !strings.Contains(frame, "["+l.Hotkey+"]") {
			t.Fatalf("frame lacks hotkey label [%s] for %q: %q", l.Hotkey, l.ID, frame)
		}
		seen[l.ID] = true
	}
	if !seen["add"] || !seen["link"] || !seen["rotate"] || !seen["remove"] || !seen["reload"] {
		t.Fatalf("card action lines incomplete: %v", seen)
	}

	reset := func() {
		s.dialog = nil
	}
	// a -> add wizard (tui_servers.go key case "a").
	if _, c := s.Update(serversKey('a', "a")); c != nil {
		t.Fatal("hotkey a produced a command")
	}
	if s.dialog == nil || s.dialog.wizard == nil {
		t.Fatal("hotkey a did not open the add wizard")
	}
	reset()
	// l -> link warning confirm prompt.
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("hotkey l produced a command")
	}
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ConfirmPrompt || s.dialog.prompt.ConfirmLabel != "Show" {
		t.Fatalf("hotkey l did not open the show-link confirm: %+v", s.dialog)
	}
	reset()
	// k -> rotate confirm via env.OpenModal.
	if _, c := s.Update(serversKey('k', "k")); c != nil {
		t.Fatal("hotkey k produced a command")
	}
	if m := confirm(); m == nil || !strings.Contains(m.Prompt, "Rotate keys for server ss") {
		t.Fatalf("hotkey k did not open the rotate confirm: %+v", m)
	}
	// d -> remove confirm via env.OpenModal.
	if _, c := s.Update(serversKey('d', "d")); c != nil {
		t.Fatal("hotkey d produced a command")
	}
	if m := confirm(); m == nil || !strings.Contains(m.Prompt, "Remove server ss") {
		t.Fatalf("hotkey d did not open the remove confirm: %+v", m)
	}
	// r -> detail reload command, no dialog.
	_, c := s.Update(serversKey('r', "r"))
	if c == nil || s.dialog != nil {
		t.Fatalf("hotkey r: cmd=%v dialog=%v", c != nil, s.dialog != nil)
	}
	serversRun(t, s, c)
	// e is not a servers card hotkey (unlike accounts); it must do nothing.
	if _, c := s.Update(serversKey('e', "e")); c != nil || s.dialog != nil {
		t.Fatal("unexpected hotkey e behavior on the servers screen")
	}
}

func TestServersNoSecretLeakSweepAcrossFlows(t *testing.T) {
	home := serversFixture(t)
	link, err := ServerLink(home, "ss", ServerLinkOptions{Account: "alice", Format: "uri"})
	if err != nil || link == "" {
		t.Fatalf("client link: %v", err)
	}
	s := newServersTestScreen(t, home)
	var frames []string
	serversRun(t, s, s.Init())
	frames = append(frames, s.View(180, 30)) // list
	serversLoadDetail(t, s)
	frames = append(frames, s.View(180, 30)) // detail card
	_, _ = s.Update(serversKey('a', "a"))
	frames = append(frames, s.View(120, 30)) // add wizard
	_, _ = s.Update(serversKey(tea.KeyEscape, ""))
	s.dialog = nil
	serversLoadDetail(t, s)
	s.workspace.SetFocus(tui.PaneCard)
	_, _ = s.Update(serversKey('l', "l"))
	frames = append(frames, s.View(120, 30)) // link warning dialog
	serversAssertNoLeaks(t, home, link, frames)
}
