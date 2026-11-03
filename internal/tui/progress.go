package tui

import (
	"charm.land/lipgloss/v2"
	"strings"
)

// Progress renders a bounded ASCII percentage bar.
type Progress struct {
	Label string
	Value int
}

func (p Progress) Percent() int {
	if p.Value < 0 {
		return 0
	}
	if p.Value > 100 {
		return 100
	}
	return p.Value
}
func (p Progress) View(width int) string {
	if width <= 0 {
		return ""
	}
	pct := p.Percent()
	label := p.Label
	if lipgloss.Width(label) > width {
		label = clipCells(label, width)
	}
	barWidth := width - lipgloss.Width(label) - 8
	if barWidth < 1 {
		barWidth = 1
	}
	filled := barWidth * pct / 100
	bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
	return clipCells(label+" ["+bar+"] "+itoa(pct)+"%", width)
}
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [3]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
