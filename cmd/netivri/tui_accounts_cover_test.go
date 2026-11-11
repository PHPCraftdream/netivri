package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func submitAccountHopOption(t *testing.T, s *accountsScreen, value string) tea.Cmd {
	t.Helper()
	if s == nil || s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ChoicePrompt {
		t.Fatal("account hop option prompt is not open")
	}
	p := s.dialog.prompt
	maxOptions := len(p.Options)
	if maxOptions == 0 {
		t.Fatal("account hop option prompt has no options")
	}
	selected := false
	for i := 0; i < maxOptions; i++ {
		got, err := p.GetAnswer()
		if err != nil {
			t.Fatal("could not read selected account hop option")
		}
		if got == value {
			selected = true
			break
		}
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if cmd != nil {
			t.Fatal("moving through account hop options produced a command")
		}
	}
	if !selected {
		t.Fatal("requested account hop option was not found")
	}

	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting account hop option produced no result command")
	}
	msg := cmd()
	result, ok := msg.(accountsDialogResult)
	if !ok || result.Canceled || len(result.Values) != 1 || result.Values["value"] != value {
		t.Fatal("submitted account hop option returned an unexpected dialog result")
	}
	_, mutation := s.Update(result)
	return mutation
}

// The account screen never renders an empty key-hint strip, in any dialog state.
func TestAccountsKeyHintsNeverEmptyInAnyDialogState(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	if strings.TrimSpace(s.KeyHints()) == "" {
		t.Fatalf("KeyHints empty without a dialog: %q", s.KeyHints())
	}
	s.dialog = &accountsDialog{prompt: prompt(tui.TextPrompt, "Clone account", "New login", "", "")}
	if strings.TrimSpace(s.KeyHints()) == "" {
		t.Fatalf("KeyHints empty with a prompt dialog open: %q", s.KeyHints())
	}
	s.dialog = &accountsDialog{wizard: tui.NewWizard("Add account", []tui.WizardStep{{Key: "Login"}})}
	if strings.TrimSpace(s.KeyHints()) == "" {
		t.Fatalf("KeyHints empty with a wizard dialog open: %q", s.KeyHints())
	}
}

// Bindings follow the focused pane: card focus exposes card hotkeys, list focus
// exposes list bindings; both must be non-empty.
func TestAccountsBindingsFollowFocusedPane(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", Provider: "main", Exit: "direct"}}
	s.syncRows("alice")
	keys := func(hs tui.ActionHints) map[string]bool {
		out := map[string]bool{}
		for _, b := range hs {
			for _, k := range b.Keys() {
				out[k] = true
			}
		}
		return out
	}
	s.workspace.SetFocus(tui.PaneList)
	listHints := s.Bindings()
	if len(listHints) == 0 {
		t.Fatal("list-focus bindings were empty")
	}
	if !keys(listHints)["r"] {
		t.Fatalf("list-focus bindings missing the reload key: %v", keys(listHints))
	}
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	cardHints := s.Bindings()
	if len(cardHints) == 0 {
		t.Fatal("card-focus bindings were empty")
	}
	// The screen appends its own action bindings (edit e, clone c) after the
	// card navigation bindings.
	got := keys(cardHints)
	if !got["e"] || !got["c"] {
		t.Fatalf("card-focus bindings missing the screen action keys e and c: %v", got)
	}
}

// reload() drains into accountsLoaded and fills s.rows; loadChoices() drains
// into accountsChoicesLoaded and fills s.providers, against a real home.
func TestAccountsReloadAndLoadChoicesFillRowsAndProviders(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	runAccountCmd(s, s.reload())
	if s.loading || len(s.rows) != 1 || s.rows[0].Login != "alice" {
		t.Fatalf("reload did not fill rows: loading=%t rows=%#v", s.loading, s.rows)
	}
	runAccountCmd(s, s.loadChoices())
	if len(s.providers) != 1 || s.providers[0] != "main" {
		t.Fatalf("loadChoices did not fill providers: %#v", s.providers)
	}
	if _, ok := s.providerNodes["main"]; !ok {
		t.Fatalf("loadChoices did not fill provider nodes: %#v", s.providerNodes)
	}
}

