package main

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The read-only dialogs of the Providers screen that mirror the node
// commands: Nodes... shows the stored nodes of the selected provider (the
// data of `netivri node list` through the shared NodeList operation), and
// Diagnose... shows the figures of a finished `diag dial --node` run. Both
// carry labels only: an address, a port or a credential never enters a line.

// nodesCloseMsg closes the dialog that sent it.
type nodesCloseMsg struct{}

// nodesDialog is the read-only list of one provider's stored nodes.
type nodesDialog struct {
	provider  string
	entries   []NodeEntry
	countries []string // the country codes present, sorted; the cycle is all + these
	cycle     int
	list      *tui.List
	Theme     tui.Theme
}

func newNodesDialog(provider string, entries []NodeEntry) *nodesDialog {
	d := &nodesDialog{provider: provider, entries: entries, list: tui.NewList(
		[]tui.ListColumn{{Title: "Node", Width: 26, Flex: 1}, {Title: "Country", Width: 9}, {Title: "Name", Width: 24, Flex: 1}}, nil)}
	d.list.Title = "Nodes"
	d.list.EmptyText = "No stored nodes"
	d.list.AddLabel = ""
	d.list.Focused = true
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Country != "" && !seen[e.Country] {
			seen[e.Country] = true
			d.countries = append(d.countries, e.Country)
		}
	}
	sort.Strings(d.countries)
	d.setRows()
	return d
}

func (d *nodesDialog) SetTheme(t tui.Theme) { d.Theme = t; d.list.SetTheme(t) }
func (d *nodesDialog) CapturingInput() bool { return true }

// filterCountry is the country the cycle position selects; "" is all.
func (d *nodesDialog) filterCountry() string {
	if d.cycle == 0 {
		return ""
	}
	return d.countries[d.cycle-1]
}

func (d *nodesDialog) countryLabel() string {
	if d.cycle == 0 {
		return "all"
	}
	return d.countries[d.cycle-1]
}

func (d *nodesDialog) cycleCountry() {
	d.cycle = (d.cycle + 1) % (len(d.countries) + 1)
	d.setRows()
}

func (d *nodesDialog) setRows() {
	cc := d.filterCountry()
	rows := make([]tui.Row, 0, len(d.entries))
	for _, e := range d.entries {
		if cc != "" && e.Country != cc {
			continue
		}
		rows = append(rows, tui.Row{Cells: []string{e.Label(), e.Country, e.Name}, Data: e.Label()})
	}
	d.list.SetRows(rows)
}

func (d *nodesDialog) Update(m tea.Msg) (tui.Modal, tea.Cmd) {
	if k, ok := m.(tea.KeyPressMsg); ok && !d.list.Filtering {
		switch k.String() {
		case "c":
			d.cycleCountry()
			return d, nil
		case "esc":
			return d, func() tea.Msg { return nodesCloseMsg{} }
		}
	}
	d.list.Update(m)
	return d, nil
}

func (d *nodesDialog) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	hint := "c country: " + d.countryLabel() + "  / filter  Esc closes"
	var lines []string
	lines = append(lines, clipNodesLine(fmt.Sprintf("Provider %s: nodes", d.provider), w))
	if h >= 3 {
		lines = append(lines, strings.Split(d.list.View(w, h-2), "\n")...)
	}
	if h >= 2 {
		lines = append(lines, clipNodesLine(hint, w))
	}
	for len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// diagResultDialog shows the finished measurement of one node: the same
// figures `diag dial` prints, labels only.
type diagResultDialog struct {
	diag  core.NodeDiag
	Theme tui.Theme
}

func newDiagResultDialog(n core.NodeDiag) *diagResultDialog { return &diagResultDialog{diag: n} }
func (d *diagResultDialog) SetTheme(t tui.Theme)            { d.Theme = t }
func (d *diagResultDialog) CapturingInput() bool            { return true }

func (d *diagResultDialog) Update(m tea.Msg) (tui.Modal, tea.Cmd) {
	if k, ok := m.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return d, func() tea.Msg { return nodesCloseMsg{} }
	}
	return d, nil
}

func (d *diagResultDialog) lines() []string {
	n := d.diag
	lines := []string{"Diagnosis of " + n.Label, fmt.Sprintf("%d samples", max(0, n.Samples))}
	phases := []struct {
		name string
		p    core.PhaseStats
	}{{"dns", n.DNS}, {"tcp", n.TCP}, {"tls", n.TLS}, {"first_byte", n.FirstByte}}
	for _, p := range phases {
		if p.p.N <= 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-10s p50 %v ms  p90 %v ms  max %v ms  n %d", p.name, diagMS(p.p.P50), diagMS(p.p.P90), diagMS(p.p.Max), p.p.N))
	}
	lines = append(lines, fmt.Sprintf("Failures: dns %d  tcp %d  tls %d  first_byte %d", n.Failures["dns"], n.Failures["tcp"], n.Failures["tls"], n.Failures["first_byte"]))
	return lines
}

func (d *diagResultDialog) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := append([]string(nil), d.lines()...)
	lines = append(lines, "Esc closes")
	return boundedNodes(lines, w, h)
}

// boundedNodes clamps dialog lines to the frame: exactly h lines, none wider than w.
func boundedNodes(lines []string, w, h int) string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, x := range lines {
		if lipgloss.Width(x) > w {
			lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(x)
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func clipNodesLine(s string, w int) string {
	if w > 0 && lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	return s
}
