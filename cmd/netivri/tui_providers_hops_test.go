package main

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func selectProviderCardLine(t *testing.T, s *providersScreen, id string) {
	t.Helper()
	for i, line := range s.card.Lines {
		if line.ID == id {
			s.card.Selected = i
			return
		}
	}
	t.Fatalf("no card line %q", id)
}

func providerCardValue(s *providersScreen, id string) string {
	for _, line := range s.card.Lines {
		if line.ID == id {
			return line.Value
		}
	}
	return ""
}

func providerCardDisabled(s *providersScreen, id string) bool {
	for _, line := range s.card.Lines {
		if line.ID == id {
			return line.Disabled
		}
	}
	return false
}

// providerChooseHopOperation selects through the actual choice prompt and
// dispatches its result through providersScreen.Update.
func providerChooseHopOperation(t *testing.T, s *providersScreen, value string) tea.Cmd {
	t.Helper()
	if s.prompt == nil {
		t.Fatal("hop picker prompt is not open")
	}
	choice := -1
	for i, option := range s.prompt.Options {
		if option.Value == value {
			choice = i
			break
		}
	}
	if choice < 0 {
		t.Fatalf("hop picker has no operation %q", value)
	}
	for i := 0; i < choice; i++ {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	_, chosen := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if chosen == nil {
		t.Fatal("hop picker Enter returned no result")
	}
	_, operation := s.Update(chosen())
	return operation
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

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	if s.mode != "hop-picker" || s.prompt == nil || s.prompt.Kind != tui.ChoicePrompt {
		t.Fatalf("pre-hop picker did not open: mode=%q", s.mode)
	}
	if s.prompt.Value != "" || strings.Contains(ansi.Strip(s.View(100, 30)), "main.invalid") {
		t.Fatal("provider picker prefilled or disclosed a URL")
	}
	cmd := providerChooseHopOperation(t, s, "replace")
	if cmd != nil || s.mode != "edit-prompt" || s.prompt == nil || s.prompt.Kind != tui.SecretPrompt || s.prompt.Value != "" {
		t.Fatal("replace did not open a blank secret prompt")
	}
	secretURL := "http://user:hop-secret@proxy.example:8080"
	providerType(s, secretURL)
	if view := ansi.Strip(s.View(100, 30)); strings.Contains(view, "hop-secret") {
		t.Fatal("hop credentials visible while typing")
	}
	_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	if s.dialog != nil || s.prompt != nil {
		t.Fatalf("dialog did not close: mode=%q", s.mode)
	}
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{secretURL}) || len(cfg.Subscriptions[0].PostSOCKS5) != 0 {
		t.Fatalf("saved hop state mismatch: pre count=%d post count=%d", len(cfg.Subscriptions[0].PreSOCKS5), len(cfg.Subscriptions[0].PostSOCKS5))
	}
	if got := providerCardValue(s, "pre"); got != "set" {
		t.Fatalf("Pre-hop after save = %q, want set", got)
	}
	if view := ansi.Strip(s.View(100, 30)); strings.Contains(view, "proxy.example") || strings.Contains(view, "hop-secret") {
		t.Fatal("provider card disclosed hop data")
	}
	if providerCardDisabled(s, "clear-pre") || !providerCardDisabled(s, "clear-post") {
		t.Fatal("only Clear pre-hop must be enabled now")
	}

	// Empty replacement input preserves the legacy "leave unchanged" shortcut.
	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "replace")
	_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	cfg, _ = loadConfig(h)
	if s.dialog != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{secretURL}) {
		t.Fatal("empty replacement did not leave the saved hop unchanged")
	}

	selectProviderCardLine(t, s, "post")
	_, _ = s.activateCard()
	cmd = providerChooseHopOperation(t, s, "none")
	drainProvider(s, cmd)
	cfg, _ = loadConfig(h)
	if !slices.Equal(cfg.Subscriptions[0].PostSOCKS5, config.HopList{"none"}) || providerCardValue(s, "post") != "none" {
		t.Fatalf("post-hop list count=%d, card %q", len(cfg.Subscriptions[0].PostSOCKS5), providerCardValue(s, "post"))
	}
	listed, err := ProviderList(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Providers) != 1 || !slices.Equal(listed.Providers[0].Pre, []string{"http://proxy.example:8080"}) || !slices.Equal(listed.Providers[0].Post, []string{"none"}) {
		t.Fatalf("provider hop list shape mismatch: providers=%d", len(listed.Providers))
	}

	for _, id := range []string{"clear-pre", "clear-post"} {
		selectProviderCardLine(t, s, id)
		_, cmd = s.activateCard()
		drainProvider(s, cmd)
	}
	cfg, _ = loadConfig(h)
	if len(cfg.Subscriptions[0].PreSOCKS5) != 0 || len(cfg.Subscriptions[0].PostSOCKS5) != 0 {
		t.Fatal("clear actions did not remove both provider overrides")
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
				config.Subscription{Name: "alpha", URL: "https://alpha.invalid", PreSOCKS5: config.HopList{alphaPre}, PostSOCKS5: config.HopList{alphaPost}},
				config.Subscription{Name: "beta", URL: "https://beta.invalid", PreSOCKS5: config.HopList{betaPre}, PostSOCKS5: config.HopList{betaPost}},
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
			if !ok || result.kind != "edit" || result.value != "alpha" || result.err != nil {
				t.Fatal("clear worker did not return a successful result for its captured target")
			}
			if calledHome != h || calledName != "alpha" {
				t.Fatalf("ProviderUpdate target home=%q name=%q, want %q alpha", calledHome, calledName, h)
			}
			if side == "pre" {
				if edit := calledOptions.PreEdit; edit == nil || edit.Replace == nil || len(*edit.Replace) != 0 || calledOptions.PostEdit != nil || calledOptions.Pre != nil || calledOptions.Post != nil {
					t.Fatal("clear-pre did not pass only an empty replacement PreEdit")
				}
			} else if edit := calledOptions.PostEdit; edit == nil || edit.Replace == nil || len(*edit.Replace) != 0 || calledOptions.PreEdit != nil || calledOptions.Pre != nil || calledOptions.Post != nil {
				t.Fatal("clear-post did not pass only an empty replacement PostEdit")
			}

			_, load := s.Update(background)
			if load == nil || s.pendingSelect != "alpha" {
				t.Fatalf("successful edit pending selection = %q, load command = %v; want alpha and load", s.pendingSelect, load != nil)
			}
			_, loadResult := s.Update(load())
			if loadResult != nil || s.selected != "alpha" {
				t.Fatal("provider edit reload did not retain the edited selection")
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
				if len(got["alpha"].PreSOCKS5) != 0 || !slices.Equal(got["alpha"].PostSOCKS5, config.HopList{alphaPost}) {
					t.Fatal("clear-pre changed the wrong alpha hop")
				}
			} else if len(got["alpha"].PostSOCKS5) != 0 || !slices.Equal(got["alpha"].PreSOCKS5, config.HopList{alphaPre}) {
				t.Fatal("clear-post changed the wrong alpha hop")
			}
			if !slices.Equal(got["beta"].PreSOCKS5, config.HopList{betaPre}) || !slices.Equal(got["beta"].PostSOCKS5, config.HopList{betaPost}) {
				t.Fatal("clear changed beta's hops")
			}
		})
	}
}