// Dialog result plumbing: no dialog is a no-op, cancel and a missing callback
// both close the dialog without touching the config.
func TestAccountsDialogResultNoopCancelAndMissingCallback(t *testing.T) {
	s := newAccountsScreen("", tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	// No dialog open: the result is ignored.
	got, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "x"}})
	if cmd != nil || got != s {
		t.Fatalf("result without a dialog was not a no-op: cmd=%v screen=%v", cmd, got != s)
	}
	// Canceled result closes the dialog.
	s.dialog = &accountsDialog{prompt: prompt(tui.TextPrompt, "Clone account", "New login", "", ""),
		onResult: func(map[string]string) tea.Cmd { return nil }}
	_, _ = s.Update(accountsDialogResult{Canceled: true, Values: map[string]string{"value": "x"}})
	if s.dialog != nil {
		t.Fatal("canceled dialog result did not close the dialog")
	}
	// Result with no onResult callback also just closes.
	s.dialog = &accountsDialog{prompt: prompt(tui.TextPrompt, "Clone account", "New login", "", "")}
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "bob"}})
	if s.dialog != nil {
		t.Fatal("result without a callback did not close the dialog")
	}
}

// Stale background generations must be ignored; a fresh choices message fills
// providers and per-provider country codes.
func TestAccountsStaleGenerationsIgnoredAndChoicesApplied(t *testing.T) {
	s := newAccountsScreen("", tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	// Stale load: a generation from a previous reload is dropped.
	s.loading = true
	s.loadGeneration = 7
	_, _ = s.Update(accountsLoaded{Rows: []accountInfo{{Login: "ghost"}}, Generation: 6})
	if !s.loading || len(s.rows) != 0 {
		t.Fatalf("stale accountsLoaded applied: loading=%t rows=%#v", s.loading, s.rows)
	}
	// Stale poll: a generation from a previous tick is dropped.
	s.polling = true
	s.pollGeneration = 3
	_, _ = s.Update(accountsPollResult{Generation: 2})
	if !s.polling {
		t.Fatal("stale accountsPollResult applied: polling=false")
	}
	// Fresh choices message fills providers and country codes.
	_, _ = s.Update(accountsChoicesLoaded{
		Providers: []string{"main"},
		Nodes:     map[string]string{"main": "12"},
		Countries: ProviderCountriesAllResult{Providers: []ProviderCountriesResult{{Name: "main", Countries: []subscription.CountryCount{{Code: "DE", Nodes: 3}}}}},
	})
	if len(s.providers) != 1 || s.providers[0] != "main" || s.providerNodes["main"] != "12" {
		t.Fatalf("choices not applied: providers=%#v nodes=%#v", s.providers, s.providerNodes)
	}
	if got := s.providerCountries["main"]; len(got) != 1 || got[0] != "DE" {
		t.Fatalf("country codes not applied: %#v", s.providerCountries)
	}
}

// Key handling on a real config: clone via 'c' + wizard-free prompt submit,
// esc closes the dialog, 'd' opens the confirm modal, 'a' respects the
// provider prerequisite, Tab cycles panes, Esc returns to the list, 'r' reloads.
func TestAccountsUpdateKeyHotkeysOnRealConfig(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	s.workspace.SetFocus(tui.PaneList)
	type press = tea.KeyPressMsg

	// 'c' opens the clone prompt.
	_, _ = s.Update(press{Code: 'c', Text: "c"})
	d := s.dialog
	if d == nil || d.prompt == nil {
		t.Fatalf("'c' did not open the clone dialog: %+v", d)
	}
	if !strings.Contains(d.prompt.TitleText+d.prompt.Label, "New login") && !strings.Contains(d.prompt.TitleText, "Clone") {
		t.Fatalf("clone dialog is not the clone prompt: title=%q label=%q", d.prompt.TitleText, d.prompt.Label)
	}
	// Esc with an untouched value closes it via a canceled result.
	_, escCmd := s.Update(press{Code: tea.KeyEsc})
	if escCmd == nil {
		t.Fatal("esc in an untouched clone dialog produced no result command")
	}
	msg := escCmd()
	res, ok := msg.(accountsDialogResult)
	if !ok || !res.Canceled {
		t.Fatalf("esc produced %#v", msg)
	}
	if _, c := s.Update(msg); c != nil || s.dialog != nil {
		t.Fatalf("canceled result did not close the dialog: dialog=%v cmd=%v", s.dialog != nil, c)
	}
	// Submit a valid clone login through the dialog result.
	_, _ = s.Update(press{Code: 'c', Text: "c"})
	if s.dialog == nil {
		t.Fatal("second 'c' did not reopen the clone dialog")
	}
	_, cloneCmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "bob"}})
	if cloneCmd == nil {
		t.Fatal("clone submit produced no command")
	}
	runAccountCmd(s, cloneCmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range cfg.Accounts {
		if a.Login == "bob" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clone did not persist account bob: %#v", cfg.Accounts)
	}
	if s.dialog != nil {
		t.Fatal("successful clone left the dialog open")
	}

	// 'd' captures a Confirm modal and records the pending login.
	var captured tui.Modal
	s2 := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(m tui.Modal) tea.Cmd {
		captured = m
		return nil
	}}).(*accountsScreen)
	s2.rows, _ = AccountList(h)
	s2.syncRows("alice")
	s2.workspace.SetFocus(tui.PaneList)
	_, _ = s2.Update(press{Code: 'd', Text: "d"})
	if captured == nil || s2.pendingLogin != "alice" {
		t.Fatalf("'d' did not open the confirm modal: captured=%v pendingLogin=%q", captured != nil, s2.pendingLogin)
	}

	// 'a' with no providers switches to the Providers tab.
	switched := -1
	s3 := newAccountsScreen(h, tui.Env{SwitchTab: func(i int) tea.Cmd {
		switched = i
		return func() tea.Msg { return tui.ThemeMsg{Theme: tui.NewTheme()} }
	}}).(*accountsScreen)
	_, addCmd := s3.Update(press{Code: 'a', Text: "a"})
	if switched != 0 || addCmd == nil {
		t.Fatalf("'a' without providers did not switch tabs: switched=%d cmd=%v", switched, addCmd != nil)
	}
	// Without a SwitchTab hook the screen explains itself instead.
	s4 := newAccountsScreen(h, tui.Env{}).(*accountsScreen)
	_, _ = s4.Update(press{Code: 'a', Text: "a"})
	if s4.dialog != nil || !strings.Contains(s4.message, "Provider") {
		t.Fatalf("'a' without providers or hook: dialog=%v message=%q", s4.dialog != nil, s4.message)
	}
	// 'a' with providers opens the add wizard.
	s5 := newAccountsScreen(h, tui.Env{}).(*accountsScreen)
	s5.providers = []string{"main"}
	_, _ = s5.Update(press{Code: 'a', Text: "a"})
	if s5.dialog == nil || s5.dialog.wizard == nil {
		t.Fatalf("'a' with providers did not open the add wizard: %+v", s5.dialog)
	}
	s5.dialog = nil

	// Tab cycles workspace focus at width>=100; Esc returns to the list.
	s5.workspace.SetFocus(tui.PaneList)
	_, _ = s5.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, _ = s5.Update(press{Code: tea.KeyTab})
	if s5.workspace.Focus() != tui.PaneCard {
		t.Fatalf("tab did not move focus to the card: focus=%d", s5.workspace.Focus())
	}
	_, _ = s5.Update(press{Code: tea.KeyEsc})
	if s5.workspace.Focus() != tui.PaneList || s5.workspace.Drilled {
		t.Fatalf("esc did not return to the list: focus=%d drilled=%t", s5.workspace.Focus(), s5.workspace.Drilled)
	}

	// 'r' reloads: the returned batch drains into fresh rows and choices.
	s6 := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s6.rows = nil
	s6.workspace.SetFocus(tui.PaneList)
	_, rCmd := s6.Update(press{Code: 'r', Text: "r"})
	if rCmd == nil {
		t.Fatal("'r' did not produce a reload command")
	}
	runAccountCmd(s6, rCmd)
	if s6.loading || len(s6.rows) != 2 {
		t.Fatalf("'r' reload did not fill rows: loading=%t rows=%#v", s6.loading, s6.rows)
	}
	if len(s6.providers) != 1 || s6.providers[0] != "main" {
		t.Fatalf("'r' reload did not refresh provider choices: %#v", s6.providers)
	}
}

