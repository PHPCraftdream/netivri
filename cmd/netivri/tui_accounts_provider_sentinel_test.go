package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func TestAccountsCountryFieldHintLiteralAnyIsNotWildcard(t *testing.T) {
	s := &accountsScreen{
		providers:         []string{"any", "backup"},
		providerCountries: map[string][]string{"any": {"DE"}, "backup": {"NL"}},
	}

	if got := s.countryFieldHint([]string{"any"}); got != "Any or country codes: DE" {
		t.Fatalf("literal any provider hint = %q, want DE only", got)
	}
	for name, providers := range map[string][]string{"nil": nil, "blank": {""}} {
		if got := s.countryFieldHint(providers); got != "Any or country codes: DE, NL" {
			t.Errorf("%s wildcard hint = %q, want DE and NL", name, got)
		}
	}
}

func TestAccountsProviderPickerPersistsLiteralAnyProvider(t *testing.T) {
	h := providerSeed(t,
		config.Subscription{Name: "any", URL: "https://any.invalid"},
		config.Subscription{Name: "backup", URL: "https://backup.invalid"},
	)
	store := subscription.NewStore(h + "/subscriptions")
	for _, provider := range []string{"any", "backup"} {
		if err := store.Save(provider, subscription.Snapshot{Nodes: []subscription.Node{{ID: provider, Country: "DE"}}}); err != nil {
			t.Fatal(err)
		}
	}

	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.providers = []string{"any", "backup"}
	s.beginAdd()
	wizard := s.dialog.wizard
	providerPrompt := wizard.Steps[1].Prompt
	if len(providerPrompt.Options) != 3 || providerPrompt.Options[0].Value != accountProviderAnySentinel || providerPrompt.Options[1].Value != "any" || providerPrompt.Options[2].Value != "backup" {
		t.Fatalf("provider options conflated any-provider and literal provider: %#v", providerPrompt.Options)
	}
	typeKey := func(r rune) {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	for _, r := range "alice" {
		typeKey(r)
	}
	enter := func() tea.Cmd {
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return cmd
	}
	if cmd := enter(); cmd != nil || wizard.Index != 1 {
		t.Fatalf("login did not advance to provider: index=%d cmd=%v", wizard.Index, cmd)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd := enter(); cmd != nil || wizard.Index != 2 || wizard.Values["Provider"] != "any" {
		t.Fatalf("literal any provider was not selected and saved: index=%d values=%#v cmd=%v", wizard.Index, wizard.Values, cmd)
	}
	for range "any" {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "DE" {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if cmd := enter(); cmd != nil || wizard.Index != 3 || wizard.Values["Country"] != "DE" {
		t.Fatalf("country DE was not retained: index=%d values=%#v cmd=%v", wizard.Index, wizard.Values, cmd)
	}
	for i := 3; i < len(wizard.Steps); i++ {
		if cmd := enter(); cmd != nil || wizard.Index != i+1 && !wizard.Review {
			t.Fatalf("wizard step %d did not advance: index=%d review=%t cmd=%v", i, wizard.Index, wizard.Review, cmd)
		}
	}
	resultCmd := enter()
	if resultCmd == nil {
		t.Fatal("wizard review did not produce a result")
	}
	result := resultCmd()
	if _, ok := result.(accountsDialogResult); !ok {
		t.Fatalf("wizard result command produced %#v", result)
	}
	_, callbackCmd := s.Update(result)
	if callbackCmd == nil {
		t.Fatal("wizard callback did not start account creation")
	}
	runAccountCmd(s, callbackCmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "alice" || cfg.Accounts[0].Provider != "any" || cfg.Accounts[0].ExitCountry != "DE" {
		t.Fatalf("literal provider selection was not persisted: %#v", cfg.Accounts)
	}
}

func TestAccountsProviderTextEditPreservesLiteralAny(t *testing.T) {
	h := providerSeed(t,
		config.Subscription{Name: "any", URL: "https://any.invalid"},
		config.Subscription{Name: "backup", URL: "https://backup.invalid"},
	)
	store := subscription.NewStore(h + "/subscriptions")
	for _, provider := range []string{"any", "backup"} {
		if err := store.Save(provider, subscription.Snapshot{Nodes: []subscription.Node{{ID: provider, Country: "DE"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AccountAdd(h, AccountAddOptions{Login: "alice", Country: "DE", Exit: "route", ExitGiven: true}); err != nil {
		t.Fatal(err)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	a := s.rows[0]
	s.openEdit(a, "provider")
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.TextPrompt {
		t.Fatal("provider editor is not a text prompt")
	}
	if s.dialog.prompt.Value != "" {
		t.Fatalf("initial provider text = %q, want empty", s.dialog.prompt.Value)
	}

	for _, r := range "any" {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if s.dialog.prompt.Value != "any" {
		t.Fatalf("typed provider text = %q, want literal any", s.dialog.prompt.Value)
	}
	_, resultCmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if resultCmd == nil {
		t.Fatal("provider prompt Enter did not return its value")
	}
	result := resultCmd()
	if _, ok := result.(accountsDialogResult); !ok {
		t.Fatalf("provider prompt result command produced %#v", result)
	}
	_, cmd := s.Update(result)
	if cmd == nil {
		t.Fatal("provider text edit did not start update")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Provider != "any" || cfg.Accounts[0].ExitCountry != "DE" {
		t.Fatalf("literal provider text or country was not persisted: %#v", cfg.Accounts)
	}
}
