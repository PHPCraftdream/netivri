package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

// checkProvidersFrame asserts the stable rendering contract: exactly h lines,
// no line wider than w (ANSI-stripped, rune-width aware).
func checkProvidersFrame(t *testing.T, s tui.Screen, w, h int) {
	t.Helper()
	v := ansi.Strip(s.View(w, h))
	lines := strings.Split(v, "\n")
	if len(lines) != h {
		t.Fatalf("view at %dx%d has %d lines, want %d", w, h, len(lines), h)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > w {
			t.Fatalf("line wider than %d at %dx%d: %q", w, w, h, line)
		}
	}
}

func TestProviderRenameEndToEndFollowsAccountsAndSnapshot(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "old", URL: "https://old.invalid/path"})
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "old"}}
	if err = config.Save(config.ConfigPath(h), cfg); err != nil {
		t.Fatal(err)
	}
	if err = subscription.NewStore(h+"/subscriptions").Save("old", subscription.Snapshot{Nodes: []subscription.Node{{ID: "n1", Country: "DE"}}, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	if s.selected != "old" {
		t.Fatalf("selected %q", s.selected)
	}
	for i, l := range s.card.Lines {
		if l.ID == "edit-name" {
			s.card.Selected = i
			break
		}
	}
	if _, c := s.activateCard(); c != nil {
		drainProvider(s, c)
	}
	if s.mode != "rename-prompt" || s.prompt == nil || s.prompt.Kind != tui.TextPrompt {
		t.Fatalf("rename prompt did not open: mode=%q prompt=%v", s.mode, s.prompt)
	}
	if s.prompt.Value != "old" {
		t.Fatalf("rename prompt not prefilled: %q", s.prompt.Value)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	providerType(s, "renamed")
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("rename did not close the dialog")
	}
	if s.selected != "renamed" || s.pendingSelect != "renamed" {
		t.Fatalf("selection did not follow rename: %q %q", s.selected, s.pendingSelect)
	}
	if s.message != "Provider renamed" {
		t.Fatalf("toast message: %q", s.message)
	}
	cfg, err = loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "renamed" || cfg.Subscriptions[0].URL != "https://old.invalid/path" {
		t.Fatalf("config after rename: %+v", cfg.Subscriptions)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Provider != "renamed" {
		t.Fatalf("account provider did not follow: %+v", cfg.Accounts)
	}
	found := false
	for _, row := range s.list.Rows {
		if row.Data == "renamed" {
			found = true
		}
	}
	if !found {
		t.Fatal("renamed provider missing from list after reload")
	}
	store := subscription.NewStore(h + "/subscriptions")
	snap, err := store.Load("renamed")
	if err != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("renamed snapshot: %v %v", snap.FetchedAt, err)
	}
	if _, err = store.Load("old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old snapshot still present: %v", err)
	}
}

func TestProviderRenameScreenValidationFailureWritesNothing(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "old", URL: "https://old.invalid"})
	if err := subscription.NewStore(h+"/subscriptions").Save("old", subscription.Snapshot{FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	cfgPath := config.ConfigPath(h)
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	for i, l := range s.card.Lines {
		if l.ID == "edit-name" {
			s.card.Selected = i
			break
		}
	}
	s.activateCard()
	_, _ = s.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	providerType(s, "bad/name")
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.dialog == nil || s.prompt == nil || s.prompt.Saving {
		t.Fatal("failed rename did not retain the prompt")
	}
	if s.prompt.Error == nil || !strings.Contains(s.prompt.Error.Error(), "must match") {
		if s.prompt.Error != nil {
			t.Fatalf("prompt error: %v", s.prompt.Error)
		}
		t.Fatal("validation error not shown in prompt")
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed rename wrote the config")
	}
	if _, err = subscription.NewStore(h + "/subscriptions").Load("old"); err != nil {
		t.Fatalf("old snapshot disturbed: %v", err)
	}
	// Esc asks to discard the typed answer; Discard (focus right, Enter) closes.
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.prompt == nil || len(s.prompt.Buttons.Labels) != 2 || s.prompt.Buttons.Labels[0] != "Continue" {
		t.Fatal("esc on a changed rename prompt did not ask to discard")
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("discard did not close the failed rename prompt")
	}
}

func TestProviderRenameDialogFrameContract(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "old", URL: "https://old.invalid"})
	s := newProvidersScreenWith(h, providerEnv(h), nil, func() time.Time { return testNow })
	drainProvider(s, s.Init())
	for i, l := range s.card.Lines {
		if l.ID == "edit-name" {
			s.card.Selected = i
			break
		}
	}
	s.activateCard()
	for _, z := range []struct{ w, hgt int }{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		checkProvidersFrame(t, s, z.w, z.hgt)
	}
}
