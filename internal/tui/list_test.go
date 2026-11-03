package tui

import (
	"strings"
	"testing"

	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestListNavigationDoesNotActivate(t *testing.T) {
	l := NewList(nil, []Row{{}, {}, {}, {}, {}})
	l.SetSize(20, 6) // three visible rows: height minus border and status
	for _, tc := range []struct {
		key  string
		want int
	}{{"down", 1}, {"j", 2}, {"up", 1}, {"k", 0}, {"end", 5}, {"pgup", 2}, {"home", 0}, {"pgdown", 3}} {
		if got := l.Update(tuiTestKey(tc.key)); got {
			t.Fatalf("%s activated", tc.key)
		}
		if l.Selected != tc.want {
			t.Fatalf("%s selected %d, want %d", tc.key, l.Selected, tc.want)
		}
	}
	if !l.Update(tuiTestKey("enter")) {
		t.Fatal("enter on data row should activate")
	}
}

func TestListAddAndReadOnlyBindings(t *testing.T) {
	l := NewList(nil, []Row{{}})
	l.Update(tuiTestKey("a"))
	if !l.IsAdd() || !l.Update(tuiTestKey("enter")) {
		t.Fatal("add row selection/activation mismatch")
	}
	if _, ok := l.SelectedRow(); ok {
		t.Fatal("add row is not data")
	}
	readOnly := NewList(nil, []Row{{}})
	readOnly.AddLabel = ""
	if readOnly.Update(tuiTestKey("a")) || readOnly.Selected != 0 {
		t.Fatal("read-only list accepted a")
	}
	for _, b := range readOnly.Bindings() {
		if b.Keys() != nil && slices.Contains(b.Keys(), "a") {
			t.Fatal("read-only bindings contain a")
		}
	}
}

func TestListDoubleClick(t *testing.T) {
	l := NewList(nil, []Row{{}, {}})
	l.SetSize(12, 6)
	if l.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft}) {
		t.Fatal("first click activated")
	}
	if !l.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft}) {
		t.Fatal("second click should activate")
	}
}

func TestListBoundsAndTinySizes(t *testing.T) {
	l := NewList([]ListColumn{{Title: "Name", Width: 20}}, []Row{{Cells: []string{"界界界"}}})
	for _, sz := range [][2]int{{-1, -1}, {1, 1}, {2, 2}, {5, 3}, {12, 5}} {
		got := l.View(sz[0], sz[1])
		if sz[0] <= 0 || sz[1] <= 0 {
			if got != "" {
				t.Fatalf("View%v = %q", sz, got)
			}
			continue
		}
		lines := strings.Split(got, "\n")
		if len(lines) != sz[1] {
			t.Fatalf("View%v has %d lines", sz, len(lines))
		}
		for _, line := range lines {
			if lipglossWidth(line) > sz[0] {
				t.Fatalf("View%v overflows: %q", sz, line)
			}
		}
	}
}

func TestListEmptyExplanationAndHeader(t *testing.T) {
	l := NewList([]ListColumn{{Title: "名前", Width: 6}, {Title: "Count", Width: 5, Align: AlignRight}}, nil)
	l.EmptyText = "Nothing here"
	l.SetSize(22, 6)
	if got := l.View(22, 6); !strings.Contains(got, "名前") || !strings.Contains(got, "Count") || !strings.Contains(got, "Nothing") {
		t.Fatalf("missing header/empty explanation: %q", got)
	}
	l.Selected = 0
	if !l.IsAdd() {
		t.Fatal("empty list add row should be focused")
	}
}

func TestListASCIIAndScrollPosition(t *testing.T) {
	t.Setenv("NETIVRI_ASCII", "1")
	t.Setenv("LANG", "en_US.UTF-8")
	l := NewList(nil, []Row{{}, {}, {}, {}, {}, {}})
	l.SetSize(12, 6)
	l.Update(tuiTestKey("end"))
	if l.Selected != 6 {
		t.Fatalf("selected %d, want 6", l.Selected)
	}
	if l.offset != 4 {
		t.Fatalf("offset %d, want 4", l.offset)
	}
	got := l.View(12, 6)
	if !strings.HasPrefix(got, "+----------+\n") || !strings.Contains(got, "7/7") {
		t.Fatalf("unexpected ASCII/scroll view: %q", got)
	}
	lines := strings.Split(got, "\n")
	for i, line := range lines[1:4] {
		if lipgloss.Width(line) != 12 || lipgloss.Width(line[:10]) != 10 || (line[10] != '#' && line[10] != '|') || line[11] != '|' {
			t.Fatalf("row %d has misaligned scrollbar or width: %q", i, line)
		}
	}
}

func lipglossWidth(s string) int { return lipgloss.Width(s) }
