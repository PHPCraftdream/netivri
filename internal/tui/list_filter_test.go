package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func filterTestList() *List {
	cols := []ListColumn{{Title: "Name", Width: 10}, {Title: "Kind", Width: 10}, {Title: "State", Width: 10}}
	rows := []Row{
		{Cells: []string{"alpha", "server", "running"}},
		{Cells: []string{"beta", "client", "stopped"}},
		{Cells: []string{"gamma", "SERVER", "running"}},
	}
	l := NewList(cols, rows)
	l.Focused = true
	return l
}

func TestListSlashStartsFilterAndIsConsumed(t *testing.T) {
	l := filterTestList()
	if got := l.Update(tuiTestKey("/")); got {
		t.Fatal("/ should be consumed by the list, not passed up")
	}
	if !l.Filtering {
		t.Fatal("/ did not start filtering")
	}
	if l.Selected != 0 {
		t.Fatalf("selection %d, want first visible row", l.Selected)
	}
	// without focus, / is not captured
	u := filterTestList()
	u.Focused = false
	if u.Filtering || u.Update(tuiTestKey("/")) {
		t.Fatal("unfocused list must not start filtering")
	}
}

func TestListFilterMatchesMiddleColumnCaseInsensitive(t *testing.T) {
	l := filterTestList()
	l.Update(tuiTestKey("/"))
	for _, k := range []string{"s", "e", "R", "V"} {
		l.Update(tuiTestKey(k))
	}
	if l.Filter != "seRV" {
		t.Fatalf("filter text %q", l.Filter)
	}
	vr := l.visibleRows()
	if len(vr) != 2 || vr[0] != 0 || vr[1] != 2 {
		t.Fatalf("visible rows %v, want [0 2]", vr)
	}
	if l.count() != 3 { // two matches + add line
		t.Fatalf("count %d, want 3", l.count())
	}
	if row, ok := l.SelectedRow(); !ok || row.Cells[0] != "alpha" {
		t.Fatalf("SelectedRow %+v, want alpha row", row)
	}
}

func TestListFilterEscClearsEnterKeeps(t *testing.T) {
	l := filterTestList()
	l.Update(tuiTestKey("/"))
	l.Update(tuiTestKey("r"))
	l.Update(tuiTestKey("esc"))
	if l.Filtering || l.Filter != "" {
		t.Fatalf("Esc did not clear: filtering=%v filter=%q", l.Filtering, l.Filter)
	}
	if l.count() != 4 {
		t.Fatalf("count %d after Esc, want 4", l.count())
	}
	l.Update(tuiTestKey("/"))
	l.Update(tuiTestKey("r"))
	if len(l.visibleRows()) != 2 {
		t.Fatalf("visible %v, want [0 2]", l.visibleRows())
	}
	l.Update(tuiTestKey("enter"))
	if l.Filtering || l.Filter != "r" {
		t.Fatalf("Enter did not keep filter: filtering=%v filter=%q", l.Filtering, l.Filter)
	}
	if l.count() != 3 {
		t.Fatalf("count %d after Enter, want 3", l.count())
	}
}

func TestListFilterSelectionFollowsFirstMatchAndBackspace(t *testing.T) {
	l := filterTestList()
	l.Update(tuiTestKey("end"))
	if l.Selected != 3 {
		t.Fatalf("setup: selected %d", l.Selected)
	}
	l.Update(tuiTestKey("/"))
	l.Update(tuiTestKey("t"))
	if l.Selected != 0 {
		t.Fatalf("selection %d after filter, want 0 (first match)", l.Selected)
	}
	if row, ok := l.SelectedRow(); !ok || row.Cells[0] != "alpha" {
		t.Fatalf("SelectedRow %+v, want alpha", row)
	}
	l.Update(tuiTestKey("/"))
	l.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if l.Filter != "" || l.count() != 4 {
		t.Fatalf("backspace: filter %q count %d", l.Filter, l.count())
	}
}

func TestListFilterZeroMatchesKeepsAddLineSelectable(t *testing.T) {
	l := filterTestList()
	l.Update(tuiTestKey("/"))
	l.Update(tuiTestKey("z"))
	l.Update(tuiTestKey("z"))
	if len(l.visibleRows()) != 0 {
		t.Fatalf("visible %v, want none", l.visibleRows())
	}
	if l.Selected != 0 || !l.IsAdd() {
		t.Fatalf("selected %d IsAdd=%v, want add line selected", l.Selected, l.IsAdd())
	}
	if _, ok := l.SelectedRow(); ok {
		t.Fatal("add line must not resolve to a data row")
	}
	got := l.View(40, 10)
	if !strings.Contains(got, "+ Add") {
		t.Fatalf("add line missing on zero matches: %q", got)
	}
}

func TestListFilterNavigationOverSubset(t *testing.T) {
	l := filterTestList()
	l.Update(tuiTestKey("/"))
	l.Update(tuiTestKey("r")) // matches rows 0 and 2 via any cell
	if len(l.visibleRows()) != 2 {
		t.Fatalf("visible %v, want [0 2]", l.visibleRows())
	}
	if l.Selected != 0 {
		t.Fatalf("selected %d, want 0", l.Selected)
	}
	l.Update(tuiTestKey("down"))
	if l.Selected != 1 || rowName(l) != "gamma" {
		t.Fatalf("down over subset: selected %d row %q", l.Selected, rowName(l))
	}
	l.Update(tuiTestKey("end"))
	if !l.IsAdd() {
		t.Fatal("end should land on add line")
	}
	l.Update(tuiTestKey("a"))
	if !l.IsAdd() {
		t.Fatal("a hotkey should jump to add line while filtering")
	}
}

func rowName(l *List) string {
	r, _ := l.SelectedRow()
	if len(r.Cells) > 0 {
		return r.Cells[0]
	}
	return ""
}

func TestListFilterRenderContractInsidePane(t *testing.T) {
	for _, sz := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		l := filterTestList()
		l.Update(tuiTestKey("/"))
		l.Update(tuiTestKey("s"))
		l.Update(tuiTestKey("e"))
		view := l.View(sz[0], sz[1])
		checkRenderContract(t, view, sz[0], sz[1])
		stripped := stripANSIList(view)
		if !strings.Contains(stripped, "/se") {
			t.Fatalf("filter line not drawn inside pane at %dx%d: %q", sz[0], sz[1], stripped)
		}
		if !strings.Contains(stripped, "1/3") {
			t.Fatalf("status not over visible subset at %dx%d: %q", sz[0], sz[1], stripped)
		}
	}
	// empty-filter contract too
	l2 := filterTestList()
	l2.Update(tuiTestKey("/"))
	checkRenderContract(t, l2.View(60, 16), 60, 16)
}

func stripANSIList(s string) string { return ansi.Strip(s) }
