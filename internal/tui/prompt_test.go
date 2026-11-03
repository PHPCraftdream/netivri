package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func promptKey(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case " ":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}
func promptMsg(t *testing.T, cmd tea.Cmd) PromptResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected result command")
	}
	m, ok := cmd().(PromptResultMsg)
	if !ok {
		t.Fatalf("result type %T", cmd())
	}
	return m
}
func TestPromptKinds(t *testing.T) {
	for _, kind := range []PromptKind{TextPrompt, SecretPrompt, NumberPrompt, ChoicePrompt, MultiChoicePrompt, YesNoPrompt, ConfirmPrompt} {
		p := NewPrompt(kind, "Title", "Label", "Hint", "value")
		if kind == SecretPrompt && strings.Contains(p.View(60, 12), "value") {
			t.Fatal("secret rendered")
		}
		if p.View(60, 12) == "" {
			t.Fatal("empty view")
		}
	}
}
func TestPromptInitialChoiceSynchronizesValue(t *testing.T) {
	p := NewPrompt(ChoicePrompt, "t", "l", "h", "b")
	p.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	if p.Value != "b" {
		t.Fatal(p.Value)
	}
	v, e := p.GetAnswer()
	if e != nil || v != "b" || p.Value != "b" {
		t.Fatalf("%q %v value=%q", v, e, p.Value)
	}
}
func TestPromptValidateAndPropagateChoiceError(t *testing.T) {
	p := NewPrompt(TextPrompt, "t", "l", "h", "old")
	p.Validate = func(value string) error { return errors.New("invalid " + value) }
	_, cmd := p.Update(promptKey("enter"))
	if cmd != nil || p.Value != "old" {
		t.Fatal("invalid value accepted")
	}
	c := NewPrompt(ChoicePrompt, "t", "l", "h", "")
	_, cmd = c.Update(promptKey("enter"))
	if cmd != nil || c.Error == nil {
		t.Fatal("empty choice error ignored")
	}
}
func TestPromptChoiceRetryClearsValidationError(t *testing.T) {
	p := NewPrompt(ChoicePrompt, "t", "l", "h", "a")
	p.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	p.Validate = func(value string) error {
		if value == "a" {
			return errors.New("invalid choice")
		}
		return nil
	}

	if cmd := p.accept(); cmd != nil || p.Error == nil {
		t.Fatal("invalid choice should set validation error")
	}
	p.Update(promptKey("down"))
	_, cmd := p.Update(promptKey("enter"))
	if cmd == nil || p.Error != nil {
		t.Fatalf("valid retry should accept and clear error; cmd=%v error=%v", cmd, p.Error)
	}
	result := promptMsg(t, cmd)
	if result.Canceled || result.Value != "b" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPromptConfirmImmediateAndButtons(t *testing.T) {
	p := NewPrompt(ConfirmPrompt, "t", "Delete?", "", "false")
	if !strings.Contains(p.View(50, 8), "Cancel") || !strings.Contains(p.View(50, 8), "Remove") {
		t.Fatal("confirm buttons missing")
	}
	if m := promptMsg(t, func() tea.Cmd { _, c := p.Update(promptKey("y")); return c }()); m.Canceled {
		t.Fatal("y canceled")
	}
	p = NewPrompt(ConfirmPrompt, "t", "Delete?", "", "")
	if m := promptMsg(t, func() tea.Cmd { _, c := p.Update(promptKey("n")); return c }()); !m.Canceled {
		t.Fatal("n did not cancel")
	}
	_, cmd := p.Update(promptKey("enter"))
	if promptMsg(t, cmd).Canceled == false {
		t.Fatal("Enter must default cancel")
	}
}
func TestPromptDiscardContinueAndDiscard(t *testing.T) {
	for _, choice := range []struct {
		key    string
		cancel bool
	}{{"left", false}, {"right", true}} {
		p := NewPrompt(TextPrompt, "t", "l", "h", "old")
		p.changed = true
		p.discard = true
		p.Buttons = Buttons{Labels: []string{"Continue", "Discard"}}
		p.Update(promptKey(choice.key))
		_, cmd := p.Update(promptKey("enter"))
		if choice.cancel {
			if !promptMsg(t, cmd).Canceled {
				t.Fatal("discard not canceled")
			}
		} else if cmd != nil || p.discard {
			t.Fatal("continue did not resume")
		}
	}
}
func TestPromptMultiChoiceSpaceAndNumberBounds(t *testing.T) {
	p := NewPrompt(MultiChoicePrompt, "t", "l", "h", "")
	p.Options = []PromptOption{{Value: "a"}, {Value: "b"}}
	p.Update(promptKey(" "))
	if got, _ := p.GetAnswer(); got != "a" {
		t.Fatal(got)
	}
	min, max := 2, 5
	n := NewPrompt(NumberPrompt, "t", "n", "", "1")
	n.Min = &min
	n.Max = &max
	n.accept()
	if n.Error == nil || !strings.Contains(n.Error.Error(), "at least") {
		t.Fatal(n.Error)
	}
	n.Value = "6"
	n.accept()
	if n.Error == nil || !strings.Contains(n.Error.Error(), "at most") {
		t.Fatal(n.Error)
	}
}
func TestPromptSecretRedactionAndRevealReset(t *testing.T) {
	p := NewPrompt(SecretPrompt, "t", "url", "h", "https://alice:pw@example.test/path?token=secret")
	p.Validate = func(value string) error { return errors.New("invalid " + value) }
	p.accept()
	s := p.Error.Error()
	for _, secret := range []string{"alice", "pw", "example.test", "/path", "secret"} {
		if strings.Contains(s, secret) {
			t.Fatalf("leaked %q in %q", secret, s)
		}
	}
	p.AllowReveal = true
	p.Update(promptKey("ctrl+r"))
	if !p.reveal || !strings.Contains(p.View(80, 10), "Warning: secret visible") {
		t.Fatal("reveal warning missing")
	}
	p.Update(promptKey("esc"))
	if p.reveal {
		t.Fatal("reveal not reset")
	}
}
func TestPromptBounds(t *testing.T) {
	p := NewPrompt(TextPrompt, "title", "a long label", "hint", "x")
	for _, wh := range [][2]int{{0, 0}, {1, 1}, {2, 3}, {20, 5}} {
		s := p.View(wh[0], wh[1])
		if wh[1] == 0 && s != "" {
			t.Fatal("nonempty zero height")
		}
		if wh[1] > 0 && len(strings.Split(s, "\n")) != wh[1] {
			t.Fatalf("height %d: %q", wh[1], s)
		}
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "invalid" }
