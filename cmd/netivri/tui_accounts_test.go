package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func accountsHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	if e := config.Save(config.ConfigPath(h), config.Default()); e != nil {
		t.Fatal(e)
	}
	return h
}
func accountsSyncBackground(label string, work func() tea.Msg) tea.Cmd {
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
}
func runAccountCmd(s *accountsScreen, c tea.Cmd) {
	if c == nil {
		return
	}
	m := c()
	if b, ok := m.(tui.BackgroundMsg); ok {
		s.Update(b)
		return
	}
	if batch, ok := m.(tea.BatchMsg); ok {
		for _, x := range batch {
			if x != nil {
				if b, ok := x().(tui.BackgroundMsg); ok {
					s.Update(b)
				}
			}
		}
	}
}
func TestAccountsPollKeepsLiveCellsCardAndSelection(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", Provider: "main", ExitCountry: "DE"}}
	s.syncRows("alice")
	card := s.workspace.Card
	s.workspace.List.Selected = 0
	s.workspace.SetFocus(tui.PaneCard)
	s.live["alice"] = angel.AccountActivity{Login: "alice", Connections: 7}
	s.rates["alice"] = [2]string{"1", "2"}
	s.updateLiveRows()
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	before := s.workspace.List.Selected
	view := s.View(120, 30)
	list := s.workspace.List.View(120*35/100, 30)
	if !strings.Contains(view, "Accounts") {
		t.Fatal("workspace was not rendered")
	}
	if s.workspace.Card != card || s.workspace.List.Selected != before || s.workspace.Focus() != tui.PaneCard {
		t.Fatal("live update changed selection, card identity, or focus")
	}
	if len(s.workspace.List.Columns) != 6 {
		t.Fatalf("columns=%d", len(s.workspace.List.Columns))
	}
	for i, want := range []string{"7", "1", "2"} {
		if s.workspace.List.Rows[0].Cells[i+3] != want {
			t.Fatalf("cell %d=%q", i+3, s.workspace.List.Rows[0].Cells[i+3])
		}
	}
	for _, v := range []string{"7", "1", "2"} {
		if !strings.Contains(list, v) {
			t.Fatalf("list pane missing %q: %q", v, list)
		}
	}
}

