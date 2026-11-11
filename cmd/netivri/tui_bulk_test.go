package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func bulkTestScreen(t *testing.T, h string) *accountsScreen {
	t.Helper()
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.syncRows("")
	s.workspace.SetFocus(tui.PaneList)
	return s
}

func accountsEqual(a, b []config.Account) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestAccountsBulkMarkingKeys drives Space, m and M with real keys, checks
// the "*" gutter on the rows and the bulk card line's disabled state.
func TestAccountsBulkMarkingKeys(t *testing.T) {
	h := accountsHome(t)
	for _, login := range []string{"alice", "bob"} {
		if _, e := AccountAdd(h, AccountAddOptions{Login: login, Exit: "direct", ExitGiven: true}); e != nil {
			t.Fatal(e)
		}
	}
	s := bulkTestScreen(t, h)
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	if _, c := s.Update(space); c != nil {
		t.Fatal("space produced a command")
	}
	if !s.marked["alice"] || !s.workspace.List.Rows[0].Mark {
		t.Fatal("space did not mark the selected account")
	}
	if _, _ = s.Update(space); s.marked["alice"] {
		t.Fatal("second space did not unmark")
	}
	if _, _ = s.Update(space); !s.marked["alice"] {
		t.Fatal("re-mark failed")
	}
	if _, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown}); s.workspace.List.Selected != 1 {
		t.Fatalf("selection after down = %d", s.workspace.List.Selected)
	}
	if _, _ = s.Update(tea.KeyPressMsg{Code: 'm', Text: "m"}); !s.marked["bob"] || !s.workspace.List.Rows[1].Mark {
		t.Fatal("m did not mark bob")
	}
	if _, _ = s.Update(tea.KeyPressMsg{Code: 'M', Text: "M"}); len(s.marked) != 0 {
		t.Fatal("M did not clear the marks")
	}
	if s.workspace.List.Rows[0].Mark || s.workspace.List.Rows[1].Mark {
		t.Fatal("clear left a mark gutter")
	}
	// Space on the + Add line changes nothing.
	s.workspace.List.Selected = len(s.workspace.List.Rows)
	if _, _ = s.Update(space); len(s.marked) != 0 {
		t.Fatal("space on the add line marked something")
	}
	// The bulk card line is disabled exactly while nothing is marked.
	bulkLine := func() (tui.CardLine, bool) {
		for _, l := range s.workspace.Card.Lines {
			if l.ID == "bulk" {
				return l, true
			}
		}
		return tui.CardLine{}, false
	}
	line, ok := bulkLine()
	if !ok || !line.Disabled {
		t.Fatalf("bulk line not disabled without marks: %#v (found=%t)", line, ok)
	}
	s.workspace.List.Selected = 0
	if _, _ = s.Update(space); !s.marked["alice"] {
		t.Fatal("re-mark for the card line check failed")
	}
	line, ok = bulkLine()
	if !ok || line.Disabled {
		t.Fatal("bulk line stayed disabled with a mark")
	}
}

// TestAccountsBulkMarksSurviveReloads keeps marks keyed by login over a
// reload and drops the marks of accounts that disappear.
func TestAccountsBulkMarksSurviveReloads(t *testing.T) {
	h := accountsHome(t)
	for _, login := range []string{"alice", "bob"} {
		if _, e := AccountAdd(h, AccountAddOptions{Login: login, Exit: "direct", ExitGiven: true}); e != nil {
			t.Fatal(e)
		}
	}
	s := bulkTestScreen(t, h)
	s.marked["alice"] = true
	s.marked["bob"] = true
	rows, _ := AccountList(h)
	_, _ = s.Update(accountsLoaded{Rows: rows, Generation: s.loadGeneration})
	if !s.marked["alice"] || !s.marked["bob"] {
		t.Fatal("reload dropped marks of existing accounts")
	}
	if !s.workspace.List.Rows[0].Mark {
		t.Fatal("reload lost the mark gutter")
	}
	rows, _ = AccountList(h)
	rows = rows[:1] // bob disappears
	_, _ = s.Update(accountsLoaded{Rows: rows, Generation: s.loadGeneration})
	if !s.marked["alice"] {
		t.Fatal("reload dropped alice's mark")
	}
	if s.marked["bob"] {
		t.Fatal("the mark of a vanished account survived")
	}
}

