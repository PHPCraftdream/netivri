package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestActionHintsShortAndFullUseEnabledBindings(t *testing.T) {
	bindings := ActionHints{key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")), key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove"))}
	if len(bindings.ShortHelp()) != 2 || len(bindings.FullHelp()) != 1 || len(bindings.FullHelp()[0]) != 2 {
		t.Fatal("bindings not grouped")
	}
	line := HintLine(bindings, 80)
	if !strings.Contains(line, "a add") || !strings.Contains(line, "d remove") {
		t.Fatalf("hints: %q", line)
	}
	if lipgloss.Width(HintLine(bindings, 4)) > 4 {
		t.Fatal("hint over width")
	}
	bindings[1].SetEnabled(false)
	if strings.Contains(HintLine(bindings, 80), "remove") {
		t.Fatal("disabled binding rendered")
	}
}
func TestHintASCIIPreservesLabelPositions(t *testing.T) {
	bindings := append([]key.Binding(nil), globalShortBindings...)
	unicodeTheme := Theme{ActiveTab: lipgloss.NewStyle(), UnfocusedPane: lipgloss.NewStyle()}
	asciiTheme := unicodeTheme
	asciiTheme.ASCII = true
	unicodeLine := themedHintLine(bindings, 80, unicodeTheme)
	asciiLine := themedHintLine(bindings, 80, asciiTheme)
	for _, label := range []string{"quit", "help", "focus", "tabs"} {
		unicodePos := lipgloss.Width(unicodeLine[:strings.Index(unicodeLine, label)])
		asciiPos := lipgloss.Width(asciiLine[:strings.Index(asciiLine, label)])
		if unicodePos != asciiPos {
			t.Errorf("%s starts at %d in Unicode and %d in ASCII", label, unicodePos, asciiPos)
		}
	}
	if got := globalShortBindings[3].Help().Key; got != "←/→" {
		t.Fatalf("global binding mutated: %q", got)
	}
}

func TestHintClippingUsesCells(t *testing.T) {
	if got := clipCells("界abc", 2); got != "界…" {
		t.Fatalf("got %q", got)
	}
}
func TestConfirmActions(t *testing.T) {
	for _, tc := range []struct {
		key                 tea.KeyPressMsg
		confirmed, canceled bool
	}{
		{tea.KeyPressMsg{Code: tea.KeyEnter}, false, true},
		{tea.KeyPressMsg{Code: 'y', Text: "y"}, true, false},
		{tea.KeyPressMsg{Code: 'n', Text: "n"}, false, true},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, false, true},
	} {
		c := &Confirm{Prompt: "Remove?"}
		_, cmd := c.Update(tc.key)
		if cmd == nil {
			t.Fatalf("key %v: expected a result command", tc.key)
		}
		got := cmd().(ConfirmResultMsg)
		if got.Confirmed != tc.confirmed || got.Canceled != tc.canceled {
			t.Fatalf("key %v: %+v", tc.key, got)
		}
	}
}
func TestConfirmCancelFocusedAndBounded(t *testing.T) {
	c := &Confirm{Prompt: "Remove?"}
	view := c.View(30, 5)
	if !strings.Contains(strings.Split(view, "\n")[2], "› [ Cancel ]") {
		t.Fatal("Cancel not initially focused")
	}
	c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if c.selected != 1 {
		t.Fatal("Tab did not focus Remove")
	}
	for _, line := range strings.Split(c.View(30, 5), "\n") {
		if lipgloss.Width(line) > 30 {
			t.Fatal("confirm exceeds width")
		}
	}
}
