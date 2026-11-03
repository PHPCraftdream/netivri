package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type CardLineKind int

const (
	CardField CardLineKind = iota
	CardAction
	CardReadOnly
)

type CardLine struct {
	ID, Label, Value, Hotkey string
	Kind                     CardLineKind
	Action, Disabled, Secret bool
}
type Card struct {
	Theme                    Theme
	Title                    string
	Info                     []string
	Lines                    []CardLine
	Selected, Scroll         int
	Focused                  bool
	width, height, lastClick int
}

func NewCard(title string, lines []CardLine) *Card {
	c := &Card{Title: title, Lines: append([]CardLine(nil), lines...), lastClick: -1}
	c.normalize()
	return c
}
func (c *Card) SetTheme(t Theme) { c.Theme = t }
func (c *Card) SetSize(w, h int) {
	w, h = max(0, w), max(0, h)
	changed := c.width != w || c.height != h
	c.width, c.height = w, h
	c.normalize()
	if changed {
		c.ensureVisible()
	} else {
		c.clampScroll()
	}
}
func (c *Card) Bindings() []key.Binding {
	b := []key.Binding{key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move")), key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move")), key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page")), key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page")), key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first")), key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "activate"))}
	for _, l := range c.Lines {
		if l.Hotkey != "" && c.eligibleLine(l) {
			b = append(b, key.NewBinding(key.WithKeys(l.Hotkey), key.WithHelp(l.Hotkey, "activate "+l.Label)))
		}
	}
	return b
}
func (c *Card) eligibleLine(l CardLine) bool { return !l.Disabled && l.Kind != CardReadOnly }
func (c *Card) eligible(i int) bool          { return i >= 0 && i < len(c.Lines) && c.eligibleLine(c.Lines[i]) }
func (c *Card) normalize() {
	if c.eligible(c.Selected) {
		return
	}
	for i := range c.Lines {
		if c.eligible(i) {
			c.Selected = i
			return
		}
	}
	c.Selected = 0
}
func (c *Card) next(from, d int) int {
	for i := from + d; i >= 0 && i < len(c.Lines); i += d {
		if c.eligible(i) {
			return i
		}
	}
	return from
}
func (c *Card) visibleRows() int { return max(0, c.height-2) }
func (c *Card) contentRows() int { return boolInt(c.Title != "") + len(c.Info) + len(c.Lines) }
func (c *Card) selectedRow() int { return boolInt(c.Title != "") + len(c.Info) + c.Selected }
func (c *Card) clampScroll() {
	c.Scroll = max(0, min(c.Scroll, max(0, c.contentRows()-c.visibleRows())))
}
func (c *Card) ensureVisible() {
	n := c.visibleRows()
	if n <= 0 {
		c.Scroll = 0
		return
	}
	selected := c.selectedRow()
	if c.Focused {
		if selected < c.Scroll {
			c.Scroll = selected
		}
		if selected >= c.Scroll+n {
			c.Scroll = selected - n + 1
		}
	}
	c.Scroll = max(0, min(c.Scroll, max(0, c.contentRows()-n)))
}
func (c *Card) Update(msg tea.Msg) bool {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		s := m.String()
		for i, l := range c.Lines {
			if l.Hotkey != "" && s == l.Hotkey && c.eligible(i) {
				c.Selected = i
				c.ensureVisible()
				return true
			}
		}
		switch s {
		case "up", "k":
			c.Selected = c.next(c.Selected, -1)
		case "down", "j":
			c.Selected = c.next(c.Selected, 1)
		case "pgup":
			for i := 0; i < max(1, c.visibleRows()); i++ {
				c.Selected = c.next(c.Selected, -1)
			}
		case "pgdown":
			for i := 0; i < max(1, c.visibleRows()); i++ {
				c.Selected = c.next(c.Selected, 1)
			}
		case "home":
			for i := range c.Lines {
				if c.eligible(i) {
					c.Selected = i
					break
				}
			}
		case "end":
			for i := len(c.Lines) - 1; i >= 0; i-- {
				if c.eligible(i) {
					c.Selected = i
					break
				}
			}
		case "enter":
			return c.eligible(c.Selected)
		default:
			return false
		}
		c.ensureVisible()
	case tea.MouseWheelMsg:
		mouse := tea.Mouse(m)
		switch mouse.Button {
		case tea.MouseWheelUp:
			c.Scroll = max(0, c.Scroll-1)
		case tea.MouseWheelDown:
			c.Scroll = min(max(0, c.contentRows()-c.visibleRows()), c.Scroll+1)
		default:
			return false
		}
		return false
	case tea.MouseClickMsg:
		mouse := tea.Mouse(m)
		if mouse.Button != tea.MouseLeft {
			return false
		}
		if mouse.X < 1 || mouse.X >= c.width-1 || mouse.Y < 1 || mouse.Y >= c.height-1 {
			return false
		}
		absolute := c.Scroll + mouse.Y - 1
		idx := absolute - boolInt(c.Title != "") - len(c.Info)
		if idx < 0 || idx >= len(c.Lines) {
			return false
		}
		if idx == c.Selected && idx == c.lastClick {
			c.lastClick = -1
			return c.eligible(idx)
		}
		c.lastClick = idx
		if c.eligible(idx) {
			c.Selected = idx
			c.ensureVisible()
			if c.Lines[idx].Kind != CardField {
				return false
			}
			start, end := c.editRange(c.Lines[idx])
			return mouse.X >= start && mouse.X < end
		}
		return false
	}
	return false
}
func (c *Card) View(w, h int) string {
	c.SetSize(w, h)
	if w < 2 || h < 2 {
		if w <= 0 || h <= 0 {
			return ""
		}
		return strings.Repeat("\n", max(0, h-1)) + strings.Repeat(" ", max(0, w))
	}
	t := c.Theme
	if t.Accent == nil {
		t = NewTheme()
	}
	body := make([]string, 0, max(0, h-2))
	entries := make([]string, 0, c.contentRows())
	if c.Title != "" {
		entries = append(entries, c.Title)
	}
	entries = append(entries, c.Info...)
	for i, l := range c.Lines {
		mark := " "
		selected := c.Focused && i == c.Selected
		if selected {
			mark = t.FocusMark()
		}
		v := l.Value
		if l.Secret && v != "" {
			v = "********"
		}
		s := mark + " " + l.Label
		if v != "" {
			s += "  " + v
		}
		if l.Kind == CardField {
			s += "  [edit]"
		}
		if l.Hotkey != "" {
			s += "  [" + l.Hotkey + "]"
		}
		line := clipCardCell(s, max(0, w-2), t)
		if l.Disabled || l.Kind == CardReadOnly {
			line = lipgloss.NewStyle().Faint(true).Render(line)
		}
		if selected {
			line = t.SelectedRow.Render(line)
		}
		entries = append(entries, line)
	}
	for i := c.Scroll; i < len(entries) && len(body) < h-2; i++ {
		body = append(body, clipCardCell(entries[i], max(0, w-2), t))
	}
	b := lipgloss.RoundedBorder()
	if t.ASCII {
		b = lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"}
	}
	inner := max(0, w-2)
	lines := make([]string, 0, h)
	lines = append(lines, b.TopLeft+strings.Repeat(b.Top, inner)+b.TopRight)
	for i := 0; i < h-2; i++ {
		cell := ""
		if i < len(body) {
			cell = body[i]
		}
		cell += strings.Repeat(" ", max(0, inner-lipgloss.Width(cell)))
		lines = append(lines, b.Left+cell+b.Right)
	}
	lines = append(lines, b.BottomLeft+strings.Repeat(b.Bottom, inner)+b.BottomRight)
	if !t.NoColor {
		border := t.Muted
		if c.Focused {
			border = t.Accent
		}
		styled := lipgloss.NewStyle().Foreground(border).Render
		lines[0] = styled(lines[0])
		lines[len(lines)-1] = styled(lines[len(lines)-1])
	}
	return strings.Join(lines, "\n")
}

func (c *Card) editRange(l CardLine) (int, int) {
	value := l.Value
	if l.Secret && value != "" {
		value = "********"
	}
	prefix := "  " + l.Label
	if value != "" {
		prefix += "  " + value
	}
	start := 1 + ansi.StringWidth(prefix) + 2
	return start, start + ansi.StringWidth("[edit]")
}

func clipCardCell(s string, width int, t Theme) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(singleLine(s), width, t.Ellipsis())
}
