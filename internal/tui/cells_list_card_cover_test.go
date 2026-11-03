package tui

import (
	"strings"
	"testing"
)

func asciiTestTheme() Theme {
	th := NewTheme()
	th.ASCII = true
	return th
}

func TestListRenderCellsAlignment(t *testing.T) {
	l := NewList(
		[]ListColumn{
			{Title: "A", Width: 8, Align: AlignCenter},
			{Title: "B", Width: 6, Align: AlignRight},
		},
		[]Row{{Cells: []string{"x", "y"}}},
	)
	widths := []int{8, 6}
	got := l.renderCells(0, widths)
	if got[:8] != "   x    " {
		t.Fatalf("center cell = %q, want %q", got[:8], "   x    ")
	}
	if got[10:] != "     y" {
		t.Fatalf("right cell = %q, want %q", got[10:], "     y")
	}
	header := l.renderCells(-1, widths)
	if !strings.Contains(header, "A") || !strings.Contains(header, "B") {
		t.Fatalf("header row missing titles: %q", header)
	}
}

func TestListScrollbarThumbWhileScrolling(t *testing.T) {
	rows := make([]Row, 10)
	for i := range rows {
		rows[i] = Row{Cells: []string{"r" + string(rune('0'+i))}}
	}
	l := NewList([]ListColumn{{Title: "N", Width: 6}}, rows)
	l.AddLabel = "" // count rows only
	l.SetRows(rows)
	l.SetSize(20, 8)
	l.Update(tuiTestKey("end"))
	if l.Selected != 9 {
		t.Fatalf("Selected = %d, want 9 after end key", l.Selected)
	}
	v := l.View(20, 8)
	if !strings.Contains(v, "█") {
		t.Fatalf("unicode view missing scrollbar thumb: %q", v)
	}
	l.SetTheme(asciiTestTheme())
	a := l.View(20, 8)
	if !strings.Contains(a, "#") {
		t.Fatalf("ascii view missing scrollbar thumb: %q", a)
	}
	if strings.Contains(a, "█") {
		t.Fatalf("ascii view still shows unicode thumb: %q", a)
	}
}

func TestCardTinyView(t *testing.T) {
	c := NewCard("t", nil)
	if got := c.View(1, 1); got != " " {
		t.Fatalf("View(1,1) = %q, want single space", got)
	}
	if got := c.View(3, 1); got != "   " {
		t.Fatalf("View(3,1) = %q, want three spaces", got)
	}
	if got := c.View(4, 0); got != "" {
		t.Fatalf("View(4,0) = %q, want empty", got)
	}
}

func TestClipCardCellEllipsis(t *testing.T) {
	th := asciiTestTheme()
	if got := clipCardCell("hello world", 5, th); got != "hell." {
		t.Fatalf("ascii clip = %q, want %q", got, "hell.")
	}
	if got := clipCardCell("hello", 5, th); got != "hello" {
		t.Fatalf("unclipped value changed: %q", got)
	}
	if got := clipCardCell("hello", 0, th); got != "" {
		t.Fatalf("zero width clip = %q, want empty", got)
	}
}

func TestCardEnsureVisibleUnfocusedKeepsScroll(t *testing.T) {
	lines := make([]CardLine, 10)
	for i := range lines {
		lines[i] = CardLine{Label: "L", Value: "v", Kind: CardField, Action: true}
	}
	c := NewCard("title", lines)
	c.SetSize(40, 6) // visibleRows = 4
	c.Selected = 9
	c.ensureVisible()
	if c.Scroll != 0 {
		t.Fatalf("unfocused Scroll = %d, want 0", c.Scroll)
	}
	c.Focused = true
	c.SetSize(40, 7) // size change triggers ensureVisible; visibleRows = 5
	if c.Scroll != 6 {
		t.Fatalf("focused Scroll = %d, want 6", c.Scroll)
	}
}
