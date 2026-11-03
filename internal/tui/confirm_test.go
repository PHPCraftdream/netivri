package tui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestConfirmDefaultNoAndResults(t *testing.T) {
	for _, tc := range []struct {
		key         tea.KeyPressMsg
		yes, cancel bool
	}{
		{tea.KeyPressMsg{Code: tea.KeyEnter}, false, true},
		{tea.KeyPressMsg{Code: 'n', Text: "n"}, false, true},
		{tea.KeyPressMsg{Code: 'y', Text: "y"}, true, false},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, false, true},
	} {
		c := &Confirm{Prompt: "Remove?"}
		_, cmd := c.Update(tc.key)
		if cmd == nil {
			t.Fatalf("key %v: expected a result command", tc.key)
		}
		got := cmd().(ConfirmResultMsg)
		if got.Confirmed != tc.yes || got.Canceled != tc.cancel {
			t.Fatalf("key %v: %+v", tc.key, got)
		}
	}
}

func TestConfirmFocusAndMouseActions(t *testing.T) {
	c := &Confirm{Prompt: "Remove?"}
	_, cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd != nil || c.selected != 1 {
		t.Fatal("Tab should focus Remove without selecting")
	}
	_, cmd = c.Update(tea.MouseClickMsg{X: 20, Y: 2, Button: tea.MouseLeft})
	if cmd == nil || !cmd().(ConfirmResultMsg).Confirmed {
		t.Fatal("Remove click should confirm")
	}
}
