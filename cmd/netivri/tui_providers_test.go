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
func TestProviderCountriesDialogOpensPreselected(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	s.selected = "p"
	s.updateCard()
	drainProvider(s, s.countriesCmd())
	if s.mode != "countries-prompt" || s.dialog == nil || s.prompt == nil || s.prompt.Kind != tui.MultiChoicePrompt {
		t.Fatalf("countries dialog state: mode=%q prompt=%v", s.mode, s.prompt)
	}
	for _, want := range []string{"DE", "NL"} {
		found := false
		for _, v := range s.prompt.Selected {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("preselect missing %s: %v", want, s.prompt.Selected)
		}
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
		if calls <= 2 {
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
	// Third Enter leaves the optional via step and lands on the preview step,
	// which fetches the subscription; this first fetch fails.
	_, c = s.Update(key(tea.KeyEnter, ""))
	drainProvider(s, c)
	if s.wizard == nil || s.previewState != "failed" || s.wizard.Busy || s.previewIndex() != s.wizard.Index {
		t.Fatalf("third Enter did not run the preview: state=%q busy=%v index=%d", s.previewState, s.wizard.Busy, s.wizard.Index)
	}
	// Fourth Enter leaves the preview step and reaches the review step.
	_, c = s.Update(key(tea.KeyEnter, ""))
	if c != nil {
		drainProvider(s, c)
	}
	if s.wizard == nil || !s.wizard.Review {
		t.Fatal("fourth Enter did not reach the review step")
	}
	// Fifth Enter activates Create: the add fetch (second call) fails, so the
	// wizard is retained with the entered values and no credential on screen.
	_, c = s.Update(key(tea.KeyEnter, ""))
	drainProvider(s, c)
	if s.wizard == nil || s.wizard.Saving || s.mode != "add-wizard" || s.wizard.Values["name"] != "wizard" || s.wizard.Values["url"] != "https://user:wizard-secret@provider.invalid/?token=wizard-secret" || s.wizard.Values["via"] != "" {
		t.Fatal("wizard did not retain actual entered values after failed submission")
	}
	visible := ansi.Strip(s.View(100, 24))
	if strings.Contains(visible, "wizard-secret") {
		t.Fatal("wizard failure exposed a credential")
	}
	// Sixth Enter retries Create from review (third fetch) and succeeds.
	_, c = s.Update(key(tea.KeyEnter, ""))
	drainProvider(s, c)
	r, err := ProviderList(h)
	if err != nil || len(r.Providers) != 1 || r.Providers[0].Name != "wizard" || calls != 3 {
		t.Fatalf("wizard retry did not add provider: calls=%d err=%v", calls, err)
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
func TestProviderCountriesDialogKeysAndFrame(t *testing.T) {
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
	s.selected = "p"
	drainProvider(s, s.countriesCmd())
	if s.mode != "countries-prompt" || s.dialog == nil {
		t.Fatal("countries dialog did not open")
	}
	for _, z := range []struct{ w, hgt int }{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(s.View(z.w, z.hgt))
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > z.w {
				t.Fatalf("line wider than %d: %q", z.w, line)
			}
			if strings.Count(v, "\n")+1 != z.hgt {
				t.Fatalf("countries dialog view has %d lines, want %d", strings.Count(v, "\n")+1, z.hgt)
			}
		}
	}
	if !strings.Contains(ansi.Strip(s.View(80, 24)), "Provider p: countries") {
		t.Fatal("countries dialog title missing")
	}
	before := s.card.Lines[4].Value
	// Esc cancels without changing anything.
	_, escCmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if escCmd != nil {
		if m := escCmd(); m != nil {
			_, _ = s.Update(m)
		}
	}
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("esc did not close the countries dialog")
	}
	if s.card.Lines[4].Value != before {
		t.Fatalf("esc changed the card countries line: %q -> %q", before, s.card.Lines[4].Value)
	}
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

func TestProviderRenameActionEnabledAndEditsKept(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.selected = "p"
	s.updateCard()
	byID := map[string]tui.CardLine{}
	for _, l := range s.card.Lines {
		byID[l.ID] = l
	}
	if byID["edit-name"].Disabled || byID["edit-name"].Label != "Rename" {
		t.Fatalf("rename action missing: %+v", byID["edit-name"])
	}
	if byID["edit-subscription"].Disabled || byID["edit-via"].Disabled {
		t.Fatal("URL and Via edits should be enabled")
	}
}

func TestProviderCountriesDialogIsStableAndExactHeight(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	nodes := []subscription.Node{{Country: "DE"}, {Country: "DE"}, {Country: "NL"}}
	if e := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected = "p"
	drainProvider(s, s.countriesCmd())
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	before := ansi.Strip(s.View(80, 20))
	if lines := strings.Split(before, "\n"); len(lines) != 20 {
		t.Fatalf("countries dialog view has %d lines, want 20", len(lines))
	}
	s.busy = "Loading countries"
	s.message = "failed operation"
	s.hint = "retry it"
	after := ansi.Strip(s.View(80, 20))
	if after != before {
		t.Fatalf("busy/message changed the countries dialog:\nbefore %q\nafter  %q", before, after)
	}
}

// TestProviderFieldLineEnterOpensEditPrompt checks the spec: Enter on the
// Subscription or Via card FIELD line opens its edit prompt directly, without
// needing the separate edit action line.
func TestProviderFieldLineEnterOpensEditPrompt(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	// Real keys: Tab moves focus to the card pane.
	s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if s.workspace.Focus() != tui.PaneCard {
		t.Fatal("Tab did not focus the card pane")
	}
	find := func(id string) int {
		for i, l := range s.card.Lines {
			if l.ID == id {
				return i
			}
		}
		return -1
	}
	for _, z := range []struct{ id, label string }{{"subscription", "Subscription URL"}, {"via", "Via URL"}} {
		idx := find(z.id)
		if idx < 0 {
			t.Fatalf("card has no %s line", z.id)
		}
		line := s.card.Lines[idx]
		if line.Kind != tui.CardField {
			t.Fatalf("%s line is not a CardField: %v", z.id, line)
		}
		for s.card.Selected < idx {
			s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		s.mode = ""
		s.dialog = nil
		s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if s.mode != "edit-prompt" || s.prompt == nil {
			t.Fatalf("Enter on %s field line: mode=%q prompt=%v", z.id, s.mode, s.prompt)
		}
		if s.prompt.Label != z.label {
			t.Fatalf("Enter on %s opened prompt %q, want %q", z.id, s.prompt.Label, z.label)
		}
		s.dialog = nil
		s.prompt = nil
	}
}
