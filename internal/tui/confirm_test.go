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
		assertConfirmKey(t, tc.key, tc.yes, tc.cancel)
	}
}

// assertConfirmKey checks that key on a fresh Confirm yields a ConfirmResultMsg with the given outcome.
func assertConfirmKey(t *testing.T, key tea.KeyPressMsg, confirmed, canceled bool) {
	t.Helper()
	c := &Confirm{Prompt: "Remove?"}
	_, cmd := c.Update(key)
	if cmd == nil {
		t.Fatalf("key %v: expected a result command", key)
	}
	got, ok := cmd().(ConfirmResultMsg)
	if !ok {
		t.Fatalf("key %v: result is %T, want ConfirmResultMsg", key, cmd())
	}
	if got.Confirmed != confirmed || got.Canceled != canceled {
		t.Fatalf("key %v: %+v", key, got)
	}
}

func TestConfirmFocusAndMouseActions(t *testing.T) {
	c := &Confirm{Prompt: "Remove?"}
	_, cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd != nil || c.selected != 1 {
		t.Fatal("Tab should focus Remove without selecting")
	}
	_, cmd = c.Update(tea.MouseClickMsg{X: 20, Y: 2, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Remove click should confirm")
	}
	if got, ok := cmd().(ConfirmResultMsg); !ok || !got.Confirmed {
		t.Fatal("Remove click should confirm")
	}
}