// Card actions: enter on the clone/remove/clear-post lines drives the same
// flows as their hotkeys.
func TestAccountsActivateCardActionsOnRealConfig(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	hop := "socks5://u:pw@h:1"
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Provider: "main", Country: "DE", Exit: "route", ExitGiven: true, PostSOCKS5: config.HopList{hop}}); e != nil {
		t.Fatal(e)
	}
	enter := func(s *accountsScreen) tea.Cmd {
		_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return c
	}
	selectLine := func(s *accountsScreen, id string) bool {
		for i, l := range s.workspace.Card.Lines {
			if l.ID == id {
				s.workspace.Card.Selected = i
				return true
			}
		}
		return false
	}
	focusCard := func(s *accountsScreen) {
		s.rows, _ = AccountList(h)
		s.syncRows("alice")
		_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		s.workspace.SetFocus(tui.PaneCard)
		s.refreshSelectedCard(true)
	}

	// Clone line opens the clone prompt.
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	focusCard(s)
	if !selectLine(s, "clone") {
		t.Fatalf("card has no clone line: %#v", s.workspace.Card.Lines)
	}
	if c := enter(s); c != nil {
		t.Fatalf("clone line activation produced a command: %v", c)
	}
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatalf("clone line did not open the clone dialog: %+v", s.dialog)
	}
	s.dialog = nil

	// Remove line captures the confirm modal.
	var captured tui.Modal
	s2 := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(m tui.Modal) tea.Cmd {
		captured = m
		return nil
	}}).(*accountsScreen)
	focusCard(s2)
	if !selectLine(s2, "remove") {
		t.Fatalf("card has no remove line: %#v", s2.workspace.Card.Lines)
	}
	if c := enter(s2); c != nil {
		t.Fatalf("remove line activation produced a command: %v", c)
	}
	if captured == nil || s2.pendingLogin != "alice" {
		t.Fatalf("remove line did not open the confirm modal: captured=%v pendingLogin=%q", captured != nil, s2.pendingLogin)
	}

	// Clear-post line clears the stored hop through AccountUpdate.
	s3 := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	focusCard(s3)
	if !selectLine(s3, "clear-post") {
		t.Fatalf("card has no clear-post line: %#v", s3.workspace.Card.Lines)
	}
	if s3.workspace.Card.Lines[s3.workspace.Card.Selected].Disabled {
		t.Fatalf("clear-post line is disabled even though a post-hop is set: %#v", s3.workspace.Card.Lines[s3.workspace.Card.Selected])
	}
	if c := enter(s3); c == nil {
		t.Fatal("clear-post activation produced no command")
	} else {
		runAccountCmd(s3, c)
	}
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].Login != "alice" || len(cfg.Accounts[0].PostSOCKS5) != 0 {
		t.Fatalf("clear-post did not clear the stored hop: %#v", cfg.Accounts[0])
	}
	// Inherit-pre line clears the stored override through AccountUpdate.
	s4 := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	focusCard(s4)
	if !selectLine(s4, "clear-pre") || s4.workspace.Card.Lines[s4.workspace.Card.Selected].Disabled {
		t.Fatal("inherit-pre line is missing or disabled")
	}
	if c := enter(s4); c == nil {
		t.Fatal("inherit-pre activation produced no command")
	} else {
		runAccountCmd(s4, c)
	}
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatal("inherit-pre left the account override set")
	}
}

