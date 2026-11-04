package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func filterKey(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text}
}

func assertNoQuitCmd(t *testing.T, c tea.Cmd) {
	t.Helper()
	if c == nil {
		return
	}
	m := c()
	if b, ok := m.(tea.BatchMsg); ok {
		for _, x := range b {
			if x == nil {
				continue
			}
			if _, quit := x().(tea.QuitMsg); quit {
				t.Fatal("frame returned a quit command while filtering")
			}
		}
		return
	}
	if _, quit := m.(tea.QuitMsg); quit {
		t.Fatal("frame returned a quit command while filtering")
	}
}

func TestProvidersFilterWalkKeepsInputInList(t *testing.T) {
	h := providerSeed(t,
		config.Subscription{Name: "alpha", URL: "https://alpha.invalid"},
		config.Subscription{Name: "quartz", URL: "https://quartz.invalid"},
		config.Subscription{Name: "arqnet", URL: "https://arq.invalid"},
		config.Subscription{Name: "rune", URL: "https://rune.invalid"},
	)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Start filtering and type a query containing a, r and q.
	s.Update(filterKey('/', "/"))
	providerType(s, "arq")
	if s.list.Filter != "arq" || !s.list.Filtering {
		t.Fatalf("filter state: %q filtering=%v", s.list.Filter, s.list.Filtering)
	}
	if row, ok := s.list.SelectedRow(); !ok || row.Cells[0] != "arqnet" {
		t.Fatalf("SelectedRow %+v, want arqnet", row)
	}
	// No wizard, no refresh busy, no remove confirm: nothing but the filter fired.
	if s.dialog != nil || s.mode != "" || s.confirm != nil || s.busy != "" {
		t.Fatalf("stray side effect: dialog=%v mode=%q confirm=%v busy=%q", s.dialog, s.mode, s.confirm, s.busy)
	}
	if !strings.Contains(ansi.Strip(s.View(120, 30)), "+ Add provider") {
		t.Fatal("add line missing from filtered view")
	}

	// Esc clears the filter and shows all rows again.
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.list.Filtering || s.list.Filter != "" {
		t.Fatalf("esc: filtering=%v filter=%q", s.list.Filtering, s.list.Filter)
	}
	for _, name := range []string{"alpha", "quartz", "arqnet", "rune"} {
		if !strings.Contains(ansi.Strip(s.list.View(120*35/100, 30)), name) {
			t.Fatalf("esc did not restore row %q", name)
		}
	}

	// '/' + query + Enter keeps the filter but leaves filter mode; the key is
	// consumed by the list, so no drill-in and no wizard.
	s.Update(filterKey('/', "/"))
	providerType(s, "arq")
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.list.Filtering || s.list.Filter != "arq" {
		t.Fatalf("enter: filtering=%v filter=%q", s.list.Filtering, s.list.Filter)
	}
	v := ansi.Strip(s.list.View(120*35/100, 30))
	if !strings.Contains(v, "arqnet") || strings.Contains(v, "quartz") || strings.Contains(v, "rune") {
		t.Fatal("enter kept filter but rows are not narrowed")
	}
	if s.dialog != nil || s.workspace.Drilled {
		t.Fatal("enter while filtering must not activate or drill")
	}
	// Navigation stays inside the subset (one match + the add line).
	s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.list.Selected != 1 {
		t.Fatalf("down after filter: selected %d, want 1", s.list.Selected)
	}
}

func TestAccountsFilterWalkKeepsInputInList(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", Provider: "p"}, {Login: "arnold", Provider: "p"}, {Login: "bob", Provider: "p"}}
	s.syncRows("alice")
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Screen-level walk: 'q' in the query must never reach the frame — it only
	// lands in the filter and opens no dialog.
	s.Update(filterKey('/', "/"))
	providerTypeAccounts(s, "arq")
	if s.workspace.List.Filter != "arq" || !s.workspace.List.Filtering {
		t.Fatalf("filter state: %q filtering=%v", s.workspace.List.Filter, s.workspace.List.Filtering)
	}
	if s.dialog != nil || s.message != "" {
		t.Fatalf("stray side effect: dialog=%v message=%q", s.dialog, s.message)
	}
	// Esc reaches the list (clear filter), not the workspace drill-back.
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.workspace.List.Filtering || s.workspace.List.Filter != "" {
		t.Fatalf("esc: filtering=%v filter=%q", s.workspace.List.Filtering, s.workspace.List.Filter)
	}
	// Check the list pane only: the card pane may show other content.
	lv := func() string { return ansi.Strip(s.workspace.List.View(120*35/100, 30)) }
	av := lv()
	for _, name := range []string{"alice", "arnold", "bob"} {
		if !strings.Contains(av, name) {
			t.Fatalf("esc did not restore account row %q", name)
		}
	}
	// Enter while filtering keeps the filter and does not drill or open.
	s.Update(filterKey('/', "/"))
	providerTypeAccounts(s, "ar")
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.workspace.List.Filtering || s.workspace.List.Filter != "ar" {
		t.Fatalf("enter: filtering=%v filter=%q", s.workspace.List.Filtering, s.workspace.List.Filter)
	}
	if kv := ansi.Strip(s.workspace.List.View(120*35/100, 30)); !strings.Contains(kv, "arnold") || strings.Contains(kv, "alice") || strings.Contains(kv, "bob") {
		t.Fatal("enter kept filter but account rows are not narrowed")
	}
	if s.workspace.Drilled || s.workspace.Focus() == tui.PaneCard {
		t.Fatal("enter while filtering drilled into the card")
	}
	if row, ok := s.workspace.List.SelectedRow(); !ok || row.Cells[0] != "arnold" {
		t.Fatalf("SelectedRow %+v, want arnold", row)
	}
	// Navigation moves within the subset only.
	s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.workspace.List.Selected != 1 || !s.workspace.List.IsAdd() {
		t.Fatalf("down after filter: selected %d IsAdd=%v, want add line of 2-row subset", s.workspace.List.Selected, s.workspace.List.IsAdd())
	}
}

