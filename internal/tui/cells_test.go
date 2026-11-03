package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBoundLinesExactBounds(t *testing.T) {
	got := boundLines([]string{"界界x", "second", "third"}, 3, 2)
	if got != "界  \nsec" {
		t.Fatalf("got %q", got)
	}
	lines := strings.Split(boundLines([]string{"x"}, 4, 3), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count: %d", len(lines))
	}
	for _, line := range lines {
		if lipgloss.Width(line) != 4 {
			t.Fatalf("line width: %q", line)
		}
	}
	if got := boundLines([]string{"x"}, 0, 2); got != "" {
		t.Fatalf("zero width: %q", got)
	}
}

func TestCellSanitizationAndClipping(t *testing.T) {
	if got := singleLine("\x1b[31ma\nb\x01\x1b[0m"); got != "a b " {
		t.Fatalf("sanitized: %q", got)
	}
	for _, tc := range []struct {
		in    string
		width int
	}{{"界界", 3}, {"\x1b[31m界界\x1b[0m", 2}, {"abcdef", 0}} {
		got := clipTableCell(tc.in, tc.width)
		if lipgloss.Width(got) > tc.width {
			t.Fatalf("width %d output %q", tc.width, got)
		}
	}
}

func TestBoolInt(t *testing.T) {
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatal("unexpected conversion")
	}
}