func TestProviderHopListPickerAddRemoveAndInherit(t *testing.T) {
	oldPre := "http://pre-user:pre-secret@pre.example:8080"
	oldPost := "https://post-user:post-secret@post.example:8443"
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid/path", PreSOCKS5: config.HopList{oldPre}, PostSOCKS5: config.HopList{oldPost}})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	if s.prompt == nil || s.prompt.Value != "" || strings.Contains(ansi.Strip(s.View(100, 30)), "pre.example") || strings.Contains(ansi.Strip(s.View(100, 30)), "pre-secret") {
		t.Fatal("operation picker prefilled or exposed an existing hop")
	}
	if providerChooseHopOperation(t, s, "add") != nil || s.prompt == nil || s.prompt.Kind != tui.SecretPrompt || s.prompt.Value != "" {
		t.Fatal("Add did not open a blank secret prompt")
	}
	newSecret := "socks5://second-user:second-secret@second.example:1080"
	providerType(s, newSecret)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Add did not attempt a production update")
	}
	drainProvider(s, cmd)
	if s.dialog == nil || s.prompt == nil || s.prompt.Kind != tui.SecretPrompt || s.prompt.Error == nil {
		t.Fatal("production list limit did not retain the secret prompt")
	}
	if strings.Contains(ansi.Strip(s.View(100, 30)), "second-secret") || strings.Contains(s.message, "second-secret") || strings.Contains(s.prompt.Error.Error(), "second-secret") {
		t.Fatal("failed add exposed the new credentials")
	}
	cfg, err := loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{oldPre}) || !slices.Equal(cfg.Subscriptions[0].PostSOCKS5, config.HopList{oldPost}) {
		t.Fatalf("capacity failure changed provider hops: err=%v", err)
	}
	_, _ = s.Update(tui.PromptResultMsg{Canceled: true})

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	for _, option := range s.prompt.Options {
		if strings.HasPrefix(option.Value, "remove:") && (strings.Contains(option.Label, "pre.example") || strings.Contains(option.Label, "pre-secret")) {
			t.Fatal("unsafe remove option label")
		}
	}
	cmd = providerChooseHopOperation(t, s, "remove:1")
	drainProvider(s, cmd)
	cfg, err = loadConfig(h)
	if err != nil || len(cfg.Subscriptions[0].PreSOCKS5) != 0 || !slices.Equal(cfg.Subscriptions[0].PostSOCKS5, config.HopList{oldPost}) {
		t.Fatalf("remove did not edit only pre list: err=%v", err)
	}

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "replace")
	replacement := "https://replace-user:replace-secret@replace.example:443"
	providerType(s, replacement)
	_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	foundInherit := false
	for _, option := range s.prompt.Options {
		if option.Value == "inherit" {
			foundInherit = true
		}
	}
	if !foundInherit {
		t.Fatal("Inherit was not offered for a configured provider list")
	}
	cmd = providerChooseHopOperation(t, s, "inherit")
	drainProvider(s, cmd)
	cfg, err = loadConfig(h)
	if err != nil || len(cfg.Subscriptions[0].PreSOCKS5) != 0 || !slices.Equal(cfg.Subscriptions[0].PostSOCKS5, config.HopList{oldPost}) {
		t.Fatalf("Inherit did not clear only provider pre-hop: err=%v", err)
	}
	if providerCardValue(s, "pre") != "inherited" || providerCardValue(s, "post") != "set" {
		t.Fatalf("provider card after inherit: pre=%q post=%q", providerCardValue(s, "pre"), providerCardValue(s, "post"))
	}
	if strings.Contains(ansi.Strip(s.View(100, 30)), "replace.example") || strings.Contains(ansi.Strip(s.View(100, 30)), "post.example") {
		t.Fatal("provider card exposed hop hosts")
	}
}

