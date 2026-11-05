package main

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Route... dialog of the Accounts screen mirrors `netivri account show`:
// the configured provider and country lists, the route as ordered tiers and,
// when the angel runs, the live node state. It carries labels and country
// codes only — a hop URL, an address or any other credential never enters a
// line (accountsHopLabel shows state labels, not values).

// routeCloseMsg closes the route viewer that sent it.
type routeCloseMsg struct{}

// accountsRouteLoaded delivers a finished `account show` for the Route dialog.
type accountsRouteLoaded struct {
	Login      string
	Result     AccountShowResult
	Err        error
	Generation uint64
}

type routeViewer struct {
	login            string
	loading          bool
	result           *AccountShowResult
	scroll, viewport int
	Theme            tui.Theme
}

func newRouteViewer(login string) *routeViewer {
	return &routeViewer{login: login, loading: true, viewport: 20}
}
func (v *routeViewer) SetTheme(t tui.Theme) { v.Theme = t }
func (v *routeViewer) CapturingInput() bool { return true }

func (v *routeViewer) maxScroll() int {
	return max(0, len(v.lines())-max(1, v.viewport))
}

func (v *routeViewer) Update(m tea.Msg) (tui.Modal, tea.Cmd) {
	if k, ok := m.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return v, func() tea.Msg { return routeCloseMsg{} }
		case "up":
			v.scroll = max(0, v.scroll-1)
		case "down":
			v.scroll = min(v.maxScroll(), v.scroll+1)
		case "pgup":
			v.scroll = max(0, v.scroll-max(1, v.viewport))
		case "pgdown":
			v.scroll = min(v.maxScroll(), v.scroll+max(1, v.viewport))
		case "home":
			v.scroll = 0
		case "end":
			v.scroll = v.maxScroll()
		}
		return v, nil
	}
	if w, ok := m.(tea.MouseWheelMsg); ok {
		mouse := tea.Mouse(w)
		switch mouse.Button {
		case tea.MouseWheelUp:
			v.scroll = max(0, v.scroll-1)
		case tea.MouseWheelDown:
			v.scroll = min(v.maxScroll(), v.scroll+1)
		}
		return v, nil
	}
	return v, nil
}

// accountsRouteNodeState names the live state of a node without its figures'
// provenance: down when routing skips it, not probed without an angel, else up.
func accountsRouteNodeState(n accountShowNode) string {
	if n.Down != nil && *n.Down {
		return "down"
	}
	if n.Alive == nil {
		return "not probed"
	}
	if *n.Alive {
		return "up"
	}
	return "down"
}

func accountsRouteConns(n accountShowNode) string {
	if n.Connections == nil {
		return "-"
	}
	return strconv.Itoa(*n.Connections)
}

func accountsListOrAny(ss []string) string {
	if len(ss) == 0 {
		return "any"
	}
	return strings.Join(ss, ",")
}

// lines builds the dialog text: labels and countries only, never Own, never a
// Value URL, never an address.
func (v *routeViewer) lines() []string {
	if v.loading || v.result == nil {
		return nil
	}
	r := v.result
	lines := []string{
		"Exit " + r.Exit,
		"Providers " + accountsListOrAny(r.Providers) + " · Countries " + accountsListOrAny(r.Countries),
		"Pre-hop " + accountsHopLabel(r.PreSOCKS5.Source, r.PreSOCKS5.Value) + " · Post-hop " + accountsHopLabel(r.PostSOCKS5.Source, r.PostSOCKS5.Value),
	}
	if !r.Live {
		lines = append(lines, "The angel is not running; live node state and connection counts are unavailable.")
	}
	if len(r.Tiers) == 0 {
		lines = append(lines, "No route tiers")
	}
	for _, t := range r.Tiers {
		if t.Skipped {
			reason := t.Reason
			if reason == "" {
				reason = "no nodes"
			}
			lines = append(lines, "Skipped · country "+t.Country+" · providers "+strings.Join(t.Providers, ",")+" · "+reason)
			continue
		}
		kind := "primary"
		if t.Fallback {
			kind = "fallback"
		}
		lines = append(lines, "Tier "+strconv.Itoa(t.Tier)+" "+kind+" · country "+t.Country+" · providers "+strings.Join(t.Providers, ","))
		for _, n := range t.Nodes {
			label := n.Label
			if label == "" {
				label = n.Name
			}
			lines = append(lines, "  "+label+" · "+n.Country+" · "+accountsRouteNodeState(n)+" · connections "+accountsRouteConns(n))
		}
	}
	if r.Live {
		lines = append(lines, "Fallback connections "+accountsFallbackText(r.FallbackConnections))
	}
	return lines
}

func (v *routeViewer) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	title := "Route " + v.login
	hint := "Up/Down/PgUp/PgDn/Home/End scroll - Esc closes"
	var visible []string
	if v.loading {
		visible = []string{"Loading route for " + v.login + "..."}
	} else {
		body := v.lines()
		v.viewport = max(1, h-2)
		if v.scroll > v.maxScroll() {
			v.scroll = v.maxScroll()
		}
		if v.scroll < 0 {
			v.scroll = 0
		}
		if v.scroll < len(body) {
			visible = body[v.scroll:min(len(body), v.scroll+v.viewport)]
		}
	}
	lines := append([]string{title}, visible...)
	lines = append(lines, hint)
	return boundedNodes(lines, w, h)
}

// openRoute loads the route of the selected account through the same
// `account show` operation the CLI runs and opens a read-only viewer.
func (s *accountsScreen) openRoute() tea.Cmd {
	login := s.selectedLogin()
	if login == "" {
		return nil
	}
	s.viewer = newRouteViewer(login)
	s.routeLogin = login
	s.routeGeneration++
	g := s.routeGeneration
	return s.bg("account route", func() tea.Msg {
		r, e := s.show(s.env.Home, login)
		return accountsRouteLoaded{Login: login, Result: r, Err: e, Generation: g}
	})
}

// routeViewerInput routes input and close messages to the open route viewer.
func (s *accountsScreen) routeViewerInput(msg tea.Msg) (tea.Cmd, bool) {
	if _, ok := msg.(routeCloseMsg); ok {
		s.viewer = nil
		return nil, true
	}
	if s.viewer == nil {
		return nil, false
	}
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg:
		var c tea.Cmd
		s.viewer, c = s.viewer.Update(msg)
		return c, true
	}
	return nil, false
}

// applyRouteLoaded fills the open viewer; an error closes it and is shown by
// message and hint only.
func (s *accountsScreen) applyRouteLoaded(m accountsRouteLoaded) tea.Cmd {
	if s.viewer == nil || m.Generation != s.routeGeneration || m.Login != s.routeLogin {
		return nil
	}
	if m.Err != nil {
		s.viewer = nil
		s.message = accountsErrorText(m.Err)
		return s.notice(s.message)
	}
	v, ok := s.viewer.(*routeViewer)
	if !ok {
		return nil
	}
	v.loading = false
	v.result = &m.Result
	return nil
}