func TestAccountsInitialLoadSchedulesSelectedDetail(t *testing.T) {
	s := newAccountsScreen("", tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.show = func(_, login string) (AccountShowResult, error) { return AccountShowResult{Login: login}, nil }
	_, cmd := s.Update(accountsLoaded{Rows: []accountInfo{{Login: "alice"}}, Generation: s.loadGeneration})
	if cmd == nil {
		t.Fatal("initial list load did not return detail command")
	}
	runAccountCmd(s, cmd)
	if s.cardResult == nil || s.cardResult.Login != "alice" {
		t.Fatal("initial selected detail result was not delivered")
	}
}

func TestAccountsLiveCellsVisibleAtWorkspaceWidths(t *testing.T) {
	for _, width := range []int{100, 120} {
		s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
		s.rows = []accountInfo{{Login: "a", Provider: "p", Exit: "d"}}
		s.syncRows("a")
		s.live["a"] = angel.AccountActivity{Login: "a", Connections: 7}
		s.rates["a"] = [2]string{"1", "2"}
		s.updateLiveRows()
		_, _ = s.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		got := s.workspace.List.Columns
		if len(got) != 6 {
			t.Fatalf("width %d: columns=%d", width, len(got))
		}
		for i, want := range []string{"7", "1", "2"} {
			if s.workspace.List.Rows[0].Cells[i+3] != want {
				t.Fatalf("width %d cell %d=%q", width, i+3, s.workspace.List.Rows[0].Cells[i+3])
			}
		}
		list := s.workspace.List.View(width*35/100, 30)
		lines := strings.Split(ansi.Strip(list), "\n")
		if len(lines) <= 3 {
			t.Fatalf("width %d: missing data row: %q", width, list)
		}
		row := lines[3]
		starts := map[int][]int{100: {21, 25, 30}, 120: {26, 32, 37}}[width]
		for i, want := range []string{"7", "1", "2"} {
			start := starts[i]
			if start >= ansi.StringWidth(row) || ansi.Cut(row, start, start+1) != want {
				t.Fatalf("width %d: live cell %q not at column start %d: %q", width, want, start, row)
			}
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width*35/100 {
				t.Fatalf("width %d: rendered line exceeds pane: %q", width, line)
			}
		}
	}
}

func TestAccountsDuplicateAddRetainsWizardAndRedactsSecret(t *testing.T) {
	h := accountsHome(t)
	if _, err := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); err != nil {
		t.Fatal(err)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.providers = []string{"main"}
	s.beginAdd()
	d := s.dialog
	secret := "socks5://u:unique-secret@host:1080"
	d.wizard.Values = map[string]string{"Login": "alice", "Provider": accountProviderAnySentinel, "Country": "any", "Exit": "direct", "Clients": "everyone", "Pre-hop": "", "Post-hop": secret}
	d.wizard.Steps[7].Prompt.Value = secret
	_, cmd := s.Update(accountsDialogResult{Values: d.wizard.Values})
	if cmd == nil || !d.wizard.Saving {
		t.Fatal("add operation did not enter saving state")
	}
	runAccountCmd(s, cmd)
	if s.dialog != d || d.wizard.Saving || d.wizard.Values["Login"] != "alice" || d.wizard.Values["Post-hop"] != secret {
		t.Fatal("failed duplicate add did not retain editable answers")
	}
	if strings.Contains(s.View(120, 30), "unique-secret") || strings.Contains(s.message, "unique-secret") {
		t.Fatal("failed add exposed entered secret")
	}
}

func TestAccountsDialogValidationKeepsAnswersAndCancelsSaving(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.providers = []string{"main"}
	s.beginAdd()
	d := s.dialog
	d.wizard.Values["Login"] = "bad login"
	d.wizard.Index = len(d.wizard.Steps) - 1
	values := map[string]string{"Login": "bad login", "Country": "any", "Clients": "everyone"}
	_, _ = s.Update(accountsDialogResult{Values: values})
	if s.dialog != d || d.wizard.Saving || d.wizard.Values["Login"] != "bad login" {
		t.Fatal("validation closed/saved wizard or discarded its answer")
	}
}
func TestAccountsWorkspaceBoundsAndAddLine(t *testing.T) {
	s := newAccountsScreen(accountsHome(t), tui.Env{}).(*accountsScreen)
	for _, d := range [][2]int{{60, 16}, {80, 24}, {99, 24}, {100, 30}, {120, 40}, {12, 5}} {
		v := s.View(d[0], d[1])
		if len(strings.Split(v, "\n")) > d[1] {
			t.Fatalf("height at %v", d)
		}
		if strings.Contains(v, "password-secret") {
			t.Fatal("secret")
		}
	}
	if !strings.Contains(s.View(80, 24), "Add account") {
		t.Fatal("add action absent")
	}
}
func TestAccountsOperationsReadback(t *testing.T) {
	h := accountsHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true, PostSOCKS5: config.HopList{"socks5://u:fake-token@host:1080"}}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	a := s.rows[0]
	runAccountCmd(s, s.mutateEdit(a, "clients", map[string]string{"value": "loopback"}))
	r, e := AccountList(h)
	if e != nil || r[0].AllowedClients != "loopback" {
		t.Fatalf("edit %#v %v", r, e)
	}
	_ = s.openEdit(a, "post")
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "none"}})
	runAccountCmd(s, cmd)
	r, _ = AccountList(h)
	if !reflect.DeepEqual(r[0].PostSOCKS5.Value, []string{"none"}) {
		t.Fatal("hop clear")
	}
	runAccountCmd(s, s.bg("clone", func() tea.Msg {
		_, e := AccountClone(h, AccountCloneOptions{Source: "alice", Login: "bob"})
		return s.finishMutation(e, "cloned", "bob")
	}))
	r, e = AccountList(h)
	if e != nil || len(r) != 2 || r[1].Login != "bob" || !reflect.DeepEqual(r[1].PostSOCKS5.Value, []string{"none"}) || r[1].AllowedClients != "loopback" {
		t.Fatalf("clone %#v %v", r, e)
	}
	runAccountCmd(s, s.mutateRemove("bob"))
	r, e = AccountList(h)
	if e != nil || len(r) != 1 || r[0].Login != "alice" {
		t.Fatal("remove")
	}
}
func TestAccountsAddWizardCallbackPersistsEveryoneWildcard(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.providers = []string{"main"}
	s.beginAdd()
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{
		"Login": "bob", "Provider": accountProviderAnySentinel, "Country": "any", "Exit": "direct",
		"Clients": "everyone", "Pre-hop": "", "Post-hop": "",
	}})
	if cmd == nil {
		t.Fatal("wizard callback did not start account creation")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "bob" || cfg.Accounts[0].Provider != "" || cfg.Accounts[0].ExitCountry != "" || cfg.Accounts[0].AllowedClients != "*" {
		t.Fatalf("account config did not persist wildcard access and any-provider selection: %#v", cfg.Accounts)
	}
	if s.dialog != nil || s.selectedLogin() != "bob" {
		t.Fatal("successful wizard did not close and select the new account")
	}
}

