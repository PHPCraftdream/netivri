package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Buttons struct {
	Labels []string
	Focus  int
	Danger map[int]bool
}

func (b Buttons) segments(theme Theme) []string {
	parts := make([]string, len(b.Labels))
	for i, label := range b.Labels {
		marker := "  "
		if i == b.Focus {
			marker = theme.FocusMark() + " "
		}
		parts[i] = marker + "[ " + label + " ]"
	}
	return parts
}

func (b *Buttons) Move(delta int) {
	if len(b.Labels) == 0 {
		b.Focus = 0
		return
	}
	b.Focus = (b.Focus + delta + len(b.Labels)) % len(b.Labels)
}
func (b *Buttons) Update(msg tea.Msg, y int) (int, bool) {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		switch m.String() {
		case "left", "shift+tab":
			if len(b.Labels) == 0 {
				return -1, false
			}
			b.Move(-1)
			return -1, true
		case "right", "tab":
			if len(b.Labels) == 0 {
				return -1, false
			}
			b.Move(1)
			return -1, true
		case "enter":
			if len(b.Labels) > 0 {
				return b.Focus, true
			}
		}
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft || m.Y != y {
			return -1, false
		}
		x := 0
		for i, segment := range b.segments(NewTheme()) {
			width := lipgloss.Width(segment)
			if m.X >= x && m.X < x+width {
				b.Focus = i
				return i, true
			}
			x += width + 3
		}
	}
	return -1, false
}
func (b Buttons) View(theme Theme) string {
	parts := b.segments(theme)
	for i, s := range parts {
		if b.Danger[i] {
			parts[i] = theme.DangerButton.Render(s)
		} else if i == b.Focus {
			parts[i] = theme.FocusedButton.Render(s)
		} else {
			parts[i] = theme.Button.Render(s)
		}
	}
	return strings.Join(parts, "   ")
}
