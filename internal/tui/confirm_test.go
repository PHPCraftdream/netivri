package tui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestConfirmDefaultNoAndResults(t *testing.T) {
	for _, tc := range []struct {
		code        rune
		yes, cancel bool
	}{{tea.KeyEnter, false, false}, {'n', false, false}, {'y', true, false}, {tea.KeyEscape, false, true}} {
		c := &Confirm{Prompt: "Remove?"}
		_, cmd := c.Update(tea.KeyPressMsg{Code: tc.code})
		got := cmd().(ConfirmResultMsg)
		if got.Confirmed != tc.yes || got.Canceled != tc.cancel {
			t.Fatalf("key %q: %+v", tc.code, got)
		}
	}
}
