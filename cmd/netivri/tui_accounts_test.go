package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
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
func countryKey(f *tui.Form) string {
	for _, x := range f.Fields {
		if strings.HasPrefix(x.Name, "Country") {
			return x.Name
		}
	}
	return "Country"
}
func accountsSubmit(s *accountsScreen, v map[string]string) tea.Cmd {
	f := s.form.Form
	for i := range f.Fields {
		x := &f.Fields[i]
		if x.Kind == tui.MultiChoiceField {
			if z, ok := v[x.Name]; ok {
				x.Selected = strings.Split(z, ",")
			}
		} else if z, ok := v[x.Name]; ok {
			x.Value = z
		}
	}
	f.Focus = len(f.Fields) - 1
	_, c := f.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if c == nil {
		return nil
	}
	m, ok := c().(tui.FormResultMsg)
	if !ok {
		return nil
	}
	_, c = s.Update(m)
	return c
}
func accountsRunBackground(s *accountsScreen, c tea.Cmd) {
	if c == nil {
		return
	}
	m := c()
	if bg, ok := m.(tui.BackgroundMsg); ok {
		s.Update(bg)
		return
	}
	if batch, ok := m.(tea.BatchMsg); ok {
		for _, task := range batch {
			if task != nil {
				if bg, ok := task().(tui.BackgroundMsg); ok {
					s.Update(bg)
				}
			}
		}
	}
}
func TestAccountsDynamicProviderCountryHintModal(t *testing.T) {
	s := newAccountsScreen(accountsHome(t), tui.Env{}).(*accountsScreen)
	s.providers = []string{"backup", "main"}
	s.providerCountries["backup"] = []string{"DE", "NL"}
	s.providerCountries["main"] = []string{"US"}
	var modal tui.Modal
	s.env.OpenModal = func(m tui.Modal) tea.Cmd { modal = m; return nil }
	s.openForm("add", nil)
	f := s.form.Form
	p, c := -1, -1
	for i := range f.Fields {
		if f.Fields[i].Kind == tui.MultiChoiceField {
			p = i
		}
		if strings.HasPrefix(f.Fields[i].Name, "Country") {
			c = i
		}
	}
	if p < 0 || c < 0 {
		t.Fatal("fields missing")
	}
	f.Focus = p
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "any" {
		t.Fatalf("after Space: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "any" || f.Fields[p].Value != "backup" {
		t.Fatalf("after Down: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "backup" || f.Fields[p].Value != "backup" {
		t.Fatalf("after provider Space: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "backup" || f.Fields[p].Value != "any" {
		t.Fatalf("after Up: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "any" || f.Fields[p].Value != "any" {
		t.Fatalf("after any Space: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "any" || f.Fields[p].Value != "backup" {
		t.Fatalf("after Down from any: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if got := f.Fields[p].Selected; len(got) != 1 || got[0] != "backup" || f.Fields[p].Value != "backup" {
		t.Fatalf("after Space replacing any: selected=%v highlighted=%q", got, f.Fields[p].Value)
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(f.Fields[p].Selected) != 0 || f.Fields[p].Value != "backup" || !strings.Contains(f.Fields[c].Name, "DE,NL,US") {
		t.Fatalf("after deselecting last provider: selected=%v highlighted=%q hint=%q", f.Fields[p].Selected, f.Fields[p].Value, f.Fields[c].Name)
	}
	f.Focus = 4
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(f.Fields[p].Selected) != 0 || f.Fields[4].Value != " " {
		t.Fatal("text space")
	}
}
func TestAccountsCloneRequiresTypedLoginAndPersists(t *testing.T) {
	h := accountsHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	var modal tui.Modal
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(m tui.Modal) tea.Cmd { modal = m; return nil }}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.openForm("clone", &s.rows[0])
	f := s.form.Form
	if f.Fields[0].Name != "New login" {
		t.Fatal("clone login missing")
	}
	for f.Focus < len(f.Fields)-1 {
		modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	_, submit := modal.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submit == nil {
		t.Fatal("blank submit")
	}
	r := submit().(tui.FormResultMsg)
	_, work := s.Update(r)
	accountsRunBackground(s, work)
	if s.form == nil || s.form.Form != f || !strings.Contains(f.View(100, 18), "invalid account login") {
		t.Fatal("inline error not retained")
	}
	if f.Focus != 0 {
		t.Fatal("login not refocused")
	}
	modal, _ = modal.Update(tea.KeyPressMsg{Text: "b"})
	modal, _ = modal.Update(tea.KeyPressMsg{Text: "o"})
	modal, _ = modal.Update(tea.KeyPressMsg{Text: "b"})
	for f.Focus < len(f.Fields)-1 {
		modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	_, submit = modal.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	r = submit().(tui.FormResultMsg)
	_, work = s.Update(r)
	accountsRunBackground(s, work)
	rows, e := AccountList(h)
	if e != nil || len(rows) != 2 || rows[1].Login != "bob" {
		t.Fatalf("clone rows %#v %v", rows, e)
	}
}
func TestAccountsReloadGenerationAndSelection(t *testing.T) {
	h := accountsHome(t)
	for _, x := range []string{"alice", "bob"} {
		if _, e := AccountAdd(h, AccountAddOptions{Login: x, Exit: "direct", ExitGiven: true}); e != nil {
			t.Fatal(e)
		}
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	a, b := s.reload(), s.reload()
	old := a().(tui.BackgroundMsg).Value.(accountsLoaded)
	cur := b().(tui.BackgroundMsg).Value.(accountsLoaded)
	if old.Generation == cur.Generation {
		t.Fatal("generations")
	}
	s.Update(cur)
	s.table.Selected = 1
	s.Update(accountsLoaded{Rows: []accountInfo{s.rows[1], s.rows[0]}, Generation: s.loadGeneration})
	if row, ok := s.table.SelectedRow(); !ok || row.Data.(accountInfo).Login != "bob" {
		t.Fatal("selection lost")
	}
	before := s.rows[0].Login
	s.Update(old)
	if s.rows[0].Login != before {
		t.Fatal("stale reload")
	}
}
func TestAccountsInitialReloadUsesTempHomeWithoutStatusProbe(t *testing.T) {
	h := accountsHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.status = func(string) (angel.Status, error) { return angel.Status{}, errors.New("unexpected probe") }
	accountsRunBackground(s, s.reload())
	if len(s.rows) != 1 || s.rows[0].Login != "alice" || !strings.Contains(s.View(140, 10), "alice") || len(s.live) != 0 {
		t.Fatal("reload render")
	}
}
func TestAccountsMutationTUIReadbackAllOperations(t *testing.T) {
	h := accountsHome(t)
	captured := 0
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(tui.Modal) tea.Cmd { captured++; return nil }}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.providers = []string{"backup", "main"}
	s.openForm("add", nil)
	ck := countryKey(s.form.Form)
	v := map[string]string{"Login": "alice", "Provider": "any", "Exit": "direct", "Allowed clients": "loopback", "Pre-hop URL": "", "Post-hop URL": "socks5://u:stored-secret@host:1080"}
	v[ck] = ""
	accountsRunBackground(s, accountsSubmit(s, v))
	rows, e := AccountList(h)
	if e != nil || len(rows) != 1 || rows[0].PasswordSet {
		errorText := ""
		if s.form != nil && s.form.Form != nil {
			errorText = s.form.Form.View(100, 18)
		}
		t.Fatalf("add: inline error %q; rows %d; readback error %v", errorText, len(rows), e)
	}
	if !strings.Contains(s.View(140, 10), "alice") || s.makeRows()[0].Cells[6] != "waiting for first use" {
		t.Fatal("render")
	}
	raw, e := config.Load(config.ConfigPath(h))
	if e != nil || raw.Accounts[0].PostSOCKS5 != v["Post-hop URL"] {
		t.Fatal("persist hop")
	}
	s.rows = rows
	s.table.SetRows(s.makeRows())
	if strings.Contains(s.View(140, 10), "stored-secret") {
		t.Fatal("secret")
	}
	s.openForm("edit", &rows[0])
	ck = countryKey(s.form.Form)
	cfg, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	cfg.Accounts[0].AllowedClients = "198.51.100.0/24"
	if e = config.Save(config.ConfigPath(h), cfg); e != nil {
		t.Fatal(e)
	}
	edit := map[string]string{"Provider": "any", "Exit": "direct", "Allowed clients": "192.0.2.0/24", "Pre-hop empty unchanged; none clears": "", "Post-hop empty unchanged; none clears": ""}
	edit[ck] = ""
	accountsRunBackground(s, accountsSubmit(s, edit))
	raw, e = config.Load(config.ConfigPath(h))
	if e != nil || raw.Accounts[0].PostSOCKS5 != v["Post-hop URL"] || raw.Accounts[0].AllowedClients != "192.0.2.0/24" {
		t.Fatal("blank hop")
	}
	rows, _ = AccountList(h)
	s.rows = rows
	s.table.SetRows(s.makeRows())
	s.openForm("edit", &rows[0])
	ck = countryKey(s.form.Form)
	cfg, e = config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	cfg.Accounts[0].AllowedClients = "198.51.100.0/24"
	if e = config.Save(config.ConfigPath(h), cfg); e != nil {
		t.Fatal(e)
	}
	edit[ck] = ""
	edit["Allowed clients"] = rows[0].AllowedClients
	edit["Post-hop empty unchanged; none clears"] = "none"
	accountsRunBackground(s, accountsSubmit(s, edit))
	raw, e = config.Load(config.ConfigPath(h))
	if e != nil || raw.Accounts[0].PostSOCKS5 != "none" || raw.Accounts[0].AllowedClients != "198.51.100.0/24" {
		t.Fatal("edit")
	}
	rows, _ = AccountList(h)
	s.rows = rows
	s.table.SetRows(s.makeRows())
	s.openForm("clone", &rows[0])
	ck = countryKey(s.form.Form)
	clone := map[string]string{"New login": "bob", "Provider override": "any", "Allowed clients override": "", "Exit override": "(unchanged)"}
	clone[ck] = ""
	accountsRunBackground(s, accountsSubmit(s, clone))
	rows, e = AccountList(h)
	if e != nil || len(rows) != 2 || rows[1].Login != "bob" {
		t.Fatal("clone")
	}
	raw, e = config.Load(config.ConfigPath(h))
	if e != nil || raw.Accounts[1].PostSOCKS5 != "none" {
		t.Fatal("clone config")
	}
	s.rows = rows
	s.table.SetRows(s.makeRows())
	s.table.Selected = 1
	s.Update(tui.ConfirmResultMsg{})
	_, rm := s.Update(tui.ConfirmResultMsg{Confirmed: true})
	accountsRunBackground(s, rm)
	rows, e = AccountList(h)
	if e != nil || len(rows) != 1 || rows[0].Login != "alice" {
		t.Fatal("remove")
	}
	if captured < 3 {
		t.Fatal("modal")
	}
}
func TestAccountsDuplicateFailureRetainsWholeForm(t *testing.T) {
	h := accountsHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	captured := 0
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(tui.Modal) tea.Cmd { captured++; return nil }}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.openForm("add", nil)
	f := s.form.Form
	ck := countryKey(f)
	v := map[string]string{"Login": "alice", "Provider": "any", "Exit": "direct", "Allowed clients": "", "Pre-hop URL": "", "Post-hop URL": "socks5://u:proxy-password@host:1080"}
	v[ck] = ""
	accountsRunBackground(s, accountsSubmit(s, v))
	if s.form == nil || s.form.Form != f || len(f.Fields) != 8 || f.Fields[0].Value != "alice" || f.Fields[6].Value == "" || captured != 2 {
		t.Fatal("form retention")
	}
	view := f.View(100, 18)
	if strings.Contains(view, "proxy-password") || !strings.Contains(view, "already exists") {
		t.Fatal("mask/error")
	}
}
func TestAccountsRealFormValidationAndCountryHint(t *testing.T) {
	s := newAccountsScreen(accountsHome(t), tui.Env{OpenModal: func(tui.Modal) tea.Cmd { return nil }}).(*accountsScreen)
	s.providers = []string{"backup", "main"}
	s.providerCountries["backup"] = []string{"DE", "NL"}
	s.providerCountries["main"] = []string{"US"}
	s.openForm("add", nil)
	f := s.form.Form
	ck := countryKey(f)
	for _, tc := range []struct{ login, provider, country, want string }{{"bad login", "backup,main", "DE", "invalid account login"}, {"bob", "backup,main", "NLD", "invalid country code"}, {"bob", "missing", "DE", "unknown provider"}} {
		v := map[string]string{"Login": tc.login, "Provider": tc.provider, "Exit": "direct", "Allowed clients": ""}
		v[ck] = tc.country
		for i := range f.Fields {
			x := &f.Fields[i]
			if x.Kind == tui.MultiChoiceField {
				if y, ok := v[x.Name]; ok {
					x.Selected = strings.Split(y, ",")
				} else {
					x.Selected = nil
				}
			} else if y, ok := v[x.Name]; ok {
				x.Value = y
			}
		}
		f.Focus = len(f.Fields) - 1
		_, cmd := f.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil || !strings.Contains(f.View(100, 18), tc.want) || f.Fields[0].Value != tc.login {
			t.Fatalf("validation %s", tc.want)
		}
	}
	if accountsProviderValue(map[string]string{"Provider": "backup,main"}, "Provider") != "backup,main" {
		t.Fatal("provider")
	}
}
func TestAccountsPollingRatesAndErrorReset(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	calls := 0
	s := newAccountsScreen("", tui.Env{Now: func() time.Time { return now }, Background: accountsSyncBackground}).(*accountsScreen)
	s.status = func(string) (angel.Status, error) {
		calls++
		return angel.Status{Activity: []angel.AccountActivity{{Login: "alice", UploadBytes: int64(calls * 1024), DownloadBytes: int64(calls * 2048)}}}, nil
	}
	_, cmd := s.Update(accountsRateTick{})
	_, overlap := s.Update(accountsRateTick{})
	if cmd == nil || overlap != nil {
		t.Fatal("poll overlap")
	}
	accountsRunBackground(s, cmd)
	now = now.Add(time.Second)
	_, cmd = s.Update(accountsRateTick{})
	accountsRunBackground(s, cmd)
	if s.rates["alice"] != [2]string{"1.0 KiB/s", "2.0 KiB/s"} {
		t.Fatal("rate")
	}
	s.applyStatus(angel.Status{}, errors.New("offline"), now)
	if len(s.live) != 0 || len(s.previous) != 0 {
		t.Fatal("stale")
	}
}
func TestAccountsFrameRoutesBackgroundPollAcrossModal(t *testing.T) {
	h := accountsHome(t)
	var screen *accountsScreen
	scheduled, calls := 0, 0
	frame := tui.NewFrame(tui.Options{Home: h, Tabs: []tui.Tab{{Title: "Accounts", New: func(env tui.Env) tui.Screen {
		screen = newAccountsScreen(h, env).(*accountsScreen)
		screen.status = func(string) (angel.Status, error) { calls++; return angel.Status{}, nil }
		screen.pollWait = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			scheduled++
			return func() tea.Msg { return fn(time.Time{}) }
		}
		return screen
	}}}})
	frame.Update(screen.env.OpenModal((&tui.Form{TitleText: "Accounts test modal"}).AsModal())())
	if !strings.Contains(frame.View().Content, "Accounts test modal") {
		t.Fatal("modal")
	}
	execute := func(c tea.Cmd) tea.Cmd {
		_, work := frame.Update(c())
		batch := work().(tea.BatchMsg)
		if len(batch) != 2 || batch[0] == nil {
			t.Fatal("background/spinner tasks")
		}
		_, next := frame.Update(batch[0]())
		if next == nil {
			t.Fatal("result not routed")
		}
		return next
	}
	next := execute(screen.rateTick())
	next = execute(next)
	// The status result schedules the next timer; the first status call ran.
	if scheduled != 2 || calls != 1 {
		t.Fatalf("scheduled %d calls %d", scheduled, calls)
	}
	next = execute(next) // the second timer fires and asks for a status
	_ = execute(next)    // the second status runs and schedules a third timer
	if scheduled != 3 || calls != 2 {
		t.Fatalf("polling stopped behind the modal: scheduled %d calls %d", scheduled, calls)
	}
}
func TestAccountsSafeDisplayAndDetailResize(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", PasswordSet: true, PreSOCKS5: accountChainInfo{Value: "http://u:secret@host", Source: "account"}}}
	s.table.SetRows(s.makeRows())
	v := s.View(120, 10)
	if strings.Contains(v, "secret") || strings.Contains(v, "http://") || !strings.Contains(v, "set") {
		t.Fatal("secret")
	}
	s.detail = true
	s.detailResult = &AccountShowResult{Login: "alice", PreSOCKS5: accountShowChainInfo{Value: "socks5://secret@host", Source: "account"}, Tiers: []accountShowTier{{Tier: 1, Country: "DE", Providers: []string{"p"}, Fallback: true, Nodes: []accountShowNode{{Name: "Node"}}}}}
	wide := s.View(120, 30)
	if !strings.Contains(wide, "fallback") || !strings.Contains(wide, "Node") {
		t.Fatal("detail")
	}
	v = s.View(14, 5)
	if strings.Contains(v, "secret") || len(strings.Split(v, "\n")) > 5 {
		t.Fatal("bounds")
	}
	for _, line := range strings.Split(v, "\n") {
		if len([]rune(line)) > 14 {
			t.Fatal("width")
		}
	}
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.detail {
		t.Fatal("escape")
	}
}
func TestAccountsErrorSanitization(t *testing.T) {
	if strings.Contains(accountsErrorText(errors.New("token-secret")), "token-secret") {
		t.Fatal("raw error")
	}
	if !strings.Contains(accountsErrorText(&cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid", Hint: "hint"}), "hint") {
		t.Fatal("hint")
	}
}