func TestProviderHopPickerCancelAndSentinelInputAreNoOps(t *testing.T) {
	oldPre := "http://picker-user:picker-secret@picker.example:8080"
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid", PreSOCKS5: config.HopList{oldPre}})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	if strings.Contains(ansi.Strip(s.View(100, 30)), "picker.example") || strings.Contains(ansi.Strip(s.View(100, 30)), "picker-user") {
		t.Fatal("operation picker displayed host or userinfo")
	}
	_, cancel := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancel == nil {
		t.Fatal("canceling the operation picker returned no result")
	}
	_, _ = s.Update(cancel())
	if s.dialog != nil || s.mode != "" {
		t.Fatal("canceling the operation picker did not close it")
	}
	cfg, err := loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{oldPre}) {
		t.Fatalf("cancel changed provider config: err=%v", err)
	}

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "add")
	for i, value := range []string{"none", "inherit", ""} {
		if value == "" {
			for j := 0; j < len("inherit"); j++ {
				_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
		} else {
			for j := 0; j < len(s.prompt.Value); j++ {
				_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
			providerType(s, value)
		}
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("Add Enter case %d returned no PromptResultMsg command", i)
		}
		drainProvider(s, cmd)
		if s.prompt == nil || s.prompt.Error == nil || s.prompt.Saving {
			t.Fatalf("Add invalid-input case %d did not retain the failed prompt", i)
		}
		cfg, err := loadConfig(h)
		if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{oldPre}) {
			t.Fatalf("rejected Add case %d changed saved provider list", i)
		}
	}
}

func TestProviderHopPickerAcceptsValidAdd(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())

	selectProviderCardLine(t, s, "post")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "add")
	replacement := "https://replacement-user:replacement-secret@replacement.example:443"
	providerType(s, replacement)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	cfg, err := loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PostSOCKS5, config.HopList{replacement}) {
		t.Fatalf("valid single-hop Add was not applied through ProviderUpdate: err=%v", err)
	}
	if strings.Contains(ansi.Strip(s.View(100, 30)), "replacement.example") || strings.Contains(ansi.Strip(s.View(100, 30)), "replacement-secret") {
		t.Fatal("valid Add exposed its credentials or host in the card")
	}
}