func providerTypeAccounts(s *accountsScreen, text string) {
	for _, r := range text {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Frame-level: with the accounts screen at width >= 100, '/' then 'q' must be
// captured by the list filter (CapturingInput routes the key to the screen), so
// the frame must not quit.
func TestAccountsFrameSlashQDoesNotQuit(t *testing.T) {
	var screen *accountsScreen
	frame := tui.NewFrame(tui.Options{
		Tabs: []tui.Tab{{Title: "Accounts", New: func(env tui.Env) tui.Screen {
			screen = newAccountsScreen(env.Home, env).(*accountsScreen)
			return screen
		}}},
	})
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if screen == nil {
		t.Fatal("accounts screen not created")
	}
	_, c1 := frame.Update(filterKey('/', "/"))
	assertNoQuitCmd(t, c1)
	_, c2 := frame.Update(filterKey('q', "q"))
	assertNoQuitCmd(t, c2)
	if screen.workspace.List.Filter != "q" || !screen.workspace.List.Filtering {
		t.Fatalf("frame let 'q' escape the filter: %q filtering=%v", screen.workspace.List.Filter, screen.workspace.List.Filtering)
	}
	if screen.dialog != nil {
		t.Fatal("filtering opened a dialog")
	}
}

func TestProvidersListResultSelectHiddenRowClearsFilter(t *testing.T) {
	h := providerSeed(t,
		config.Subscription{Name: "alpha", URL: "https://alpha.invalid"},
		config.Subscription{Name: "beta", URL: "https://beta.invalid"},
		config.Subscription{Name: "gamma", URL: "https://gamma.invalid"},
	)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	pl := ProviderListResult{Providers: []ProviderInfo{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}}
	// Only beta matches the filter, so pendingSelect=gamma is hidden.
	s.list.Filter, s.list.Filtering = "beta", true
	s.list.StartFilter()
	s.pendingSelect = "gamma"
	_, _ = s.result(providerResultMsg{kind: "list", value: pl, err: nil})
	if s.list.Filter != "" || s.list.Filtering {
		t.Fatalf("hidden pendingSelect did not clear the filter: %q filtering=%v", s.list.Filter, s.list.Filtering)
	}
	if s.selected != "gamma" || s.pendingSelect != "" {
		t.Fatalf("selection %q pendingSelect %q, want gamma/empty", s.selected, s.pendingSelect)
	}
	if row, ok := s.list.SelectedRow(); !ok || row.Data != "gamma" {
		t.Fatalf("SelectedRow %+v, want gamma", row)
	}
}

func TestProvidersListResultSelectVisibleRowKeepsFilter(t *testing.T) {
	h := providerSeed(t,
		config.Subscription{Name: "alpha", URL: "https://alpha.invalid"},
		config.Subscription{Name: "beta", URL: "https://beta.invalid"},
		config.Subscription{Name: "gamma", URL: "https://gamma.invalid"},
	)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	pl := ProviderListResult{Providers: []ProviderInfo{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}}
	s.list.Filter, s.list.Filtering = "beta", true
	s.list.StartFilter()
	s.pendingSelect = "beta"
	_, _ = s.result(providerResultMsg{kind: "list", value: pl, err: nil})
	if s.list.Filter != "beta" || !s.list.Filtering {
		t.Fatalf("visible pendingSelect must keep the filter: %q filtering=%v", s.list.Filter, s.list.Filtering)
	}
	if s.selected != "beta" {
		t.Fatalf("selection %q, want beta", s.selected)
	}
	if s.list.Selected != 0 {
		t.Fatalf("selected %d, want filtered position 0", s.list.Selected)
	}
	if row, ok := s.list.SelectedRow(); !ok || row.Data != "beta" {
		t.Fatalf("SelectedRow %+v, want beta", row)
	}
}

func TestAccountsSyncRowsSelectUnderFilter(t *testing.T) {
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", Provider: "p"}, {Login: "arnold", Provider: "p"}, {Login: "bob", Provider: "p"}}
	s.syncRows("alice")
	// Only alice matches; arnold is hidden, so syncRows must clear the filter.
	s.workspace.List.Filter, s.workspace.List.Filtering = "alice", true
	s.workspace.List.StartFilter()
	s.syncRows("arnold")
	if s.workspace.List.Filter != "" || s.workspace.List.Filtering {
		t.Fatalf("hidden preferred did not clear the filter: %q filtering=%v", s.workspace.List.Filter, s.workspace.List.Filtering)
	}
	if row, ok := s.workspace.List.SelectedRow(); !ok || row.Cells[0] != "arnold" {
		t.Fatalf("SelectedRow %+v, want arnold", row)
	}
	// A visible preferred keeps the filter and selects the filtered position.
	s.workspace.List.Filter, s.workspace.List.Filtering = "arnold", true
	s.workspace.List.StartFilter()
	s.syncRows("arnold")
	if s.workspace.List.Filter != "arnold" || !s.workspace.List.Filtering {
		t.Fatalf("visible preferred must keep the filter: %q filtering=%v", s.workspace.List.Filter, s.workspace.List.Filtering)
	}
	if row, ok := s.workspace.List.SelectedRow(); !ok || row.Cells[0] != "arnold" {
		t.Fatalf("SelectedRow %+v, want arnold", row)
	}
}
