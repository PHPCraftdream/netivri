package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

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

func TestServersCardActionHotkeysEnumerate(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	frame := s.View(120, 30)

	// Every card action line carries a visible hotkey label; the servers
	// card exposes exactly add(a)/link(l)/rotate(t)/remove(d)/reload(r).
	// Rotate was renamed to t; k is plain card cursor navigation again.
	want := map[string]string{"add": "a", "link": "l", "rotate": "t", "remove": "d", "reload": "r"}
	seen := map[string]bool{}
	for _, l := range s.workspace.Card.Lines {
		if l.Kind != tui.CardAction || !l.Action {
			continue
		}
		if l.Hotkey == "" || want[l.ID] != l.Hotkey {
			t.Fatalf("action line %q hotkey = %q", l.ID, l.Hotkey)
		}
		if !strings.Contains(frame, "["+l.Hotkey+"]") {
			t.Fatalf("frame lacks hotkey label [%s] for %q", l.Hotkey, l.ID)
		}
		seen[l.ID] = true
	}
	for _, id := range []string{"add", "link", "rotate", "remove", "reload"} {
		if !seen[id] {
			t.Fatalf("card action lines incomplete: %v", seen)
		}
	}
	if strings.Contains(frame, "[k]") {
		t.Fatal("k must not be rendered as a rotate hotkey")
	}
	// a -> add wizard (tui_servers.go key case "a").
	if _, c := s.Update(serversKey('a', "a")); c != nil {
		t.Fatal("hotkey a produced a command")
	}
	if s.dialog == nil || s.dialog.wizard == nil {
		t.Fatal("hotkey a did not open the add wizard")
	}
	_, _ = s.Update(serversKey(tea.KeyEscape, ""))
	// l -> link warning confirm prompt.
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("hotkey l produced a command")
	}
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ConfirmPrompt || s.dialog.prompt.ConfirmLabel != "Show" {
		t.Fatalf("hotkey l did not open the show-link confirm")
	}
	_, _ = s.Update(serversKey(tea.KeyEscape, ""))
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

// TestServersCardKIsCursorNavigation traces card.go: "up"/"k" moves the card
// cursor up when the card pane is focused, and list.go the list selection
// when the list pane is focused. Neither opens rotate.
func TestServersCardKIsCursorNavigation(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30)
	if s.dialog != nil {
		t.Fatal("k must not open any dialog")
	}
	before := s.workspace.Card.Selected
	_, _ = s.Update(serversKey(tea.KeyDown, ""))
	mid := s.workspace.Card.Selected
	_, _ = s.Update(serversKey('k', "k"))
	if s.dialog != nil {
		t.Fatal("k opened a dialog (must stay cursor navigation)")
	}
	if s.workspace.Card.Selected != before && mid == before {
		t.Fatalf("card k did not move the cursor: %d -> %d", mid, s.workspace.Card.Selected)
	}
	if s.workspace.Card.Selected >= mid {
		t.Fatalf("card k did not move up: %d -> %d", mid, s.workspace.Card.Selected)
	}
	// List pane: k moves the list selection (list.go b[0] "up"/"k").
	s.workspace.SetFocus(tui.PaneList)
	s.workspace.List.Selected = 1
	_, _ = s.Update(serversKey('k', "k"))
	if s.workspace.List.Selected != 0 {
		t.Fatalf("list k did not move the selection: %d", s.workspace.List.Selected)
	}
}

// TestServersRotateConfirmThroughRealFrame drives rotate through a real
// tui.NewFrame: 't' opens the confirm via env.OpenModal whose command
// produces modalOpen (frame.go envFor), Enter on the untouched confirm
// cancels (confirm.go selected==0), and 'y' confirms.
func TestServersRotateConfirmThroughRealFrame(t *testing.T) {
	home := serversFixture(t)
	f := serversTestFrame(t, home, 120, 40)
	serversFrameLoadDetail(t, f)

	serversFrameKey(t, f, 't', "t")
	view := serversFrameView(t, f)
	if !strings.Contains(view, "Rotate keys for server ss") || !strings.Contains(view, "alice") {
		t.Fatalf("rotate confirm prompt wrong: %q", view)
	}
	// Default No: Enter cancels; config unchanged.
	serversFrameKey(t, f, tea.KeyEnter, "")
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i < 0 {
		t.Fatal("canceled rotate removed the server")
	}
	if strings.Contains(serversFrameView(t, f), "Rotate keys for server ss") {
		t.Fatal("confirm modal stayed open after Enter-cancel")
	}

	// Reopen; y confirms; the rotate job runs through the frame background.
	serversFrameKey(t, f, 't', "t")
	if !strings.Contains(serversFrameView(t, f), "Rotate keys for server ss") {
		t.Fatal("rotate confirm did not reopen")
	}
	serversFrameKey(t, f, 'y', "y")
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i < 0 {
		t.Fatal("rotate removed the server entry")
	}
	if !strings.Contains(serversFrameView(t, f), "Keys rotated for ss") {
		t.Fatal("rotate toast missing from the frame")
	}
}

// TestServersRemoveConfirmThroughRealFrame: same real-frame contract for
// remove; Enter (default No) keeps the server, y deletes it and drops the
// stored secrets.
func TestServersRemoveConfirmThroughRealFrame(t *testing.T) {
	home := serversFixture(t)
	f := serversTestFrame(t, home, 120, 40)
	serversFrameLoadDetail(t, f)

	serversFrameKey(t, f, 'd', "d")
	view := serversFrameView(t, f)
	if !strings.Contains(view, "Remove server ss") || !strings.Contains(view, "alice") {
		t.Fatalf("remove confirm prompt wrong: %q", view)
	}
	serversFrameKey(t, f, tea.KeyEnter, "")
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i < 0 {
		t.Fatal("default Enter removed the server (confirm.go default must cancel)")
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("cancel disturbed other servers: %#v", cfg.Servers)
	}

	serversFrameKey(t, f, 'd', "d")
	if !strings.Contains(serversFrameView(t, f), "Remove server ss") {
		t.Fatal("remove confirm did not reopen")
	}
	serversFrameKey(t, f, 'y', "y")
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i >= 0 {
		t.Fatal("confirmed remove did not delete the server")
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("unrelated server lost: %#v", cfg.Servers)
	}
	if _, ok := serversStoredSecrets(t, home)["ss"]; ok {
		t.Fatal("removed server secrets survived")
	}
	if !strings.Contains(serversFrameView(t, f), "Server removed") {
		t.Fatal("remove toast missing from the frame")
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