func TestProviderHopPickerRejectsTypedSentinelsAndKeepsLegacyNone(t *testing.T) {
	oldPre := "http://old.invalid:8080"
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid", PreSOCKS5: config.HopList{oldPre}})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "replace")
	providerType(s, "inherit")
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	if s.prompt == nil || s.prompt.Error == nil {
		t.Fatal("typed Inherit was accepted as a proxy URL")
	}
	cfg, err := loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{oldPre}) {
		t.Fatalf("rejected sentinel changed existing list: err=%v", err)
	}
	_, discard := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if discard != nil || s.prompt == nil || !strings.Contains(ansi.Strip(s.View(100, 30)), "Discard changes? Continue editing or discard") {
		t.Fatal("changed hop prompt did not show the discard confirmation")
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	_, discard = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if discard == nil {
		t.Fatal("discarding the changed hop prompt returned no PromptResultMsg command")
	}
	_, _ = s.Update(discard())
	if s.prompt != nil || s.dialog != nil {
		t.Fatal("discard result did not close the hop prompt")
	}
	cfg, err = loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{oldPre}) {
		t.Fatal("discarding the changed prompt altered the saved hop")
	}

	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "replace")
	providerType(s, "none")
	_, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	cfg, err = loadConfig(h)
	if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{"none"}) {
		t.Fatalf("legacy none replacement was not saved: err=%v", err)
	}
	selectProviderCardLine(t, s, "pre")
	_, _ = s.activateCard()
	for _, option := range s.prompt.Options {
		if option.Value == "add" {
			t.Fatal("Add was offered for a provider list explicitly set to None")
		}
	}
	_, _ = s.Update(tui.PromptResultMsg{Canceled: true})
}

func TestProviderHopPickerRejectsInvalidChoicesWithoutSaving(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid", PreSOCKS5: config.HopList{"http://valid.invalid:8080"}})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())

	for _, choice := range []string{"remove:0", "remove:bad", "unknown"} {
		selectProviderCardLine(t, s, "pre")
		_, _ = s.activateCard()
		_, _ = s.Update(tui.PromptResultMsg{Value: choice})
		if s.prompt == nil || s.dialog == nil || s.prompt.Error == nil || s.prompt.Saving {
			t.Fatalf("invalid picker choice did not retain picker with error: %s", choice)
		}
		cfg, err := loadConfig(h)
		if err != nil || !slices.Equal(cfg.Subscriptions[0].PreSOCKS5, config.HopList{"http://valid.invalid:8080"}) {
			t.Fatalf("invalid picker choice %q changed config: err=%v", choice, err)
		}
		_, _ = s.Update(tui.PromptResultMsg{Canceled: true})
	}
}

func TestProviderHopListCardShowsSafeOrderedAlternatives(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.selected = "main"
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{
		Name: "main",
		Pre:  []string{"http://first.example:8080", "https://second.example:8443"},
	}}})
	s.updateCard()
	got := providerCardValue(s, "pre")
	for _, want := range []string{"2 alternatives", "1 set", "2 set"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pre-hop label %q missing %q", got, want)
		}
	}
	if strings.Contains(ansi.Strip(s.View(100, 30)), "first.example") || strings.Contains(ansi.Strip(s.View(100, 30)), "second.example") || strings.Contains(providerCardValue(s, "pre"), "→") {
		t.Fatal("card exposed hosts or implied serial chaining")
	}
	if strings.Index(got, "1 set") > strings.Index(got, "2 set") {
		t.Fatal("alternatives are out of order")
	}
}

func TestProviderHopRejectsInvalidURLAndKeepsPromptOpen(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://main.invalid/path"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	selectProviderCardLine(t, s, "post")
	_, _ = s.activateCard()
	_ = providerChooseHopOperation(t, s, "replace")
	if s.mode != "edit-prompt" || s.prompt == nil || s.prompt.Kind != tui.SecretPrompt {
		t.Fatalf("post-hop replacement prompt did not open: mode=%q", s.mode)
	}
	providerType(s, "ftp://leak-me.example:21")
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, cmd)
	if s.prompt == nil || s.dialog == nil {
		t.Fatal("an invalid hop must keep the prompt open")
	}
	cfg, _ := loadConfig(h)
	if len(cfg.Subscriptions[0].PostSOCKS5) != 0 {
		t.Fatalf("invalid hop list was saved (count=%d)", len(cfg.Subscriptions[0].PostSOCKS5))
	}
	if view := ansi.Strip(s.View(100, 30)); strings.Contains(view, "leak-me") {
		t.Fatal("hop value visible after the error")
	}
	checkProvidersFrame(t, s, 100, 30)
	checkProvidersFrame(t, s, 60, 16)
}
