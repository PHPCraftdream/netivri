package tui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestConfirmDefaultNoAndResults(t *testing.T) {
	for _, tc := range []struct {
		code        rune
		yes, cancel bool
	}{{tea.KeyEnter, false, true}, {'n', false, true}, {'y', true, false}, {tea.KeyEscape, false, true}} {
		c := &Confirm{Prompt: "Remove?"}
		_, cmd := c.Update(tuiTestKey(string(tc.code)))
		got := cmd().(ConfirmResultMsg)
		if got.Confirmed != tc.yes || got.Canceled != tc.cancel {
			t.Fatalf("key %q: %+v", tc.code, got)
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