// Wizard add path on a real home: valid values persist with "everyone" mapped
// to "*"; an invalid login fails validation and keeps the wizard editable.
func TestAccountsWizardResultPersistsAndValidationRetains(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.providers = []string{"main"}
	s.beginAdd()
	d := s.dialog
	if d == nil || d.wizard == nil {
		t.Fatalf("beginAdd did not open a wizard: %+v", d)
	}
	d.wizard.Values["Login"] = "bad login"
	// Validation failure keeps the wizard open without saving.
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"Login": "bad login", "Country": "any", "Clients": "everyone"}})
	if s.dialog != d || d.wizard.Saving || d.wizard.Error == nil || d.wizard.Values["Login"] != "bad login" {
		t.Fatalf("invalid login did not retain the wizard with an error: dialog=%v saving=%t err=%v values=%#v",
			s.dialog != nil, d.wizard.Saving, d.wizard.Error, d.wizard.Values)
	}
	// Valid values create the account and close the dialog.
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{
		"Login": "bob", "Provider": accountProviderAnySentinel, "Country": "any", "Exit": "direct",
		"Clients": "everyone", "CustomClients": "", "Pre-hop": "", "Post-hop": "",
	}})
	if cmd == nil {
		t.Fatal("valid wizard result produced no command")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "bob" || cfg.Accounts[0].Provider != "" || cfg.Accounts[0].ExitCountry != "" || cfg.Accounts[0].AllowedClients != "*" {
		t.Fatalf("wizard add did not persist wildcard clients and any-provider selection: %#v", cfg.Accounts)
	}
	if s.dialog != nil {
		t.Fatal("successful wizard add left the dialog open")
	}
}

