package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestListMarkGutterAndUnmarkedIdentity checks the Row.Mark gutter: a marked
// row is prefixed "* " before its cells, unmarked rows show no gutter, and the frame contract (exact h lines, none wider
// than w) holds.
func TestListMarkGutterAndUnmarkedIdentity(t *testing.T) {
	cols := []ListColumn{{Title: "NAME", Min: 4, Flex: 1}, {Title: "COUNTRY", Min: 4, Flex: 1}}
	unmarked := func() []Row {
		return []Row{{Cells: []string{"alpha", "DE"}}, {Cells: []string{"beta", "NL"}}}
	}
	for _, size := range [][2]int{{40, 8}, {60, 6}} {
		w, h := size[0], size[1]
		base := NewList(cols, unmarked())
		base.SetSize(w, h)
		got := base.View(w, h)
		lines := strings.Split(got, "\n")
		if len(lines) != h {
			t.Fatalf("size %dx%d: %d lines", w, h, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > w {
				t.Fatalf("size %dx%d: line wider than %d: %q", w, h, w, line)
			}
		}
		if strings.Contains(got, "*") {
			t.Fatalf("size %dx%d: unmarked list shows a mark gutter: %q", w, h, got)
		}
	}
	rows := unmarked()
	rows[0].Mark = true
	l := NewList(cols, rows)
	l.SetSize(40, 8)
	got := l.View(40, 8)
	lines := strings.Split(got, "\n")
	if len(lines) != 8 {
		t.Fatalf("marked list rendered %d lines, want 8", len(lines))
	}
	var marked, plain []string
	for _, line := range lines {
		if strings.Contains(line, "* alpha") {
			marked = append(marked, line)
		}
		if strings.Contains(line, "beta") {
			plain = append(plain, line)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("marked row not rendered exactly once: %q", got)
	}
	if !strings.Contains(marked[0], "* alpha") {
		t.Fatalf("marked row lacks \"* <cell>\": %q", marked[0])
	}
	if len(plain) != 1 || strings.Contains(plain[0], "*") {
		t.Fatalf("unmarked row shows a mark gutter: %q", got)
	}
}