// TestAccountsBulkApplyEachField applies every bulk field through the shared
// operations on the switchHome fixture and checks the stored config.
func TestAccountsBulkApplyEachField(t *testing.T) {
	h := switchHome(t)
	s := bulkTestScreen(t, h)
	s.marked["alice"] = true
	s.marked["bob"] = true
	value := func(login, field string) string {
		t.Helper()
		c, err := loadConfig(h)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := c.Account(login)
		if a == nil {
			t.Fatalf("account %s vanished", login)
		}
		switch field {
		case "provider":
			return a.Provider
		case "country":
			return a.ExitCountry
		case "clients":
			return a.AllowedClients
		case "exit":
			return a.Exit
		}
		return ""
	}
	hopList := func(login, field string) config.HopList {
		t.Helper()
		c, err := loadConfig(h)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := c.Account(login)
		if a == nil {
			t.Fatalf("account %s vanished", login)
		}
		switch field {
		case "pre":
			return a.PreSOCKS5
		case "post":
			return a.PostSOCKS5
		}
		return nil
	}
	apply := func(field, val string) {
		t.Helper()
		runAccountCmd(s, s.applyBulk(field, val))
	}

	apply("country", "DE")
	if value("alice", "country") != "DE" || value("bob", "country") != "DE" || s.message != "2 accounts updated" {
		t.Fatalf("country bulk: alice=%s bob=%s message=%q", value("alice", "country"), value("bob", "country"), s.message)
	}
	apply("provider", "p2")
	if value("alice", "provider") != "p2" || value("bob", "provider") != "p2" || value("carol", "provider") != "" {
		t.Fatal("provider bulk touched the wrong accounts")
	}
	apply("clients", "loopback")
	if value("alice", "clients") != "loopback" || value("bob", "clients") != "loopback" {
		t.Fatal("clients bulk")
	}
	apply("clients", "none")
	if value("alice", "clients") != "" {
		t.Fatal("clients none did not clear")
	}
	apply("clients", "everyone")
	if value("alice", "clients") != "*" {
		t.Fatal("clients everyone did not become the wildcard")
	}
	apply("post", "socks5://192.0.2.1:1080")
	if !slices.Equal(hopList("alice", "post"), config.HopList{"socks5://192.0.2.1:1080"}) {
		t.Fatal("post bulk")
	}
	apply("post", "none")
	if !slices.Equal(hopList("alice", "post"), config.HopList{"none"}) {
		t.Fatal("post none")
	}
	apply("post", "inherit")
	if len(hopList("alice", "post")) != 0 {
		t.Fatal("post inherit did not clear")
	}
	apply("pre", "socks5://192.0.2.1:1080")
	if !slices.Equal(hopList("alice", "pre"), config.HopList{"socks5://192.0.2.1:1080"}) {
		t.Fatal("pre bulk")
	}
	apply("pre", "inherit")
	if len(hopList("alice", "pre")) != 0 {
		t.Fatal("pre inherit did not clear")
	}
	apply("pre", "none")
	if !slices.Equal(hopList("alice", "pre"), config.HopList{"none"}) {
		t.Fatal("pre none")
	}
}

// TestAccountsBulkNothingPartialOnError refuses a bulk run as a whole when
// one target or the value is invalid, and reports message and hint only.
func TestAccountsBulkNothingPartialOnError(t *testing.T) {
	h := switchHome(t)
	s := bulkTestScreen(t, h)
	s.marked["alice"] = true
	s.marked["bob"] = true
	saved := func() []config.Account {
		c, err := loadConfig(h)
		if err != nil {
			t.Fatal(err)
		}
		return append([]config.Account(nil), c.Accounts...)
	}
	before := saved()
	runAccountCmd(s, s.applyBulk("exit", "direct"))
	if !strings.Contains(s.message, "direct requires empty provider") {
		t.Fatalf("exit direct message = %q", s.message)
	}
	after := saved()
	if !accountsEqual(before, after) {
		t.Fatal("a refused exit bulk changed the config")
	}
	_, _, err := accountBulkApply(h, "country", []string{"alice", "ghost"}, "DE")
	if err == nil || !strings.Contains(accountsErrorText(err), "account not found: ghost") {
		t.Fatalf("vanished target error = %v", err)
	}
	if !accountsEqual(after, saved()) {
		t.Fatal("a bulk with a vanished target changed the config")
	}
	runAccountCmd(s, s.applyBulk("country", "FI")) // p1 offers no FI
	if !strings.Contains(s.message, "nothing was changed") {
		t.Fatalf("country message = %q", s.message)
	}
	if !accountsEqual(after, saved()) {
		t.Fatal("a bulk with an invalid value changed the config")
	}
}

