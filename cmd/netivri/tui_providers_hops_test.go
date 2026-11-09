package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
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

func TestProviderClearHopTargetsSelectionAtCreation(t *testing.T) {
	for _, side := range []string{"pre", "post"} {
		t.Run(side, func(t *testing.T) {
			alphaPre, alphaPost := "http://alpha-pre.invalid:8080", "http://alpha-post.invalid:8081"
			betaPre, betaPost := "http://beta-pre.invalid:8082", "http://beta-post.invalid:8083"
			h := providerSeed(t,
				config.Subscription{Name: "alpha", URL: "https://alpha.invalid", PreSOCKS5: alphaPre, PostSOCKS5: alphaPost},
				config.Subscription{Name: "beta", URL: "https://beta.invalid", PreSOCKS5: betaPre, PostSOCKS5: betaPost},
			)
			s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
			drainProvider(s, s.Init())
			if s.selected != "alpha" {
				t.Fatalf("initial selection = %q, want alpha", s.selected)
			}

			var heldWorker func() tea.Msg
			s.env.Background = func(label string, work func() tea.Msg) tea.Cmd {
				heldWorker = work
				return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
			}
			var calledHome, calledName string
			var calledOptions ProviderUpdateOptions
			update := s.updateProvider
			s.updateProvider = func(home, name string, options ProviderUpdateOptions) error {
				calledHome, calledName, calledOptions = home, name, options
				return update(home, name, options)
			}

			cmd := s.clearHop(side)
			if heldWorker == nil || cmd == nil {
				t.Fatal("clear did not schedule a held background worker")
			}
			_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			if s.selected != "beta" || s.selectedName() != "beta" {
				t.Fatalf("selection after Down = screen %q, list %q; want beta", s.selected, s.selectedName())
			}

			background := cmd()
			wrapped, ok := background.(tui.BackgroundMsg)
			if !ok {
				t.Fatalf("background result = %T, want tui.BackgroundMsg", background)
			}
			result, ok := wrapped.Value.(providerResultMsg)
			if !ok {
				t.Fatalf("worker result = %T, want providerResultMsg", wrapped.Value)
			}
			if result.kind != "edit" || result.value != "alpha" || result.err != nil {
				t.Fatal("clear worker did not return a successful result for its captured target")
			}
			if calledHome != h || calledName != "alpha" {
				t.Fatalf("ProviderUpdate target home=%q name=%q, want %q alpha", calledHome, calledName, h)
			}
			if side == "pre" {
				if calledOptions.Pre == nil || *calledOptions.Pre != "" || calledOptions.Post != nil {
					t.Fatalf("clear-pre options = %+v, want only empty Pre", calledOptions)
				}
			} else if calledOptions.Post == nil || *calledOptions.Post != "" || calledOptions.Pre != nil {
				t.Fatalf("clear-post options = %+v, want only empty Post", calledOptions)
			}

			_, load := s.Update(background)
			if load == nil || s.pendingSelect != "alpha" {
				t.Fatalf("successful edit pending selection = %q, load command = %v; want alpha and load", s.pendingSelect, load != nil)
			}
			_, loadResult := s.Update(load())
			if loadResult != nil {
				t.Fatalf("provider list result unexpectedly scheduled another command: %v", loadResult)
			}
			if s.selected != "alpha" {
				t.Fatalf("selection after edit result = %q, want edited provider alpha", s.selected)
			}

			cfg, err := loadConfig(h)
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string]config.Subscription, len(cfg.Subscriptions))
			for _, sub := range cfg.Subscriptions {
				got[sub.Name] = sub
			}
			if side == "pre" {
				if got["alpha"].PreSOCKS5 != "" || got["alpha"].PostSOCKS5 != alphaPost {
					t.Fatal("clear-pre changed the wrong alpha hop")
				}
			} else if got["alpha"].PostSOCKS5 != "" || got["alpha"].PreSOCKS5 != alphaPre {
				t.Fatal("clear-post changed the wrong alpha hop")
			}
			if got["beta"].PreSOCKS5 != betaPre || got["beta"].PostSOCKS5 != betaPost {
				t.Fatal("clear changed beta's hops")
			}
		})
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
