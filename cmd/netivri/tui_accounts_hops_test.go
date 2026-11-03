package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// hopCardLine returns the card line with the given ID and its index.
func hopCardLine(s *accountsScreen, id string) (tui.CardLine, int) {
	for i, l := range s.workspace.Card.Lines {
		if l.ID == id {
			return l, i
		}
	}
	return tui.CardLine{}, -1
}

func hopScreen(t *testing.T, pre bool) (*accountsScreen, string) {
	t.Helper()
	h := accountProviderHome(t)
	opts := AccountAddOptions{Login: "alice", Provider: "p1", Country: "DE", Exit: "route", ExitGiven: true}
	if pre {
		opts.PreSOCKS5 = cloneHopURL
	}
	if _, e := AccountAdd(h, opts); e != nil {
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
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	return s, h
}

// TestAccountsClearPreHopEndToEnd clears the pre-hop from the card action with
// real keys and verifies the hop is gone from the stored config.
func TestAccountsClearPreHopEndToEnd(t *testing.T) {
	s, h := hopScreen(t, true)
	line, i := hopCardLine(s, "clear-pre")
	if line.ID == "" {
		t.Fatal("card has no clear pre-hop action")
	}
	if line.Disabled {
		t.Fatal("clear pre-hop is disabled although the hop is set")
	}
	// Navigate down to the clear action, then activate it with Enter.
	for n := 0; n < i; n++ {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.workspace.Card.Selected != i {
		t.Fatalf("card selection = %d, want %d", s.workspace.Card.Selected, i)
	}
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on clear pre-hop did not start the update")
	}
	runAccountCmd(s, cmd)
	if s.dialog != nil {
		t.Fatal("clear pre-hop opened a dialog")
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := c.Account("alice")
	if alice.PreSOCKS5 != "" {
		t.Fatalf("pre-hop was not cleared: %q", alice.PreSOCKS5)
	}
	s.refreshSelectedCard(true)
	line, _ = hopCardLine(s, "clear-pre")
	if !line.Disabled {
		t.Fatal("clear pre-hop stayed enabled after clearing")
	}
	v := s.View(120, 30)
	if strings.Contains(v, "fake-hop") || strings.Contains(v, "socks5://") {
		t.Fatal("cleared hop still disclosed its URL")
	}
}

// TestAccountsClearHopDisabledWithoutHop checks that with no hop set the clear
// action is grey and Enter on it does nothing.
func TestAccountsClearHopDisabledWithoutHop(t *testing.T) {
	s, h := hopScreen(t, false)
	for _, id := range []string{"clear-pre", "clear-post"} {
		line, i := hopCardLine(s, id)
		if line.ID == "" || !line.Disabled {
			t.Fatalf("%s = %#v, want a disabled action", id, line)
		}
		for n := 0; n < i; n++ {
			_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil {
			t.Fatalf("Enter on disabled %s produced a command", id)
		}
		if s.dialog != nil {
			t.Fatalf("Enter on disabled %s opened a dialog", id)
		}
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := c.Account("alice")
	if alice.PreSOCKS5 != "" || alice.PostSOCKS5 != "" {
		t.Fatal("disabled clear action changed the config")
	}
}

// TestAccountsHopLinesShowStateOnly checks the card reports set/not set and
// never the hop URL, and the password line reports the waiting state.
func TestAccountsHopLinesShowStateOnly(t *testing.T) {
	s, h := hopScreen(t, true)
	line, _ := hopCardLine(s, "pre")
	if line.Value == "" || strings.Contains(line.Value, "@") || strings.Contains(line.Value, "socks5://") {
		t.Fatalf("pre-hop line shows more than its state: %q", line.Value)
	}
	if !strings.HasSuffix(line.Value, "set") {
		t.Fatalf("pre-hop line = %q, want it to end in set", line.Value)
	}
	pw, _ := hopCardLine(s, "password")
	if pw.Value != "waiting for the first login" {
		t.Fatalf("password line = %q, want the waiting state", pw.Value)
	}
	// Give alice a password and reload: the line flips to set.
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
	s.rows, e = AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	s.syncRows("alice")
	s.refreshSelectedCard(true)
	pw, _ = hopCardLine(s, "password")
	if pw.Value != "set" {
		t.Fatalf("password line after setting one = %q, want set", pw.Value)
	}
	s2, _ := hopScreen(t, false)
	line2, _ := hopCardLine(s2, "pre")
	if !strings.HasSuffix(line2.Value, "none") {
		t.Fatalf("unset pre-hop line = %q, want none", line2.Value)
	}
	pw2, _ := hopCardLine(s2, "password")
	if pw2.Value != "waiting for the first login" {
		t.Fatalf("password line = %q, want the waiting state", pw2.Value)
	}
	v := s.View(120, 30)
	if strings.Contains(v, "fake-hop") || strings.Contains(v, "socks5://") {
		t.Fatal("card view disclosed the hop URL")
	}
}