// openEdit prompts: label plumbing and persisted edits through the dialog
// result path, plus the save-time country validation error path.
func TestAccountsOpenEditPromptsPersistAndValidate(t *testing.T) {
	h := accountProviderHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Provider: "p1", Country: "DE", Exit: "route", ExitGiven: true, PostSOCKS5: config.HopList{"http://post-user:post-secret@post.example:8080"}}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	a := s.rows[0]

	// clients edit: prompt labelled "Allowed clients", "loopback" persists.
	if c := s.openEdit(a, "clients"); c != nil {
		t.Fatalf("openEdit clients produced a command: %v", c)
	}
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Label != "Allowed clients" {
		t.Fatalf("clients editor prompt wrong: %+v", s.dialog)
	}
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "loopback"}})
	if cmd == nil {
		t.Fatal("clients submit produced no command")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if s.dialog != nil || cfg.Accounts[0].AllowedClients != "loopback" {
		t.Fatalf("clients edit not persisted: dialog=%v accounts=%#v", s.dialog != nil, cfg.Accounts)
	}

	// post editor is a safe operation picker, not a prefilled URL prompt.
	s.rows, _ = AccountList(h)
	a = s.rows[0]
	_ = s.openEdit(a, "post")
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ChoicePrompt {
		t.Fatalf("post-hop picker did not open: %+v", s.dialog)
	}
	if !strings.Contains(s.dialog.View(100, 20), "http://post.example:8080") || strings.Contains(s.dialog.View(100, 20), "post-user") || strings.Contains(s.dialog.View(100, 20), "post-secret") {
		t.Fatal("picker did not show a safely redacted endpoint")
	}
	cmd = submitAccountHopOption(t, s, "none")
	if cmd == nil {
		t.Fatal("None action did not start the account update")
	}
	runAccountCmd(s, cmd)
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts[0].PostSOCKS5) != 1 || cfg.Accounts[0].PostSOCKS5[0] != "none" {
		t.Fatalf("none action not persisted: %#v", cfg.Accounts[0])
	}

	// countries edit: "NLD" is not a valid code, the save fails, the prompt
	// stays open with an error and the config is untouched.
	s.rows, _ = AccountList(h)
	a = s.rows[0]
	_ = s.openEdit(a, "countries")
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatalf("countries editor did not open: %+v", s.dialog)
	}
	_, cmd = s.Update(accountsDialogResult{Values: map[string]string{"value": "NLD"}})
	if cmd == nil {
		t.Fatal("countries submit produced no command")
	}
	runAccountCmd(s, cmd)
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Error == nil {
		t.Fatalf("invalid country did not retain the prompt with an error: dialog=%v err=%v message=%q",
			s.dialog != nil, s.dialog != nil && s.dialog.prompt != nil && s.dialog.prompt.Error != nil, s.message)
	}
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].ExitCountry != "DE" {
		t.Fatalf("invalid country was persisted: %#v", cfg.Accounts[0])
	}
}