// TestAccountsBulkConfirmFlow walks the picker and the value prompt with the
// dialogs' real results, checks the confirmation names the account count, and
// that cancel changes nothing while confirm applies.
func TestAccountsBulkConfirmFlow(t *testing.T) {
	h := switchHome(t)
	var captured tui.Modal
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground, OpenModal: func(m tui.Modal) tea.Cmd {
		captured = m
		return nil
	}}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.syncRows("")
	s.workspace.SetFocus(tui.PaneList)
	s.marked["carol"] = true
	if _, _ = s.Update(tea.KeyPressMsg{Code: 'b', Text: "b"}); s.dialog == nil || s.dialog.prompt == nil {
		t.Fatal("b did not open the bulk field picker")
	}
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if c == nil {
		t.Fatal("field picker enter produced no result command")
	}
	msg := c()
	res, ok := msg.(accountsDialogResult)
	if !ok || res.Canceled || res.Values["value"] != "provider" {
		t.Fatalf("field picker produced %#v", msg)
	}
	if _, _ = s.Update(msg); s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Label != "Provider" {
		t.Fatalf("value prompt did not open: %+v", s.dialog)
	}
	_, c = s.Update(accountsDialogResult{Values: map[string]string{"value": "p1"}})
	if c != nil {
		t.Fatal("value submit produced a command")
	}
	confirm, ok := captured.(*tui.Confirm)
	if !ok {
		t.Fatalf("value submit did not open a confirm: %+v", captured)
	}
	if confirm.Prompt != "Set provider on 1 marked account?" || confirm.ConfirmLabel != "Apply" {
		t.Fatalf("confirm prompt = %q label = %q", confirm.Prompt, confirm.ConfirmLabel)
	}
	if s.dialog != nil {
		t.Fatal("value dialog stayed open behind the confirm")
	}
	_, _ = s.Update(tui.ConfirmResultMsg{Canceled: true})
	if s.pendingBulk != nil {
		t.Fatal("cancel kept the pending bulk")
	}
	if a := accountByLogin(t, h, "carol"); a.Provider != "" {
		t.Fatal("canceled bulk changed the config")
	}
	s.pendingBulk = &bulkRequest{field: "provider", value: "p1"}
	_, cmd := s.Update(tui.ConfirmResultMsg{Confirmed: true})
	if cmd == nil || s.pendingBulk != nil {
		t.Fatalf("confirmed bulk produced no command (cmd=%v pending=%v)", cmd != nil, s.pendingBulk != nil)
	}
	runAccountCmd(s, cmd)
	if a := accountByLogin(t, h, "carol"); a.Provider != "p1" {
		t.Fatalf("confirmed bulk did not apply: %#v", a)
	}
	if s.message != "1 account updated" {
		t.Fatalf("notice = %q", s.message)
	}
}

// TestAccountsBulkFrameContracts checks exact height and width at the four
// sizes for the marked list, the route viewer and the bulk dialog, and the
// drill behavior below 100 columns.
func TestAccountsBulkFrameContracts(t *testing.T) {
	h := switchHome(t)
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	s.marked["alice"] = true
	s.syncRows("alice")
	s.show = func(_, _ string) (AccountShowResult, error) { return AccountShowResult{Login: "alice"}, nil }
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		lines := strings.Split(s.View(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Fatalf("marked list %v: %d lines, want %d", size, len(lines), size[1])
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("marked list %v: line wider than %d", size, size[0])
			}
		}
	}
	runAccountCmd(s, s.openRoute())
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		lines := strings.Split(s.View(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Fatalf("route viewer %v: %d lines, want %d", size, len(lines), size[1])
		}
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, _ = s.Update(esc())
	s.marked["alice"] = true
	if _, _ = s.Update(tea.KeyPressMsg{Code: 'b', Text: "b"}); s.dialog == nil {
		t.Fatal("b did not open the bulk picker")
	}
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		lines := strings.Split(s.View(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Fatalf("bulk dialog %v: %d lines, want %d", size, len(lines), size[1])
		}
	}
	s.dialog = nil
	// Below 100 columns the card is only visible drilled in.
	_, _ = s.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if v := s.View(90, 24); strings.Contains(v, "Route...") || strings.Contains(v, "Apply to marked...") {
		t.Fatal("card actions visible without drilling")
	}
	if _, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); !s.workspace.Drilled {
		t.Fatal("enter did not drill in")
	}
	if v := s.View(90, 24); !strings.Contains(v, "Route...") || !strings.Contains(v, "Apply to marked...") {
		t.Fatal("drilled card lacks the route/bulk actions")
	}
}
