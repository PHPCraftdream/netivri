package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"strings"
)

type Workspace struct {
	Theme         Theme
	List          *List
	Card          *Card
	FocusedPane   int
	Drilled       bool
	Width, Height int
}

const (
	PaneList = iota
	PaneCard
)

func NewWorkspace(list *List, card *Card) *Workspace {
	w := &Workspace{List: list, Card: card, FocusedPane: PaneList}
	w.syncFocus()
	return w
}
func (w *Workspace) SetTheme(t Theme) {
	w.Theme = t
	if w.List != nil {
		w.List.SetTheme(t)
	}
	if w.Card != nil {
		w.Card.SetTheme(t)
	}
}
func (w *Workspace) syncFocus() {
	if w.List != nil {
		w.List.Focused = w.FocusedPane == PaneList
	}
	if w.Card != nil {
		w.Card.Focused = w.FocusedPane == PaneCard
	}
}
func (w *Workspace) SetFocus(p int) {
	if p == PaneList || p == PaneCard {
		w.FocusedPane = p
		w.syncFocus()
	}
}
func (w *Workspace) Focus() int { return w.FocusedPane }
func (w *Workspace) SetPaneFocus(p int) {
	w.SetFocus(p)
}
func (w *Workspace) CycleFocus(d int) {
	if d == 0 {
		return
	}
	if w.Width < 100 {
		if w.Drilled {
			w.SetFocus(PaneCard)
		} else {
			w.SetFocus(PaneList)
		}
		return
	}
	if d > 0 {
		w.SetFocus(PaneCard)
	} else {
		w.SetFocus(PaneList)
	}
}
func (w *Workspace) FocusPane(reverse bool) bool {
	if w.Width < 100 {
		return false
	}
	before := w.FocusedPane
	w.CycleFocus(map[bool]int{true: -1, false: 1}[reverse])
	return before != w.FocusedPane
}
func (w *Workspace) SetSize(width, height int) {
	w.Width = max(0, width)
	w.Height = max(0, height)
	if width >= 100 {
		left := width * 35 / 100
		if w.List != nil {
			w.List.SetSize(left, height)
		}
		if w.Card != nil {
			w.Card.SetSize(max(0, width-left-1), height)
		}
	} else {
		if w.List != nil {
			w.List.SetSize(width, height)
		}
		if w.Card != nil {
			w.Card.SetSize(width, height)
		}
	}
	if width < 100 && w.Drilled {
		w.SetFocus(PaneCard)
	}
	w.syncFocus()
}
func (w *Workspace) Bindings() []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")), key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous pane")), key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open / activate"))}
}
func (w *Workspace) Update(msg tea.Msg) bool {
	if w.Width == 0 {
		w.SetSize(w.Width, w.Height)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if ret, handled := w.handleKey(k, msg); handled {
			return ret
		}
	}
	if w.Width >= 100 {
		return w.updateWide(msg)
	}
	if w.Drilled {
		return w.Card != nil && w.Card.Update(msg)
	}
	return w.updateNarrowList(msg)
}

// handleKey returns the result and whether the key was consumed; unconsumed keys fall through to pane routing.
func (w *Workspace) handleKey(k tea.KeyPressMsg, msg tea.Msg) (bool, bool) {
	if w.List != nil && w.List.Filtering && (k.String() == "esc" || k.String() == "enter") {
		// While the filter is active, esc clears the filter and enter
		// keeps it; neither may drill back or drill in.
		return w.List.Update(msg), true
	}
	switch k.String() {
	case "tab":
		w.cycleFocusWide(1)
		return false, true
	case "shift+tab":
		w.cycleFocusWide(-1)
		return false, true
	case "esc":
		if w.Drilled || w.FocusedPane == PaneCard {
			w.Drilled = false
			w.SetFocus(PaneList)
		}
		return false, true
	case "enter":
		return w.handleEnter()
	}
	return false, false
}
func (w *Workspace) cycleFocusWide(delta int) {
	if w.Width >= 100 {
		w.CycleFocus(delta)
	}
}
func (w *Workspace) handleEnter() (bool, bool) {
	if w.Width < 100 && !w.Drilled {
		if w.List != nil && w.List.IsAdd() {
			return true, true
		}
		if w.List == nil || w.List.Selected >= len(w.List.Rows) {
			return false, true
		}
		w.Drilled = true
		w.SetFocus(PaneCard)
		return false, true
	}
	if w.Width >= 100 && w.FocusedPane == PaneList {
		if w.List != nil && w.List.IsAdd() {
			return true, true
		}
		if w.List != nil && w.List.Selected < len(w.List.Rows) {
			w.SetFocus(PaneCard)
		}
		return false, true
	}
	return false, false
}

