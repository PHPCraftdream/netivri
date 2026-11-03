package tui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Alignment controls horizontal alignment in a rendered cell.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
	AlignCenter
)

// Row is a row of display cells with optional associated data.
type Row struct {
	Cells []string
	Data  any
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// boundLines clips content to an exact terminal rectangle without wrapping.
func boundLines(lines []string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			line = ansi.Truncate(line, width, "")
		}
		lines[i] = line + strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

// singleLine removes ANSI styling and replaces controls with spaces.
func singleLine(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// clipTableCell sanitizes and clips a cell to the requested terminal width.
func clipTableCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(singleLine(s), width, "")
}
