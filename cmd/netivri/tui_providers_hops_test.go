package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/charmbracelet/x/ansi"
)

func selectProviderCardLine(t *testing.T, s *providersScreen, id string) {
	t.Helper()
	for i, l := range s.card.Lines {
		if l.ID == id {
			s.card.Selected = i
			return
		}
	}
	t.Fatalf("no card line %q", id)
}

func providerCardValue(s *providersScreen, id string) string {
	for _, l := range s.card.Lines {
		if l.ID == id {
			return l.Value
		}
	}
	return ""
}

func providerCardDisabled(s *providersScreen, id string) bool {
	for _, l := range s.card.Lines {
		if l.ID == id {
			return l.Disabled
		}
	}
	return false
}

func TestProviderHopsEditAndClearEndToEnd(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid/path"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	if got := providerCardValue(s, "pre"); got != "inherited" {
		t.Fatalf("fresh Pre-hop = %q, want inherited", got)
	}
	if !providerCardDisabled(s, "clear-pre") || !providerCardDisabled(s, "clear-post") {
		t.Fatal("Clear actions must be disabled while the hops are inherited")
	}

	// set the pre hop through its field line
	selectProviderCardLine(t, s, "pre")
	if _, c := s.activateCard(); c != nil {
		drainProvider(s, c)
	}
	if s.mode != "edit-prompt" || s.prompt == nil {
		t.Fatalf("pre prompt did not open: mode=%q", s.mode)
	}
	secretURL := "http://user:hop-secret@proxy.example:8080"
	providerType(s, secretURL)
	if v := ansi.Strip(s.View(100, 30)); strings.Contains(v, "hop-secret") {
		t.Fatalf("hop credentials visible while typing:\n%s", v)
	}
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.dialog != nil || s.prompt != nil {
		t.Fatalf("dialog did not close: mode=%q message=%q", s.mode, s.message)
	}
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subscriptions[0].PreSOCKS5 != secretURL || cfg.Subscriptions[0].PostSOCKS5 != "" {
		t.Fatalf("saved = %+v", cfg.Subscriptions[0])
	}
	if got := providerCardValue(s, "pre"); got != "set" {
		t.Fatalf("Pre-hop after save = %q, want set", got)
	}
	if v := ansi.Strip(s.View(100, 30)); strings.Contains(v, "hop-secret") || strings.Contains(v, "proxy.example") {
		t.Fatalf("hop value visible in the card:\n%s", v)
	}
	if providerCardDisabled(s, "clear-pre") || !providerCardDisabled(s, "clear-post") {
		t.Fatal("only Clear pre-hop must be enabled now")
	}

	// an empty answer leaves the hop as it is
	selectProviderCardLine(t, s, "pre")
	if _, c := s.activateCard(); c != nil {
		drainProvider(s, c)
	}
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, _ = loadConfig(h)
	if s.dialog != nil || cfg.Subscriptions[0].PreSOCKS5 != secretURL {
		t.Fatalf("an empty answer must close the prompt and keep the hop: dialog=%v saved=%q", s.dialog, cfg.Subscriptions[0].PreSOCKS5)
	}

	// none on the post side
	selectProviderCardLine(t, s, "post")
	if _, c := s.activateCard(); c != nil {
		drainProvider(s, c)
	}
	providerType(s, "none")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, _ = loadConfig(h)
	if cfg.Subscriptions[0].PostSOCKS5 != "none" || providerCardValue(s, "post") != "none" {
		t.Fatalf("post = %q, card %q", cfg.Subscriptions[0].PostSOCKS5, providerCardValue(s, "post"))
	}

	// clear both
	for _, id := range []string{"clear-pre", "clear-post"} {
		selectProviderCardLine(t, s, id)
		_, c := s.activateCard()
		drainProvider(s, c)
	}
	cfg, _ = loadConfig(h)
	if cfg.Subscriptions[0].PreSOCKS5 != "" || cfg.Subscriptions[0].PostSOCKS5 != "" {
		t.Fatalf("after clear = %+v", cfg.Subscriptions[0])
	}
	if providerCardValue(s, "pre") != "inherited" || providerCardValue(s, "post") != "inherited" {
		t.Fatalf("card after clear: pre=%q post=%q", providerCardValue(s, "pre"), providerCardValue(s, "post"))
	}
}

func TestProviderHopRejectsInvalidURLAndKeepsPromptOpen(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid/path"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	selectProviderCardLine(t, s, "post")
	if _, c := s.activateCard(); c != nil {
		drainProvider(s, c)
	}
	providerType(s, "ftp://leak-me.example:21")
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.prompt == nil || s.dialog == nil {
		t.Fatal("an invalid hop must keep the prompt open")
	}
	cfg, _ := loadConfig(h)
	if cfg.Subscriptions[0].PostSOCKS5 != "" {
		t.Fatalf("invalid hop was saved: %q", cfg.Subscriptions[0].PostSOCKS5)
	}
	if v := ansi.Strip(s.View(100, 30)); strings.Contains(v, "leak-me") {
		t.Fatalf("hop value visible after the error:\n%s", v)
	}
	checkProvidersFrame(t, s, 100, 30)
	checkProvidersFrame(t, s, 60, 16)
}
