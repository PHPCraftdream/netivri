package main

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func routeTestScreen(t *testing.T, h string) *accountsScreen {
	t.Helper()
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.workspace.SetFocus(tui.PaneList)
	return s
}

// TestAccountsRouteViewerStoppedAngel opens Route... with the real
// `account show` operation and no running angel: the busy state shows while
// the load runs, the dialog then reports the configured route and one line
// saying the angel is stopped, and Esc closes it.
func TestAccountsRouteViewerStoppedAngel(t *testing.T) {
	h := accountsHome(t)
	if _, e := AccountAdd(h, AccountAddOptions{Login: "alice", Exit: "direct", ExitGiven: true}); e != nil {
		t.Fatal(e)
	}
	s := routeTestScreen(t, h)
	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	if s.selectedLogin() != "alice" {
		t.Fatalf("selection = %q", s.selectedLogin())
	}
	cmd := s.openRoute()
	if cmd == nil || s.viewer == nil {
		t.Fatal("openRoute did not open the route viewer")
	}
	if v := s.View(100, 30); !strings.Contains(v, "Loading route for alice...") {
		t.Fatalf("busy state missing: %q", v)
	}
	runAccountCmd(s, cmd)
	v := s.View(100, 30)
	if strings.Contains(v, "Loading route") {
		t.Fatal("viewer stayed busy after the load")
	}
	for _, want := range []string{"Route alice", "Exit direct", "No route tiers", "The angel is not running"} {
		if !strings.Contains(v, want) {
			t.Fatalf("route view missing %q: %q", want, v)
		}
	}
	if strings.Contains(v, "Fallback connections") {
		t.Fatal("stopped angel shows live fallback figures")
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if esc == nil {
		t.Fatal("esc produced no close command")
	}
	msg := esc()
	if _, ok := msg.(routeCloseMsg); !ok {
		t.Fatalf("esc produced %#v", msg)
	}
	_, _ = s.Update(msg)
	if s.viewer != nil {
		t.Fatal("esc did not close the route viewer")
	}
}

// TestAccountsRouteViewerRunningAngelInjected injects a live `account show`
// result: tiers with provider and country, per-node live state and connection
// counts, the fallback total — and never a credential.
func TestAccountsRouteViewerRunningAngelInjected(t *testing.T) {
	s := routeTestScreen(t, "")
	up, off, down := true, false, true
	conn3, conn0, fb := 3, 0, 2
	secret := []string{"socks5://u:fake-token@host:1080"}
	s.show = func(_, _ string) (AccountShowResult, error) {
		return AccountShowResult{
			Login: "alice", Provider: "p1", ExitCountry: "DE", Exit: "route",
			Providers: []string{"p1"}, Countries: []string{"DE", "NL"},
			PreSOCKS5:  accountShowChainInfo{Source: "account", Value: []string{"socks5://192.0.2.1:1080"}, Own: &secret},
			PostSOCKS5: accountShowChainInfo{Source: "global", Value: []string{"none"}},
			Live:       true, FallbackConnections: &fb,
			Tiers: []accountShowTier{
				{Tier: 0, Country: "DE", Providers: []string{"p1"}, Nodes: []accountShowNode{
					{Label: "p1 #1", Country: "DE", Alive: &up, Connections: &conn3},
					{Label: "p1 #2", Country: "DE", Alive: &off, Down: &down, Connections: &conn0},
				}},
				{Tier: 1, Country: "NL", Providers: []string{"p1"}, Fallback: true, Nodes: []accountShowNode{
					{Label: "p1 #3", Country: "NL"},
				}},
			},
		}, nil
	}
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	runAccountCmd(s, s.openRoute())
	v := s.View(120, 40)
	for _, want := range []string{
		"Exit route",
		"Providers p1 · Countries DE,NL",
		"Pre-hop account set · Post-hop global none",
		"Tier 0 primary · country DE · providers p1",
		"p1 #1 · DE · up · connections 3",
		"p1 #2 · DE · down · connections 0",
		"Tier 1 fallback · country NL · providers p1",
		"p1 #3 · NL · not probed · connections -",
		"Fallback connections 2",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("route view missing %q: %q", want, v)
		}
	}
	if strings.Contains(v, "The angel is not running") {
		t.Fatal("running angel reported as stopped")
	}
	if strings.Contains(v, "fake-token") || strings.Contains(v, "u:") || strings.Contains(v, "secret") {
		t.Fatal("route view leaked a hop address or credential")
	}
}