func TestAccountsMouseCardActivationAndDialogCapture(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.width, s.height = 120, 30
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	s.workspace.SetSize(s.width, s.height)
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	calls := 0
	s.dialog = &accountsDialog{prompt: prompt(tui.TextPrompt, "Edit", "Value", "", ""), onResult: func(map[string]string) tea.Cmd { calls++; return nil }}
	// Input belongs to the active dialog; it must not activate the workspace card.
	_, _ = s.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	if calls != 0 || s.dialog == nil {
		t.Fatal("dialog click escaped to workspace")
	}
}

func TestAccountsUpdatePreservesExternalUneditedField(t *testing.T) {
	h := accountsHome(t)
	_, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true})
	if e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	c.Accounts[0].AllowedClients = "loopback"
	if e = config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	runAccountCmd(s, s.mutateEdit(s.rows[0], "exit", map[string]string{"value": "route"}))
	c, e = config.Load(config.ConfigPath(h))
	if e != nil || c.Accounts[0].AllowedClients != "loopback" || c.Accounts[0].Exit != "route" {
		t.Fatal("unrelated external edit overwritten")
	}
}
func TestAccountsSecretDisplayAndErrorRedaction(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", PostSOCKS5: accountChainInfo{Source: "account", Value: []string{"socks5://u:fake-token@host"}}}}
	s.syncRows("alice")
	v := s.View(120, 30)
	if strings.Contains(v, "fake-token") || strings.Contains(v, "socks5://") {
		t.Fatal("hop secret displayed")
	}
	e := accountsSafePromptError(errors.New("invalid socks5://u:fake-token@host"), "socks5://u:fake-token@host")
	if strings.Contains(e, "fake-token") {
		t.Fatal("hop secret in error")
	}
	if strings.Contains(accountsErrorText(errors.New("raw-private-error")), "raw-private-error") {
		t.Fatal("untrusted error exposed")
	}
}
func TestAccountsPollingFixedClockSerialAndStale(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	calls := 0
	s := newAccountsScreen("", tui.Env{Now: func() time.Time { return now }, Background: accountsSyncBackground}).(*accountsScreen)
	s.status = func(string) (angel.Status, error) {
		calls++
		return angel.Status{Activity: []angel.AccountActivity{{Login: "alice", Connections: 2, UploadBytes: int64(calls * 1024), DownloadBytes: int64(calls * 2048)}}}, nil
	}
	_, c := s.Update(accountsRateTick{})
	_, over := s.Update(accountsRateTick{})
	if c == nil || over != nil {
		t.Fatal("overlapping poll")
	}
	runAccountCmd(s, c)
	now = now.Add(time.Second)
	_, c = s.Update(accountsRateTick{})
	runAccountCmd(s, c)
	if s.rates["alice"] != [2]string{"1.0 KiB/s", "2.0 KiB/s"} {
		t.Fatalf("rates %v", s.rates)
	}
	s.polling = true
	s.Update(accountsPollResult{Generation: s.pollGeneration - 1, At: now})
	if !s.polling {
		t.Fatal("stale generation applied")
	}
}
func TestAccountsWizardBackReviewSecret(t *testing.T) {
	s := newAccountsScreen(accountsHome(t), tui.Env{}).(*accountsScreen)
	s.providers = []string{"main"}
	s.providerNodes["main"] = "12"
	s.beginAdd()
	w := s.dialog.wizard
	w.View(100, 30)
	w.Steps[0].Prompt.Value = "alice"
	w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if w.Index != 1 {
		t.Fatal("did not advance")
	}
	w.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if w.Index != 0 || w.Steps[0].Prompt.Value != "alice" {
		t.Fatal("back discarded answer")
	}
	w.Values["Login"] = "alice"
	w.Values["Pre-hop"] = "socks5://u:fake-token@host"
	w.Review = true
	if strings.Contains(w.View(100, 30), "fake-token") {
		t.Fatal("wizard review revealed secret")
	}
}
func TestAccountsShowSeamNoLiveProbe(t *testing.T) {
	s := newAccountsScreen("", tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	calls := 0
	s.show = func(_, login string) (AccountShowResult, error) {
		calls++
		return AccountShowResult{Login: login, Tiers: []accountShowTier{{Tier: 1, Fallback: true, Nodes: []accountShowNode{{Name: "node"}}}}}, nil
	}
	c := s.loadDetail("alice")
	runAccountCmd(s, c)
	if calls != 1 || s.cardResult == nil || !strings.Contains(accountsTiersText(s.cardResult.Tiers), "fallback") {
		t.Fatal("show seam")
	}
}
func TestAccountsProviderPrerequisiteSwitch(t *testing.T) {
	called := -1
	s := newAccountsScreen("", tui.Env{SwitchTab: func(i int) tea.Cmd { called = i; return nil }}).(*accountsScreen)
	s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if called != 0 {
		t.Fatal("did not switch to Providers")
	}
}

func TestAccountsListNavigationRefreshesCardAndDetail(t *testing.T) {
	s := newAccountsScreen("", tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.show = func(_, login string) (AccountShowResult, error) { return AccountShowResult{Login: login}, nil }
	s.rows = []accountInfo{{Login: "alice"}, {Login: "bob"}}
	s.syncRows("alice")
	s.workspace.SetSize(120, 30)
	s.workspace.SetFocus(tui.PaneList)
	if s.workspace.Card.Title != "Account alice" {
		t.Fatalf("initial card = %q", s.workspace.Card.Title)
	}
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.workspace.List.Selected != 1 {
		t.Fatalf("KeyDown did not change selection: %d", s.workspace.List.Selected)
	}
	if cmd == nil {
		t.Fatal("selection change did not schedule a detail load")
	}
	if s.workspace.Card.Title != "Account bob" {
		t.Fatalf("card did not follow selection: %q", s.workspace.Card.Title)
	}
	runAccountCmd(s, cmd)
	if s.cardResult == nil || s.cardResult.Login != "bob" {
		t.Fatalf("detail for new selection missing: %+v", s.cardResult)
	}
}

func TestAccountsCardThemeFollowsScreenTheme(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.theme = tui.NewThemeFor(false)
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	s.refreshSelectedCard(true)
	if s.workspace.Card.Theme.Dark {
		t.Fatal("account card kept a dark theme on a light screen")
	}
	s.workspace.Card = tui.NewCard("Account", nil)
	s.workspace.Card.SetTheme(tui.NewThemeFor(true))
	_, _ = s.Update(tui.ThemeMsg{Theme: tui.NewThemeFor(false)})
	if s.workspace.Card.Theme.Dark {
		t.Fatal("ThemeMsg did not retheme the account card")
	}
}

func TestAccountsDefaultWizardRealKeysCreatesWildcard(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "main", URL: "https://provider.invalid"})
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.providers = []string{"main"}
	s.beginAdd()
	d := s.dialog
	if d == nil || d.wizard == nil {
		t.Fatal("beginAdd did not open the wizard")
	}
	typeKey := func(r rune) {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	enter := func() tea.Cmd {
		_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return c
	}
	for _, r := range "bob" {
		typeKey(r)
	}
	if d.wizard.Steps[0].Prompt.Value != "bob" {
		t.Fatalf("login input = %q", d.wizard.Steps[0].Prompt.Value)
	}
	// Seven Enters walk Login -> Provider -> Country -> Exit -> Clients ->
	// CustomClients -> Pre-hop -> Post-hop -> Review; every step keeps its default.
	for i := 0; i < 7; i++ {
		if c := enter(); c != nil {
			t.Fatalf("step %d advance produced a command", i)
		}
		if d.wizard.Index != i+1 {
			t.Fatalf("after enter %d wizard index = %d", i, d.wizard.Index)
		}
	}
	// The eighth Enter accepts the last step and opens the review.
	if c := enter(); c != nil {
		t.Fatal("last step advance produced a command")
	}
	if !d.wizard.Review {
		t.Fatal("last step enter did not reach review")
	}
	// Review Enter activates the focused [Create] button; accountsDialog.Update
	// converts the WizardResultMsg from that command into accountsDialogResult,
	// which the screen then turns into the account-creation command.
	cmd := enter()
	if cmd == nil {
		t.Fatal("review create did not return a result command")
	}
	msg := cmd()
	result, ok := msg.(accountsDialogResult)
	if !ok || result.Canceled {
		t.Fatalf("wizard result command produced %#v", msg)
	}
	_, cmd = s.Update(msg)
	if cmd == nil {
		t.Fatal("dialog result did not start account creation")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "bob" || cfg.Accounts[0].AllowedClients != "*" {
		t.Fatalf("account config did not persist wildcard access: %#v", cfg.Accounts)
	}
	if s.dialog != nil || s.selectedLogin() != "bob" {
		t.Fatal("successful wizard did not close and select the new account")
	}
}

func TestAccountsMouseRowCardFieldAndDialogSave(t *testing.T) {
	h := accountsHome(t)
	cfg0 := config.Default()
	cfg0.Subscriptions = []config.Subscription{{Name: "main", URL: "https://x.invalid/a"}}
	if e := config.Save(config.ConfigPath(h), cfg0); e != nil {
		t.Fatal(e)
	}
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := AccountAdd(h, AccountAddOptions{Login: "bob", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.rows, _ = AccountList(h)
	s.syncRows("bob")
	// (a) Click the first list row. Workspace mouse translation leaves the left
	// pane unoffset: row y = border(1) + title(1) + header(1), so alice is at y=3.
	_, _ = s.Update(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	if s.selectedLogin() != "alice" || s.workspace.Card.Title != "Account alice" {
		t.Fatalf("row click selected %q card %q", s.selectedLogin(), s.workspace.Card.Title)
	}
	// (b) Focus the card and click the Allowed clients field line at its label X
	// (y=6: border(1) + title(1) + info(1) + 3 fields; screen x=45 -> card-local x=2,
	// left of the [edit] range, so the first click only selects).
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	s.workspace.SetSize(120, 30)
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.workspace.Card.Selected != 1 {
		t.Fatalf("card selection = %d", s.workspace.Card.Selected)
	}
	_, _ = s.Update(tea.MouseClickMsg{X: 45, Y: 6, Button: tea.MouseLeft})
	if s.dialog != nil || s.workspace.Card.Selected != 3 {
		t.Fatalf("first card click opened a dialog or ignored the selection: %v %d", s.dialog != nil, s.workspace.Card.Selected)
	}
	// Second click on the same line activates it and opens the clients editor.
	_, _ = s.Update(tea.MouseClickMsg{X: 45, Y: 6, Button: tea.MouseLeft})
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatalf("card activation did not open the Allowed clients prompt: %+v", s.dialog)
	}
	// (c) Type a CIDR, then click [ Save ] on the button row (buttonY = h-1
	// after rendering the dialog at height 26; segment [ Save ] spans x 0..9).
	provider := s.dialog.prompt
	for range provider.Value {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "10.0.0.0/8" {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if provider.Value != "10.0.0.0/8" {
		t.Fatalf("prompt value = %q", provider.Value)
	}
	_ = s.View(120, 26) // render so the prompt lays out its buttons at y=25
	_, cmd := s.Update(tea.MouseClickMsg{X: 4, Y: 25, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Save click did not produce a result command")
	}
	if msg := cmd(); msg != nil {
		if _, ok := msg.(accountsDialogResult); !ok {
			t.Fatalf("save command produced %#v", msg)
		}
		_, cmd = s.Update(msg)
		runAccountCmd(s, cmd)
	}
	if s.dialog != nil {
		t.Fatal("save did not close the dialog")
	}
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].Login != "alice" || cfg.Accounts[0].AllowedClients != "10.0.0.0/8" {
		t.Fatalf("save did not persist the clients edit: %#v", cfg.Accounts[0])
	}
}
