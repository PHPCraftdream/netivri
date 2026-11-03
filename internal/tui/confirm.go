package tui

import tea "charm.land/bubbletea/v2"

type ConfirmResultMsg struct {
	Confirmed bool
	Canceled  bool
}

// Confirm asks for a yes/no decision; Enter resolves to No.
type Confirm struct{ Prompt string }

func (c *Confirm) Update(msg tea.Msg) (Modal, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, nil
	}
	var result ConfirmResultMsg
	switch k.String() {
	case "y":
		result.Confirmed = true
	case "n", "enter":
	case "esc":
		result.Canceled = true
	default:
		return c, nil
	}
	return c, func() tea.Msg { return result }
}
func (c *Confirm) View(w, h int) string { return boundLines([]string{c.Prompt + " (y/N)"}, w, h) }
func (c *Confirm) CapturingInput() bool { return true }
