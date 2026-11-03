package main

import (
	"errors"
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
	d.wizard.Values = map[string]string{"Login": "alice", "Provider": "any", "Country": "any", "Exit": "direct", "Clients": "everyone", "Pre-hop": "", "Post-hop": secret}
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
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true, PostSOCKS5: "socks5://u:fake-token@host:1080"}); e != nil {
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
	runAccountCmd(s, s.mutateEdit(a, "post", map[string]string{"value": "none"}))
	r, _ = AccountList(h)
	if r[0].PostSOCKS5.Value != "none" {
		t.Fatal("hop clear")
	}
	runAccountCmd(s, s.bg("clone", func() tea.Msg {
		_, e := AccountClone(h, AccountCloneOptions{Source: "alice", Login: "bob"})
		return s.finishMutation(e, "cloned", "bob")
	}))
	r, e = AccountList(h)
	if e != nil || len(r) != 2 || r[1].Login != "bob" || r[1].PostSOCKS5.Value != "none" || r[1].AllowedClients != "loopback" {
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
		"Login": "bob", "Provider": "any", "Country": "any", "Exit": "direct",
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
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "bob" || cfg.Accounts[0].AllowedClients != "*" {
		t.Fatalf("account config did not persist wildcard access: %#v", cfg.Accounts)
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
	s.rows = []accountInfo{{Login: "alice", PostSOCKS5: accountChainInfo{Source: "account", Value: "socks5://u:fake-token@host"}}}
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
