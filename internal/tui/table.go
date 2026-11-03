package tui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
	AlignCenter
)

type Column struct {
	Title string
	Width int
	Align Alignment
}
type Row struct {
	Cells []string
	Data  any
}
type CellRenderer func(row Row, column int, value string) string

// Table is a bounded selectable row view. Widths use terminal cell measurements.
type Table struct {
	Columns     []Column
	Rows        []Row
	EmptyText   string
	RenderCell  CellRenderer
	Selected    int
	offset      int
	lastVisible int
}

func NewTable(cols []Column, rows []Row) *Table {
	t := &Table{Columns: cols, EmptyText: "No rows"}
	t.SetRows(rows)
	return t
}
func (t *Table) SetRows(rows []Row) {
	t.Rows = append([]Row(nil), rows...)
	if t.Selected >= len(t.Rows) {
		t.Selected = len(t.Rows) - 1
	}
	if t.Selected < 0 {
		t.Selected = 0
	}
	t.offset = 0
	t.ensureVisible(t.lastVisible)
}
func (t *Table) SelectedRow() (Row, bool) {
	if t.Selected < 0 || t.Selected >= len(t.Rows) {
		return Row{}, false
	}
	return t.Rows[t.Selected], true
}
func (t *Table) UpdateKey(key string, visible int) {
	if visible < 1 {
		visible = 1
	}
	switch key {
	case "up", "k":
		t.Selected--
	case "down", "j":
		t.Selected++
	case "pgup":
		t.Selected -= visible
	case "pgdown":
		t.Selected += visible
	case "home":
		t.Selected = 0
	case "end":
		t.Selected = len(t.Rows) - 1
	}
	if t.Selected < 0 {
		t.Selected = 0
	}
	if t.Selected >= len(t.Rows) {
		t.Selected = len(t.Rows) - 1
	}
	if t.Selected < 0 {
		t.Selected = 0
	}
	t.ensureVisible(visible)
}
func (t *Table) ensureVisible(visible int) {
	if visible < 1 {
		visible = t.lastVisible
	}
	if visible < 1 {
		visible = 1
	}
	t.lastVisible = visible
	if t.Selected < t.offset {
		t.offset = t.Selected
	}
	if t.Selected >= t.offset+visible {
		t.offset = t.Selected - visible + 1
	}
	if t.offset < 0 {
		t.offset = 0
	}
	if len(t.Rows) > 0 && t.offset >= len(t.Rows) {
		t.offset = len(t.Rows) - 1
	}
}

func singleLine(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (t *Table) View(width, height int) string {
	if height < 1 || width < 1 {
		return ""
	}
	if len(t.Rows) == 0 {
		return clipTableCell(t.EmptyText, width)
	}
	t.ensureVisible(height)
	markerWidth := 2
	if width < markerWidth {
		markerWidth = width
	}
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = c.Width
		if widths[i] <= 0 {
			widths[i] = lipgloss.Width(singleLine(c.Title))
			for _, r := range t.Rows {
				if i < len(r.Cells) && lipgloss.Width(singleLine(r.Cells[i])) > widths[i] {
					widths[i] = lipgloss.Width(singleLine(r.Cells[i]))
				}
			}
		}
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
	remain := width - markerWidth
	for i := range widths {
		if remain < 1 {
			widths[i] = 0
			continue
		}
		if widths[i] > remain {
			widths[i] = remain
		}
		remain -= widths[i]
		if i < len(widths)-1 {
			remain -= 2
		}
	}
	var b strings.Builder
	end := t.offset + height
	if end > len(t.Rows) {
		end = len(t.Rows)
	}
	for ri := t.offset; ri < end; ri++ {
		if markerWidth == 1 {
			if ri == t.Selected {
				b.WriteString(">")
			} else {
				b.WriteString(" ")
			}
		} else if ri == t.Selected {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		for ci, c := range t.Columns {
			if ci >= len(widths) || widths[ci] == 0 {
				break
			}
			if ci > 0 {
				b.WriteString("  ")
			}
			v := ""
			r := t.Rows[ri]
			if ci < len(r.Cells) {
				v = r.Cells[ci]
			}
			v = singleLine(v)
			if t.RenderCell != nil {
				v = singleLine(t.RenderCell(r, ci, v))
			}
			v = lipgloss.NewStyle().MaxWidth(widths[ci]).Render(v)
			pad := widths[ci] - lipgloss.Width(v)
			switch c.Align {
			case AlignRight:
				b.WriteString(strings.Repeat(" ", pad) + v)
			case AlignCenter:
				left := pad / 2
				b.WriteString(strings.Repeat(" ", left) + v + strings.Repeat(" ", pad-left))
			default:
				b.WriteString(v + strings.Repeat(" ", pad))
			}
		}
		if ri < end-1 {
			b.WriteByte('\n')
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i, line := range lines {
		lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	return strings.Join(lines, "\n")
}
func clipTableCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(singleLine(s))
}