// TestAccountsRouteViewerScrollAndContract drives the scroll keys and checks
// the frame contract at the four sizes: exactly h lines, none wider than w.
func TestAccountsRouteViewerScrollAndContract(t *testing.T) {
	s := routeTestScreen(t, "")
	s.show = func(_, _ string) (AccountShowResult, error) {
		tiers := []accountShowTier{}
		for i := 0; i < 30; i++ {
			tiers = append(tiers, accountShowTier{Tier: i, Country: "DE", Providers: []string{"p1"}, Nodes: []accountShowNode{{Label: "n" + strconv.Itoa(i), Country: "DE"}}})
		}
		return AccountShowResult{Login: "alice", Exit: "route", Live: false, Tiers: tiers}, nil
	}
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	runAccountCmd(s, s.openRoute())
	v := s.viewer.(*routeViewer)
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		out := s.View(size[0], size[1])
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Fatalf("size %v: %d lines, want %d", size, len(lines), size[1])
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("size %v: line wider than %d: %q", size, size[0], line)
			}
		}
	}
	s.View(80, 10)
	if v.viewport != 8 {
		t.Fatalf("viewport = %d, want 8", v.viewport)
	}
	if !strings.Contains(s.View(80, 10), "n0 · DE · not probed") {
		t.Fatal("top of the route missing at scroll 0")
	}
	for i := 0; i < 6; i++ {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if v.scroll != 6 {
		t.Fatalf("scroll after 6 down = %d, want 6", v.scroll)
	}
	if strings.Contains(s.View(80, 10), "n0 · DE") {
		t.Fatal("scrolled view still shows the first node")
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if v.scroll != 14 {
		t.Fatalf("scroll after pgdown = %d, want 14", v.scroll)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if v.scroll != 0 {
		t.Fatalf("scroll after home = %d, want 0", v.scroll)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if v.scroll != v.maxScroll() || v.maxScroll() <= 0 {
		t.Fatalf("scroll after end = %d (max %d)", v.scroll, v.maxScroll())
	}
	if !strings.Contains(s.View(80, 10), "n29") {
		t.Fatal("end did not reach the last tier")
	}
	if !strings.Contains(s.View(80, 10), "Esc closes") {
		t.Fatal("the hint was cut off at the end of a long route")
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m, ok := esc().(routeCloseMsg); !ok {
		t.Fatalf("esc produced %#v", m)
	}
}

// TestAccountsRouteViewerLoadErrorCloses closes the dialog on a failed load
// and reports the error by message and hint only.
func TestAccountsRouteViewerLoadErrorCloses(t *testing.T) {
	s := routeTestScreen(t, "")
	s.show = func(_, _ string) (AccountShowResult, error) {
		return AccountShowResult{}, &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "account not found: alice", Hint: "netivri account list", Exit: cli.ExitNotFound}
	}
	s.rows = []accountInfo{{Login: "alice"}}
	s.syncRows("alice")
	runAccountCmd(s, s.openRoute())
	if s.viewer != nil {
		t.Fatal("load error left the viewer open")
	}
	if !strings.Contains(s.message, "account not found: alice") || !strings.Contains(s.message, "netivri account list") {
		t.Fatalf("error message = %q", s.message)
	}
}

// TestAccountsRouteViewerNoSelectionAndStaleLoad guards the empty list and a
// load result of an older generation or login.
func TestAccountsRouteViewerNoSelectionAndStaleLoad(t *testing.T) {
	s := routeTestScreen(t, "")
	if cmd := s.openRoute(); cmd != nil || s.viewer != nil {
		t.Fatal("openRoute without a selection opened the viewer")
	}
	up := true
	s.show = func(_, _ string) (AccountShowResult, error) {
		return AccountShowResult{Login: "bob", Exit: "route", Live: true, Tiers: []accountShowTier{{Tier: 0, Nodes: []accountShowNode{{Alive: &up}}}}}, nil
	}
	s.rows = []accountInfo{{Login: "alice"}, {Login: "bob"}}
	s.syncRows("bob")
	first := s.openRoute()
	second := s.openRoute()
	runAccountCmd(s, first)
	v, ok := s.viewer.(*routeViewer)
	if !ok {
		t.Fatal("viewer lost")
	}
	if !v.loading {
		t.Fatal("stale generation filled the viewer")
	}
	runAccountCmd(s, second)
	if v.loading || v.result == nil || v.result.Login != "bob" {
		t.Fatal("fresh load did not fill the viewer")
	}
}

// TestAccountsParityRefsIncludeRouteAndBulk checks the live card advertises
// both actions to the parity check, bulk as a disabled action without marks.
func TestAccountsParityRefsIncludeRouteAndBulk(t *testing.T) {
	home := parityFixtureHome(t)
	s := newAccountsScreen(home, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	s.rows, _ = AccountList(home)
	if len(s.rows) == 0 {
		t.Fatal("parity fixture has no accounts")
	}
	s.syncRows(s.rows[0].Login)
	s.refreshSelectedCard(true)
	refs := s.ParityRefs()
	has := func(ref string) bool {
		for _, r := range refs {
			if r == ref {
				return true
			}
		}
		return false
	}
	if !has("accounts/route") || !has("accounts/bulk") {
		t.Fatalf("parity refs %v lack accounts/route or accounts/bulk", refs)
	}
	disabled := false
	for _, l := range s.workspace.Card.Lines {
		if l.ID == "bulk" {
			disabled = l.Disabled
		}
	}
	if !disabled {
		t.Fatal("bulk action enabled while nothing is marked")
	}
}
