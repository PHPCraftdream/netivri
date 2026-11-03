package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func providerEnv(h string) tui.Env {
	return tui.Env{Home: h, Background: func(_ string, w func() tea.Msg) tea.Cmd { return w }}
}
func drainProvider(s *providersScreen, c tea.Cmd) {
	q := []tea.Cmd{c}
	for i := 0; len(q) > 0 && i < 100; i++ {
		x := q[0]
		q = q[1:]
		if x == nil {
			continue
		}
		m := x()
		if b, ok := m.(tea.BatchMsg); ok {
			q = append(q, b...)
			continue
		}
		_, n := s.Update(m)
		if n != nil {
			q = append(q, n)
		}
	}
	if len(q) > 0 {
		panic("provider command loop exceeded")
	}
}
func providerType(s *providersScreen, text string) {
	for _, r := range text {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestProvidersWorkspaceLayoutsAndSelection(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "alpha", URL: "https://user:secret@provider.invalid/path?token=abc"}, config.Subscription{Name: "beta", URL: "https://beta.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	if s.list.AddLabel != "+ Add provider" || !strings.Contains(ansi.Strip(s.View(80, 20)), "Providers") {
		t.Fatal("list/add row missing")
	}
	if strings.Contains(ansi.Strip(s.View(80, 20)), "No providers yet") {
		t.Fatal("unexpected empty message for non-empty provider list")
	}
	if strings.Contains(s.View(100, 18), "secret") || strings.Contains(s.View(120, 30), "token=abc") {
		t.Fatal("subscription secret rendered")
	}
	s.View(120, 30)
	if s.workspace.Width != 120 || s.workspace.Focus() != tui.PaneList {
		t.Fatal("wide workspace")
	}
	s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s.View(80, 24)
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.workspace.Drilled {
		t.Fatal("narrow Enter did not drill")
	}
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.workspace.Drilled {
		t.Fatal("Esc did not return")
	}
	for _, z := range []struct{ w, h int }{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := s.View(z.w, z.h)
		if strings.Count(v, "\n")+1 > z.h {
			t.Fatalf("overflow %dx%d", z.w, z.h)
		}
	}
}
func TestProviderAddRetainsFailureRedactsAndSelects(t *testing.T) {
	h := providerSeed(t)
	calls := 0
	get := func(_ context.Context, _ string, _ fetch.Options) (*fetch.Result, error) {
		calls++
		if calls == 1 {
			return nil, &cli.Error{Code: "FETCH_FAILED", Message: "failed private-token", Hint: "retry private-token"}
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["name"] = "custom"
	s.wizard.Values["url"] = "https://user:private-token@provider.invalid/path?key=private-token"
	s.wizard.Values["via"] = "http://u:proxysecret@proxy.invalid"
	s.wizard.Index = 2
	s.wizard.SetSaving(true)
	s.mode = "add-wizard"
	_, c := s.Update(tui.WizardResultMsg{Values: map[string]string{"name": "custom", "url": "https://user:private-token@provider.invalid/path?key=private-token", "via": "http://u:proxysecret@proxy.invalid"}})
	drainProvider(s, c)
	v := ansi.Strip(s.View(100, 24))
	if strings.Contains(v, "private-token") || strings.Contains(v, "proxysecret") || !strings.Contains(v, "[redacted]") {
		t.Fatalf("unsafe retained error: %q", v)
	}
	if s.dialog == nil || s.wizard.Saving {
		t.Fatal("wizard not retained")
	}
	s.wizard.SetSaving(false)
	_, c = s.Update(tui.WizardResultMsg{Values: map[string]string{"name": "custom", "url": "https://user:private-token@provider.invalid/path?key=private-token", "via": "http://u:proxysecret@proxy.invalid"}})
	drainProvider(s, c)
	r, e := ProviderList(h)
	if e != nil || len(r.Providers) != 1 || r.Providers[0].Name != "custom" || s.selected != "custom" {
		t.Fatalf("added provider %+v %v selected=%q", r, e, s.selected)
	}
	snap, e := subscription.NewStore(h + "/subscriptions").Load("custom")
	if e != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("snapshot %v %v", snap.FetchedAt, e)
	}
}
func TestProviderRefreshRemoveAndDependencies(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	calls := 0
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		calls++
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	drainProvider(s, s.refresh("p", false))
	if calls != 1 || len(s.refreshLines) != 1 {
		t.Fatalf("refresh %d %v", calls, s.refreshLines)
	}
	cfg, e := loadConfig(h)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p"}}
	if e = config.Save(config.ConfigPath(h), cfg); e != nil {
		t.Fatal(e)
	}
	drainProvider(s, s.preflight("p"))
	if s.dialog == nil || !strings.Contains(strings.Join(s.confirmDeps, ","), "alice") {
		t.Fatal("missing dependency confirmation")
	}
	_, c := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if c != nil {
		drainProvider(s, c)
	}
	if s.dialog != nil {
		t.Fatal("decline did not close confirmation")
	}
	s.mode = ""
	drainProvider(s, s.preflight("p"))
	_, c = s.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if c != nil {
		drainProvider(s, c)
	}
	if s.dialog != nil {
		t.Fatal("confirmation should have completed")
	}
	if !strings.Contains(s.message, "SUB_IN_USE") && !strings.Contains(s.message, "alice") {
		t.Fatalf("dependency refusal: %s", s.message)
	}
	cfg, e = loadConfig(h)
	if e != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal("dependency provider removed")
	}
}
func TestProviderCountriesAndRedaction(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected = "p"
	s.mode = "countries"
	drainProvider(s, s.countriesCmd())
	v := ansi.Strip(s.View(80, 8))
	if !strings.Contains(v, "3 nodes") || !strings.Contains(v, "DE: 2") || !strings.Contains(v, "NL: 1") {
		t.Fatalf("countries: %q", v)
	}
	got := redactProviderSecrets("token example.invalid", "https://u:token@example.invalid/path?key=token")
	if strings.Contains(got, "token") || strings.Contains(got, "example.invalid") {
		t.Fatalf("redaction: %q", got)
	}
}
func TestProviderErrorsAreGenericWithoutTypedError(t *testing.T) {
	h := providerSeed(t)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.setError(errors.New("private data"))
	if s.message != "Provider operation failed" {
		t.Fatalf("unsafe error %q", s.message)
	}
}
func TestProviderEditPromptsPersistURLAndVia(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid", Via: "http://old-proxy.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.load())
	s.startEditPrompt("url")
	providerType(s, "https://new.invalid/path")
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal("load provider config failed")
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].URL != "https://new.invalid/path" || cfg.Subscriptions[0].Via != "http://old-proxy.invalid" {
		t.Fatal("URL edit did not preserve unrelated provider settings")
	}
	s.startEditPrompt("via")
	providerType(s, "http://new-proxy.invalid")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, err = loadConfig(h)
	if err != nil || cfg.Subscriptions[0].Via != "http://new-proxy.invalid" || cfg.Subscriptions[0].URL != "https://new.invalid/path" {
		t.Fatal("Via edit did not persist while preserving other settings")
	}
	s.startEditPrompt("url")
	providerType(s, "not a URL")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.prompt == nil || s.prompt.Value != "not a URL" || s.prompt.Saving {
		t.Fatal("invalid URL edit did not retain input and release saving state")
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	providerType(s, "https://retry.invalid")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, err = loadConfig(h)
	if err != nil || cfg.Subscriptions[0].URL != "https://retry.invalid" {
		t.Fatal("corrected URL retry was not persisted")
	}
}
func TestProviderAddWizardUsesKeysAndRetries(t *testing.T) {
	h := providerSeed(t)
	calls := 0
	get := func(_ context.Context, _ string, _ fetch.Options) (*fetch.Result, error) {
		calls++
		if calls == 1 {
			return nil, &cli.Error{Code: "FETCH_FAILED", Message: "failed wizard-secret", Hint: "retry wizard-secret"}
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	key := func(code rune, text string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Text: text} }
	providerType(s, "wizard")
	_, c := s.Update(key(tea.KeyEnter, ""))
	if c != nil {
		drainProvider(s, c)
	}
	providerType(s, "https://user:wizard-secret@provider.invalid/?token=wizard-secret")
	_, c = s.Update(key(tea.KeyEnter, ""))
	if c != nil {
		drainProvider(s, c)
	}
	_, c = s.Update(key(tea.KeyEnter, ""))
	if c != nil {
		drainProvider(s, c)
	}
	// Third Enter leaves the optional via step and reaches the review step.
	if s.wizard == nil || !s.wizard.Review || s.wizard.Saving {
		t.Fatal("third Enter did not reach the review step")
	}
	// Fourth Enter activates Create: the first fetch fails, so the wizard is
	// retained with the entered values and no credential on screen.
	_, c = s.Update(key(tea.KeyEnter, ""))
	drainProvider(s, c)
	if s.wizard == nil || s.wizard.Saving || s.mode != "add-wizard" || s.wizard.Values["name"] != "wizard" || s.wizard.Values["url"] != "https://user:wizard-secret@provider.invalid/?token=wizard-secret" || s.wizard.Values["via"] != "" {
		t.Fatal("wizard did not retain actual entered values after failed submission")
	}
	visible := ansi.Strip(s.View(100, 24))
	if strings.Contains(visible, "wizard-secret") {
		t.Fatal("wizard failure exposed a credential")
	}
	// Fifth Enter retries Create from review and succeeds on the second fetch.
	_, c = s.Update(key(tea.KeyEnter, ""))
	drainProvider(s, c)
	r, err := ProviderList(h)
	if err != nil || len(r.Providers) != 1 || r.Providers[0].Name != "wizard" || calls != 2 {
		t.Fatal("wizard retry did not add provider")
	}
}
func TestProviderRefreshAllRetainsRedactedResults(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "alpha", URL: "https://user:refresh-secret@alpha.invalid/?token=refresh-secret"}, config.Subscription{Name: "beta", URL: "https://beta.invalid"})
	get := func(_ context.Context, raw string, _ fetch.Options) (*fetch.Result, error) {
		if strings.Contains(raw, "alpha") {
			return nil, &cli.Error{Code: "FETCH_FAILED", Message: "failed refresh-secret", Hint: "retry refresh-secret"}
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	drainProvider(s, s.refresh("", true))
	if s.mode != "results" || len(s.refreshLines) != 2 || !strings.Contains(s.refreshLines[0], "alpha") || !strings.Contains(s.refreshLines[1], "beta") {
		t.Fatal("refresh-all did not retain per-provider results")
	}
	if strings.Contains(strings.Join(s.refreshLines, " "), "refresh-secret") {
		t.Fatal("refresh result exposed credential")
	}
	v := ansi.Strip(s.View(100, 24))
	if strings.Contains(v, "refresh-secret") {
		t.Fatal("refresh screen exposed credential")
	}
}
func TestProviderCountryPagingKeysAndResize(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	codes := []string{"AD", "AE", "AF", "AG", "AI", "AL", "AM", "AO", "AQ", "AR", "AS", "AT", "AU"}
	nodes := make([]subscription.Node, 0, len(codes))
	for _, code := range codes {
		nodes = append(nodes, subscription.Node{Country: code})
	}
	if err := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); err != nil {
		t.Fatal("save country snapshot failed")
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected, s.mode = "p", "countries"
	s.height = 8
	drainProvider(s, s.countriesCmd())
	check := func(w, height int, wantOffset int) {
		s.Update(tea.WindowSizeMsg{Width: w, Height: height})
		v := ansi.Strip(s.View(w, height))
		if s.countryOffset != wantOffset || !strings.Contains(v, "13 nodes") || !strings.Contains(v, " of 13") || strings.Count(v, ": 1") > height-4 {
			t.Fatalf("country page/resize mismatch at offset %d", wantOffset)
		}
		for _, line := range strings.Split(v, "\n") {
			if strings.HasPrefix(line, "A") && !strings.Contains(line, ": ") {
				t.Fatal("invalid country row")
			}
		}
	}
	check(80, 8, 0)
	s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	check(80, 8, 4)
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	check(80, 8, 9)
	s.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	check(80, 8, 0)
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	check(100, 10, 0)
}
func TestProviderResultReturnsToastCommand(t *testing.T) {
	h := providerSeed(t)
	e := providerEnv(h)
	e.Toast = func(text string) tea.Cmd { return func() tea.Msg { return tui.ToastMsg(text) } }
	s := newProvidersScreenWith(h, e, nil, nil)
	_, cmd := s.result(providerResultMsg{kind: "add", err: errors.New("failed")})
	if cmd == nil {
		t.Fatal("result did not return toast command")
	}
	msg := cmd()
	switch result := msg.(type) {
	case tui.ToastMsg:
		if result != tui.ToastMsg("Provider operation failed") {
			t.Fatalf("incorrect toast text: %q", result)
		}
	case tea.BatchMsg:
		for _, sub := range result {
			if toast, ok := sub().(tui.ToastMsg); ok && toast == tui.ToastMsg("Provider operation failed") {
				return
			}
		}
		t.Fatal("toast command missing or contained incorrect text")
	default:
		t.Fatalf("expected toast or batched commands, got %T", msg)
	}
}

func TestProviderDisabledEditsExplainMissingOperation(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.selected = "p"
	s.updateCard()
	if !s.card.Lines[6].Disabled || !strings.Contains(s.card.Lines[6].Value, "account references") {
		t.Fatalf("rename reason missing: %+v", s.card.Lines[6])
	}
	if s.card.Lines[7].Disabled || s.card.Lines[8].Disabled {
		t.Fatal("URL and Via edits should be enabled")
	}
}

func TestProviderCountriesViewIsStableAndExactHeight(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected = "p"
	s.mode = "countries"
	drainProvider(s, s.countriesCmd())
	before := ansi.Strip(s.View(80, 20))
	if lines := strings.Split(before, "\n"); len(lines) != 20 {
		t.Fatalf("countries view has %d lines, want 20", len(lines))
	}
	s.busy = "Loading countries"
	s.message = "failed operation"
	s.hint = "retry it"
	after := ansi.Strip(s.View(80, 20))
	if after != before {
		t.Fatalf("busy/message changed the countries body:\nbefore %q\nafter  %q", before, after)
	}
}
