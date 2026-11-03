package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTableCellWidthAlignmentAndUnicodeMask(t *testing.T) {
	tb := NewTable([]Column{{Title: "name", Width: 5}, {Title: "n", Width: 3, Align: AlignRight}}, []Row{{Cells: []string{"Alice", "12"}}, {Cells: []string{"密碼", "3"}}})
	tb.RenderCell = func(_ Row, c int, v string) string {
		if c == 0 && v == "密碼" {
			return "遮罩"
		}
		return v
	}
	view := tb.View(18, 2)
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 18 {
			t.Fatalf("overflow %q", line)
		}
	}
	if !strings.Contains(view, " 12") {
		t.Fatalf("right alignment %q", view)
	}
	if !strings.Contains(view, "遮罩") {
		t.Fatalf("unicode masking %q", view)
	}
}
func TestTableScrollSelectionResizeAndSetRows(t *testing.T) {
	rows := make([]Row, 8)
	for i := range rows {
		rows[i] = Row{Cells: []string{string(rune('a' + i))}, Data: i}
	}
	tb := NewTable([]Column{{Width: 1}}, rows)
	tb.UpdateKey("pgdown", 3)
	if tb.Selected != 3 || !strings.Contains(tb.View(10, 3), "d") {
		t.Fatal("page scroll")
	}
	tb.UpdateKey("end", 3)
	if r, ok := tb.SelectedRow(); !ok || r.Data != 7 {
		t.Fatal("selected row")
	}
	if !strings.Contains(tb.View(10, 1), "h") {
		t.Fatal("height resize did not keep selected row visible")
	}
	tb.UpdateKey("home", 1)
	tb.UpdateKey("down", 1)
	tb.UpdateKey("j", 1)
	tb.UpdateKey("k", 1)
	tb.UpdateKey("up", 1)
	if tb.Selected != 0 {
		t.Fatal("navigation keys bounds")
	}
	tb.SetRows(rows[:2])
	tb.Selected = 6
	tb.SetRows(rows[:1])
	if tb.Selected != 0 {
		t.Fatal("rows clamp selection")
	}
}
func TestTableEmptyRendererAndBounds(t *testing.T) {
	tb := NewTable(nil, nil)
	tb.EmptyText = "empty"
	if tb.View(10, 2) != "empty" {
		t.Fatal("empty text")
	}
	tb = NewTable([]Column{{Width: 9}}, []Row{{Cells: []string{"secret"}}})
	tb.RenderCell = func(_ Row, _ int, _ string) string { return "mask" }
	if got := tb.View(4, 1); lipgloss.Width(got) > 4 || strings.Contains(got, "secret") {
		t.Fatalf("width/mask %q", got)
	}
}

func TestTableSingleLineAndNarrowBounds(t *testing.T) {
	tb := NewTable([]Column{{Width: 8}}, []Row{{Cells: []string{"a\r\nb\tc\x01d"}}, {Cells: []string{"界界"}}})
	for _, width := range []int{0, 1, 2, 4} {
		view := tb.View(width, 1)
		if lipgloss.Width(view) > width || strings.ContainsAny(view, "\r\t\x01") || strings.Contains(view, "\n") {
			t.Fatalf("width %d unsafe view %q", width, view)
		}
		if width == 0 && view != "" {
			t.Fatalf("zero width %q", view)
		}
		if width == 1 && view != ">" {
			t.Fatalf("one-cell marker %q", view)
		}
	}
	if got := tb.View(10, 1); got != "> a  b c d" {
		t.Fatalf("sanitized cell %q", got)
	}
	if got := tb.View(8, 2); !strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("expected two sanitized rows: %q", got)
	}
}

func TestTableANSIAndEmptyHeightBounds(t *testing.T) {
	tb := NewTable([]Column{{Width: 3}}, []Row{{Cells: []string{"\x1b[31m界界\x1b[0m"}}})
	got := tb.View(5, 1)
	if got != "> 界 " {
		t.Fatalf("ANSI/wide width %q", got)
	}
	tb.RenderCell = func(_ Row, _ int, _ string) string { return "\x1b[31m界\nX\x1b[0m" }
	got = tb.View(5, 1)
	if got != "> 界 " {
		t.Fatalf("ANSI/wide renderer cell %q", got)
	}
	empty := NewTable(nil, nil)
	empty.EmptyText = "\r\nempty\t"
	if got := empty.View(20, 0); got != "" {
		t.Fatalf("empty zero-height %q", got)
	}
	if got := empty.View(20, 1); got != "  empty " {
		t.Fatalf("empty text sanitization %q", got)
	}
}
