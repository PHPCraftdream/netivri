package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type ListColumn struct {
	Title          string
	Width          int
	Align          Alignment
	Min, Max, Flex int
}
type List struct {
	Theme                            Theme
	Columns                          []ListColumn
	Rows                             []Row
	Title, EmptyText, AddLabel       string
	Selected                         int
	Focused                          bool
	width, height, offset, lastClick int
}

func NewList(columns []ListColumn, rows []Row) *List {
	l := &List{Columns: append([]ListColumn(nil), columns...), EmptyText: "No items", AddLabel: "+ Add", lastClick: -1}
	l.SetRows(rows)
	return l
}
func (l *List) SetTheme(t Theme) { l.Theme = t }
func (l *List) SetRows(rows []Row) {
	l.Rows = append([]Row(nil), rows...)
	n := l.count() - 1
	l.Selected = max(0, min(n, l.Selected))
	l.ensureVisible()
}
func (l *List) SelectedRow() (Row, bool) {
	if l.Selected < 0 || l.Selected >= len(l.Rows) {
		return Row{}, false
	}
	return l.Rows[l.Selected], true
}
func (l *List) IsAdd() bool      { return l.AddLabel != "" && l.Selected == len(l.Rows) }
func (l *List) SetSize(w, h int) { l.width = max(0, w); l.height = max(0, h); l.ensureVisible() }
func (l *List) Bindings() []key.Binding {
	b := []key.Binding{key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move")), key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move")), key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page")), key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page")), key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first")), key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last"))}
	if l.AddLabel != "" {
		b = append(b, key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")))
	}
	b = append(b, key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")))
	return b
}
func (l *List) count() int {
	n := len(l.Rows)
	if l.AddLabel != "" {
		n++
	}
	return n
}
func (l *List) geometry() (int, int, int) {
	inner := max(0, l.width-2)
	title := 0
	if l.Title != "" {
		title = 1
	}
	header := 0
	if len(l.Columns) > 0 {
		header = 1
	}
	empty := 0
	if len(l.Rows) == 0 {
		empty = 1
	}
	status := 1
	visible := max(0, l.height-2-title-header-status-empty)
	return inner, title + header + empty, visible
}
func (l *List) ensureVisible() {
	_, _, v := l.geometry()
	if v < 1 {
		l.offset = 0
		return
	}
	if l.Selected < l.offset {
		l.offset = l.Selected
	}
	if l.Selected >= l.offset+v {
		l.offset = l.Selected - v + 1
	}
	maxoff := max(0, l.count()-v)
	l.offset = max(0, min(maxoff, l.offset))
}
func (l *List) Update(msg tea.Msg) bool {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		b := l.Bindings()
		switch {
		case key.Matches(m, b[0]):
			l.Selected--
		case key.Matches(m, b[1]):
			l.Selected++
		case key.Matches(m, b[2]):
			_, _, v := l.geometry()
			l.Selected -= max(1, v)
		case key.Matches(m, b[3]):
			_, _, v := l.geometry()
			l.Selected += max(1, v)
		case key.Matches(m, b[4]):
			l.Selected = 0
		case key.Matches(m, b[5]):
			l.Selected = l.count() - 1
		case l.AddLabel != "" && len(b) > 7 && key.Matches(m, b[6]):
			l.Selected = len(l.Rows)
		case key.Matches(m, b[len(b)-1]):
			return l.Selected >= 0 && l.Selected < l.count()
		default:
			return false
		}
		l.Selected = max(0, min(max(0, l.count()-1), l.Selected))
		l.ensureVisible()
		return false
	case tea.MouseWheelMsg:
		mouse := tea.Mouse(m)
		if mouse.Button == tea.MouseWheelUp {
			l.Selected--
		} else if mouse.Button == tea.MouseWheelDown {
			l.Selected++
		} else {
			return false
		}
		l.Selected = max(0, min(max(0, l.count()-1), l.Selected))
		l.ensureVisible()
		return false
	case tea.MouseClickMsg:
		mouse := tea.Mouse(m)
		if mouse.Button != tea.MouseLeft {
			return false
		}
		_, start, v := l.geometry()
		idx := mouse.Y - 1 - start + l.offset
		if mouse.Y < 1+start || idx < 0 || idx >= l.count() || idx >= l.offset+v {
			return false
		}
		if idx == l.Selected && idx == l.lastClick {
			l.lastClick = -1
			return true
		}
		l.Selected = idx
		l.lastClick = idx
		l.ensureVisible()
		return false
	}
	return false
}
func (l *List) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	l.SetSize(w, h)
	if w < 2 || h < 2 {
		return boundLines([]string{clipTableCell("|", w)}, w, h)
	}
	t := l.Theme
	if t.Accent == nil {
		t = NewTheme()
	}
	border := lipgloss.RoundedBorder()
	if t.ASCII {
		border = lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+", Middle: "+", MiddleLeft: "+", MiddleRight: "+", MiddleTop: "+", MiddleBottom: "+"}
	}
	inner := max(0, w-2)
	_, _, v := l.geometry()
	scrolling := l.count() > v && v > 0
	contentW := inner
	if scrolling {
		contentW = max(0, inner-1)
	}
	content := make([]string, max(0, h-2))
	put := func(y int, s string) {
		if y >= 0 && y < len(content) {
			width := inner
			if scrolling {
				width = contentW
			}
			content[y] = clipTableCell(s, width)
		}
	}
	y := 0
	if l.Title != "" {
		put(y, l.Title)
		y++
	}
	widths := l.columnWidths(max(0, contentW-3))
	if len(l.Columns) > 0 {
		put(y, "  "+l.renderCells(-1, widths))
		y++
	}
	if len(l.Rows) == 0 {
		put(y, l.EmptyText)
		y++
	}
	for n := 0; n < v && l.offset+n < l.count(); n++ {
		i := l.offset + n
		mark := " "
		if i == l.Selected {
			mark = t.FocusMark()
		}
		text := l.AddLabel
		if i < len(l.Rows) {
			text = l.renderCells(i, widths)
		}
		row := mark + " " + text
		if i == l.Selected && l.Focused && i < len(l.Rows) {
			row = t.SelectedRow.Render(row)
		}
		if scrolling {
			a, b := "│", "█"
			if t.ASCII {
				a, b = "|", "#"
			}
			g := a
			if n == min(v-1, l.offset*v/max(1, l.count()-v+1)) {
				g = b
			}
			row = clipTableCell(row, contentW)
			row += strings.Repeat(" ", max(0, contentW-lipgloss.Width(row)))
			row += g
			content[y+n] = row
			continue
		}
		put(y+n, row)
	}
	status := ""
	if l.count() > 0 {
		status = fmtPosition(l.Selected+1, l.count())
	}
	put(len(content)-1, status)
	lines := make([]string, h)
	lines[0] = border.TopLeft + strings.Repeat(border.Top, inner) + border.TopRight
	for i := 1; i < h-1; i++ {
		lines[i] = border.Left + content[i-1] + strings.Repeat(" ", max(0, inner-lipgloss.Width(content[i-1]))) + border.Right
	}
	if h > 1 {
		lines[h-1] = border.BottomLeft + strings.Repeat(border.Bottom, inner) + border.BottomRight
	}
	return strings.Join(lines, "\n")
}
func (l *List) columnWidths(av int) []int {
	w := make([]int, len(l.Columns))
	sum := 0
	flex := 0
	for i, c := range l.Columns {
		n := c.Width
		if n <= 0 {
			n = max(c.Min, lipgloss.Width(c.Title))
			for _, r := range l.Rows {
				if i < len(r.Cells) {
					n = max(n, lipgloss.Width(singleLine(r.Cells[i])))
				}
			}
		}
		n = max(n, c.Min)
		if c.Max > 0 {
			n = min(n, c.Max)
		}
		w[i] = n
		sum += n
		if i > 0 {
			sum += 2
		}
		flex += max(0, c.Flex)
	}
	for sum > av {
		changed := false
		for i := len(w) - 1; i >= 0 && sum > av; i-- {
			minw := max(0, l.Columns[i].Min)
			if w[i] > minw {
				w[i]--
				sum--
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	extra := max(0, av-sum)
	for i, c := range l.Columns {
		if extra == 0 {
			break
		}
		if c.Flex > 0 {
			d := min(extra, max(1, extra*c.Flex/max(1, flex)))
			if c.Max > 0 {
				d = min(d, max(0, c.Max-w[i]))
			}
			w[i] += d
			extra -= d
		}
	}
	return w
}
func (l *List) renderCells(n int, widths []int) string {
	p := make([]string, len(widths))
	for i, c := range l.Columns {
		if i >= len(widths) {
			break
		}
		v := c.Title
		if n >= 0 {
			v = ""
			if i < len(l.Rows[n].Cells) {
				v = singleLine(l.Rows[n].Cells[i])
			}
		}
		v = clipTableCell(v, widths[i])
		if c.Align == AlignRight {
			v = strings.Repeat(" ", max(0, widths[i]-lipgloss.Width(v))) + v
		} else if c.Align == AlignCenter {
			pad := max(0, widths[i]-lipgloss.Width(v))
			v = strings.Repeat(" ", pad/2) + v + strings.Repeat(" ", pad-pad/2)
		} else {
			v += strings.Repeat(" ", max(0, widths[i]-lipgloss.Width(v)))
		}
		p[i] = v
	}
	return strings.Join(p, "  ")
}
func fmtPosition(i, n int) string { return strconv.Itoa(i) + "/" + strconv.Itoa(n) }