// updateWide routes input in the two-pane layout (width >= 100).
func (w *Workspace) updateWide(msg tea.Msg) bool {
	switch m := msg.(type) {
	case tea.MouseClickMsg:
		return w.updateWideMouse(tea.Mouse(m), func(p tea.Mouse) tea.Msg { return tea.MouseClickMsg(p) })
	case tea.MouseWheelMsg:
		return w.updateWideMouse(tea.Mouse(m), func(p tea.Mouse) tea.Msg { return tea.MouseWheelMsg(p) })
	}
	if w.FocusedPane == PaneCard {
		return w.Card != nil && w.Card.Update(msg)
	}
	return w.List != nil && w.List.Update(msg)
}

// updateWideMouse sends a mouse event to the pane under it; the card starts right of the gutter at 35% width.
func (w *Workspace) updateWideMouse(p tea.Mouse, wrap func(tea.Mouse) tea.Msg) bool {
	left := w.Width * 35 / 100
	if p.X < 0 || p.X >= w.Width || p.Y < 0 || p.Y >= w.Height || p.X == left {
		return false
	}
	if p.X > left {
		w.SetFocus(PaneCard)
		p.X -= left + 1
		return w.Card != nil && w.Card.Update(wrap(p))
	}
	w.SetFocus(PaneList)
	return w.List != nil && w.List.Update(wrap(p))
}

// updateNarrowList routes input in the single-pane layout (width < 100) while the list is shown.
func (w *Workspace) updateNarrowList(msg tea.Msg) bool {
	if m, ok := msg.(tea.MouseClickMsg); ok && w.List != nil && tea.Mouse(m).Button == tea.MouseLeft {
		w.drillOnListClick(msg)
		return false
	}
	return w.List != nil && w.List.Update(msg)
}

// drillOnListClick drills into the card when a click lands on the already selected row.
func (w *Workspace) drillOnListClick(msg tea.Msg) {
	before := w.List.Selected
	w.List.Update(msg)
	if before == w.List.Selected && w.List.Selected < len(w.List.Rows) && !w.List.IsAdd() {
		w.Drilled = true
		w.SetFocus(PaneCard)
	}
}
func (w *Workspace) View(width, height int) string {
	w.SetSize(width, height)
	if width <= 0 || height <= 0 {
		return ""
	}
	if width < 100 {
		if w.Drilled && w.Card != nil {
			return w.Card.View(width, height)
		}
		if w.List != nil {
			return w.List.View(width, height)
		}
		return ""
	}
	left := width * 35 / 100
	if left < 1 {
		left = 1
	}
	right := max(0, width-left-1)
	l, r := "", ""
	if w.List != nil {
		l = w.List.View(left, height)
	}
	if w.Card != nil {
		r = w.Card.View(right, height)
	}
	ll, rr := strings.Split(l, "\n"), strings.Split(r, "\n")
	out := make([]string, height)
	for i := range out {
		a, b := "", ""
		if i < len(ll) {
			a = ll[i]
		}
		if i < len(rr) {
			b = rr[i]
		}
		a = lipgloss.NewStyle().Width(left).MaxWidth(left).Render(a)
		b = lipgloss.NewStyle().Width(right).MaxWidth(right).Render(b)
		out[i] = a + " " + b
	}
	return strings.Join(out, "\n")
}
