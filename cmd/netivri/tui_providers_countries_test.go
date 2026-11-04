package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func countriesSeed(t *testing.T) (string, *providersScreen) {
	t.Helper()
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://user:secret@p.invalid/path"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	return h, s
}

func openCountries(t *testing.T, s *providersScreen) {
	t.Helper()
	_, c := s.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if c != nil {
		drainProvider(s, c)
	}
	if s.mode != "countries-prompt" || s.dialog == nil || s.prompt == nil {
		t.Fatalf("countries picker did not open: mode=%q", s.mode)
	}
}

func TestProviderCountriesPickerToggleAndApply(t *testing.T) {
	var toastText string
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://user:secret@p.invalid/path"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	e := providerEnv(h)
	e.Toast = func(text string) tea.Cmd { toastText = text; return func() tea.Msg { return tui.ToastMsg(text) } }
	s := newProvidersScreen(h, e).(*providersScreen)
	drainProvider(s, s.Init())
	if s.card.Lines[4].Value != "DE:2, NL:1" {
		t.Fatalf("card summary: %q", s.card.Lines[4].Value)
	}
	openCountries(t, s)
	if s.prompt.Title() != "Provider p: countries" {
		t.Fatalf("picker title: %q", s.prompt.Title())
	}
	if len(s.prompt.Options) != 2 {
		t.Fatalf("options: %+v", s.prompt.Options)
	}
	if s.prompt.Options[0].Label != "DE — 2 nodes" || s.prompt.Options[0].Value != "DE" {
		t.Fatalf("DE option: %+v", s.prompt.Options[0])
	}
	if len(s.prompt.Selected) != 2 {
		t.Fatalf("preselect: %v", s.prompt.Selected)
	}
	// Space toggles the cursor option (DE) off.
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if len(s.prompt.Selected) != 1 {
		t.Fatalf("space did not toggle DE off: %v", s.prompt.Selected)
	}
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("apply did not close the picker")
	}
	if toastText != "Countries applied: NL" {
		t.Fatalf("toast: %q", toastText)
	}
	if s.card.Lines[4].Value != "NL" {
		t.Fatalf("card countries after apply: %q", s.card.Lines[4].Value)
	}
}

func TestProviderCountriesPickerCancelKeepsSummary(t *testing.T) {
	h, s := countriesSeed(t)
	_ = h
	openCountries(t, s)
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	// Space toggling a multi-choice option does not set the prompt's changed
	// flag, so Esc cancels directly by returning a PromptResultMsg command;
	// the picker closes only once that command is delivered.
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if c == nil {
		t.Fatal("esc on the picker produced no command")
	}
	_, _ = s.Update(c())
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("esc did not cancel the picker")
	}
	if s.card.Lines[4].Value != "DE:2, NL:1" {
		t.Fatalf("cancel changed the card summary: %q", s.card.Lines[4].Value)
	}
	// Reopening restores the preselect from the card summary.
	openCountries(t, s)
	if len(s.prompt.Selected) != 2 {
		t.Fatalf("reopen preselect: %v", s.prompt.Selected)
	}
}

func TestProviderCountriesPickerFrameContract(t *testing.T) {
	_, s := countriesSeed(t)
	openCountries(t, s)
	for _, z := range []struct{ w, hgt int }{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		checkProvidersFrame(t, s, z.w, z.hgt)
	}
}

func TestProviderCardSubscriptionIsMaskedNotURL(t *testing.T) {
	_, s := countriesSeed(t)
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	v := ansi.Strip(s.View(100, 24))
	if !strings.Contains(v, "•••••• (set)") {
		t.Fatalf("masked subscription value missing: %q", v)
	}
	for _, banned := range []string{"p.invalid", "secret", "https://", "/path"} {
		if strings.Contains(v, banned) {
			t.Fatalf("card view exposed subscription material %q", banned)
		}
	}
}
