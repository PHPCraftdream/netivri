package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestButtonsFocus(t *testing.T) {
	b := Buttons{Labels: []string{"Save", "Cancel"}}
	b.Move(-1)
	if b.Focus != 1 {
		t.Fatalf("focus=%d", b.Focus)
	}
}
func TestButtonsFixedMarkerAndMouseBounds(t *testing.T) {
	b := Buttons{Labels: []string{"Save", "Cancel"}, Focus: -1}
	v := b.View(NewTheme())
	if !strings.Contains(v, "  [ Save ]") || !strings.Contains(v, "  [ Cancel ]") {
		t.Fatalf("missing fixed prefixes: %q", v)
	}
	b.Focus = 0
	v = b.View(NewTheme())
	if !strings.Contains(v, "› [ Save ]") {
		t.Fatalf("focused marker missing: %q", v)
	}
	i, ok := b.Update(tea.MouseClickMsg{X: lipgloss.Width("› [ Save ]") - 1, Y: 7, Button: tea.MouseLeft}, 7)
	if !ok || i != 1 {
		t.Fatalf("first click: %d %v", i, ok)
	}
	i, ok = b.Update(tea.MouseClickMsg{X: 100, Y: 7, Button: tea.MouseLeft}, 7)
	if ok || i != -1 {
		t.Fatalf("out-of-bounds click: %d %v", i, ok)
	}
}
