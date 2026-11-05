package tui

import (
	tea "charm.land/bubbletea/v2"
)

type ConfirmResultMsg struct {
	Confirmed bool
	Canceled  bool
}

// Confirm asks for a decision. Cancel is initially focused; Enter cancels.
type Confirm struct {
	Prompt string
	// ConfirmLabel is the text of the confirming button; empty reads Remove.
	ConfirmLabel string
	Theme        Theme
	selected     int
}

func (c *Confirm) SetTheme(t Theme) { c.Theme = t }

func (c *Confirm) Update(msg tea.Msg) (Modal, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		switch m.String() {
		case "left", "right", "tab", "shift+tab":
			c.selected = 1 - c.selected
		case "y", "Y":
			return c, confirmResult(true, false)
		case "n", "N", "esc":
			return c, confirmResult(false, true)
		case "enter":
			return c, confirmResult(c.selected == 1, c.selected == 0)
		}
	case tea.MouseClickMsg:
		if m.Button == tea.MouseLeft && m.Y == 2 {
			b := c.buttons()
			if i, ok := b.Update(msg, 2); ok {
				return c, confirmResult(i == 1, i == 0)
			}
		}
	}
	return c, nil
}
func confirmResult(confirmed, canceled bool) tea.Cmd {
	return func() tea.Msg { return ConfirmResultMsg{Confirmed: confirmed, Canceled: canceled} }
}
func (c *Confirm) buttons() Buttons {
	label := c.ConfirmLabel
	if label == "" {
		label = "Remove"
	}
	return Buttons{Labels: []string{"Cancel", label}, Focus: c.selected, Danger: map[int]bool{1: true}}
}
func (c *Confirm) View(w, h int) string {
	t := c.Theme
	if t.Accent == nil {
		t = NewTheme()
	}
	lines := []string{c.Prompt, "", c.buttons().View(t), "←/→ or Tab focus · Enter select · y confirm · n/Esc cancel"}
	if t.ASCII {
		lines[3] = "Left/Right or Tab focus - Enter select - y confirm - n/Esc cancel"
	}
	return boundLines(lines, w, h)
}
func (c *Confirm) CapturingInput() bool { return true }
