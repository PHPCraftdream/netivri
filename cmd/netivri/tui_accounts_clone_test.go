package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

const cloneHopURL = "socks5://u:fake-hop@hop.example:1080"

// cloneScreenHome seeds a provider home with alice on p1/DE carrying a real
// password and two hops, and a screen whose list is loaded from that config.
func cloneScreenHome(t *testing.T, extraLogins ...string) (*accountsScreen, string) {
	t.Helper()
	h := accountProviderHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Provider: "p1", Country: "DE", Exit: "route", ExitGiven: true, PreSOCKS5: cloneHopURL, PostSOCKS5: "http://u:fake-post@post.example:8080"}); e != nil {
		t.Fatal(e)
	}
	for _, login := range extraLogins {
		if _, e := AccountAdd(h, AccountAddOptions{Login: login, Provider: "p1", Country: "DE", Exit: "route", ExitGiven: true}); e != nil {
			t.Fatal(e)
		}
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	hash, e := auth.Hash(cloneSourcePassword)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := c.Account("alice")
	a.Password = hash
	if e = config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	rows, e := AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	s.rows = rows
	s.syncRows("alice")
	return s, h
}

// checkAccountsFrame asserts the stable rendering contract: exactly h lines and
// no ANSI-stripped line wider than w.
func checkAccountsFrame(t *testing.T, v string, w, h int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(v), "\n")
	if len(lines) != h {
		t.Fatalf("view at %dx%d has %d lines, want %d", w, h, len(lines), h)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > w {
			t.Fatalf("line wider than %d at %dx%d: %q", w, w, h, line)
		}
	}
}

// TestAccountsCloneRealKeysEndToEnd clones alice through the screen with real
// keys: the prompt opens pre-filled with alice-2, Enter clones in the
// background, the list reloads with the new account selected, and the stored
// config shows the copied route and hops with a fresh placeholder password.
func TestAccountsCloneRealKeysEndToEnd(t *testing.T) {
	s, h := cloneScreenHome(t)
	if !strings.Contains(s.KeyHints(), "/ filter") {
		t.Fatal("key hints do not mention the list filter")
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	d := s.dialog
	if d == nil || d.prompt == nil {
		t.Fatal("clone hotkey did not open the clone prompt")
	}
	if d.prompt.Value != "alice-2" {
		t.Fatalf("clone prompt default = %q, want alice-2", d.prompt.Value)
	}
	if v := s.View(120, 30); strings.Contains(v, cloneHopURL) || strings.Contains(v, "fake-post") {
		t.Fatal("clone prompt revealed a hop secret")
	}
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the clone prompt did not return a result command")
	}
	msg := cmd()
	result, ok := msg.(accountsDialogResult)
	if !ok || result.Canceled || result.Values["value"] != "alice-2" {
		t.Fatalf("clone result = %#v", msg)
	}
	_, cmd = s.Update(msg)
	if cmd == nil {
		t.Fatal("dialog result did not start the clone")
	}
	if !d.prompt.Saving {
		t.Fatal("clone prompt did not enter the saving state")
	}
	runAccountCmd(s, cmd)
	if s.dialog != nil {
		t.Fatal("successful clone did not close the prompt")
	}
	if s.selectedLogin() != "alice-2" {
		t.Fatalf("clone did not select the new account: %q", s.selectedLogin())
	}
	rows, e := AccountList(h)
	if e != nil || len(rows) != 2 {
		t.Fatalf("list after clone = %#v %v", rows, e)
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := c.Account("alice")
	bob, _ := c.Account("alice-2")
	if bob.Password != config.InitPlaceholder {
		t.Fatalf("clone stored a password or hash instead of the placeholder")
	}
	if bob.Provider != "p1" || bob.ExitCountry != "DE" || bob.Exit != "route" || bob.PreSOCKS5 != cloneHopURL || bob.PostSOCKS5 != "http://u:fake-post@post.example:8080" || bob.AllowedClients != alice.AllowedClients {
		t.Fatalf("clone did not copy provider/countries/exit/clients/hops: %+v", bob)
	}
	if !strings.Contains(alice.Password, "$argon2id$") || alice.PreSOCKS5 != cloneHopURL {
		t.Fatal("the source account changed")
	}
	if ok, e := auth.VerifyHash(alice.Password, cloneSourcePassword); e != nil || !ok {
		t.Fatalf("source password no longer verifies: %v %v", ok, e)
	}
}

// TestAccountsCloneDefaultSkipsTakenLogins bumps the proposed clone login past
// the ones that already exist.
func TestAccountsCloneDefaultSkipsTakenLogins(t *testing.T) {
	s, _ := cloneScreenHome(t, "alice-2")
	s.beginClone(s.rows[0])
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Value != "alice-3" {
		t.Fatalf("clone default with alice-2 taken = %#v", s.dialog)
	}
}

// TestAccountsClonePromptFrameContract keeps the clone prompt inside the frame
// at every supported size.
func TestAccountsClonePromptFrameContract(t *testing.T) {
	s, _ := cloneScreenHome(t)
	s.beginClone(s.rows[0])
	if s.dialog == nil {
		t.Fatal("no clone prompt")
	}
	for _, d := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		checkAccountsFrame(t, s.View(d[0], d[1]), d[0], d[1])
	}
}

// TestAccountsHopCardFrameContract keeps the account card with set hops inside
// the frame at every supported size, without revealing the hop values.
func TestAccountsHopCardFrameContract(t *testing.T) {
	s, _ := cloneScreenHome(t)
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	for _, d := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		checkAccountsFrame(t, s.View(d[0], d[1]), d[0], d[1])
	}
}
