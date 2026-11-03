package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// promptButtonX computes the X coordinate of the middle of button segment
// index, exactly as Buttons.Update hit-tests them (segments use NewTheme).
func promptButtonX(t *testing.T, labels []string, index int) int {
	t.Helper()
	b := Buttons{Labels: labels}
	x := 0
	for i, seg := range b.segments(NewTheme()) {
		if i == index {
			return x + 2
		}
		x += lipgloss.Width(seg) + 3
	}
	t.Fatalf("button index %d out of range for labels %v", index, labels)
	return -1
}

func promptClick(t *testing.T, p *Prompt, w, h int, labels []string, index int) tea.Cmd {
	t.Helper()
	p.View(w, h) // render first so p.buttonY is set
	x := promptButtonX(t, labels, index)
	_, cmd := p.Update(tea.MouseClickMsg{X: x, Y: p.buttonY, Button: tea.MouseLeft})
	return cmd
}

func TestPromptMouseClickButtonRows(t *testing.T) {
	t.Run("save accepts value", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "hello")
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 0)
		m := promptMsg(t, cmd)
		if m.Canceled || m.Value != "hello" {
			t.Fatalf("save click: got %+v, want Value=hello not canceled", m)
		}
	})
	t.Run("cancel emits canceled result", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "hello")
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 1)
		m := promptMsg(t, cmd)
		if !m.Canceled || m.Value != "" {
			t.Fatalf("cancel click: got %+v, want canceled empty value", m)
		}
	})
	t.Run("confirm remove via mouse click", func(t *testing.T) {
		p := NewPrompt(ConfirmPrompt, "T", "Delete?", "H", "")
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 1)
		m := promptMsg(t, cmd)
		if m.Canceled {
			t.Fatalf("confirm click canceled: %+v", m)
		}
	})
	t.Run("confirm cancel via mouse click on index 0", func(t *testing.T) {
		p := NewPrompt(ConfirmPrompt, "T", "Delete?", "H", "")
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 0)
		m := promptMsg(t, cmd)
		if !m.Canceled {
			t.Fatalf("cancel-index click not canceled: %+v", m)
		}
	})
}

func TestPromptMouseClickChoiceRows(t *testing.T) {
	opts := []PromptOption{
		{Label: "A", Value: "a"},
		{Label: "B", Value: "b"},
		{Label: "C", Value: "c"},
	}
	t.Run("click sets choice", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.View(80, 20) // rows start at Y=3
		_, cmd := p.Update(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("row click produced cmd %v", cmd)
		}
		if p.choice != 1 {
			t.Fatalf("choice = %d, want 1", p.choice)
		}
	})
	t.Run("click out of range row keeps choice", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.View(80, 20)
		p.Update(tea.MouseClickMsg{X: 2, Y: 10, Button: tea.MouseLeft})
		if p.choice != 0 {
			t.Fatalf("choice = %d, want 0 after out-of-range click", p.choice)
		}
	})
	t.Run("click toggles multi choice selection", func(t *testing.T) {
		p := NewPrompt(MultiChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.View(80, 20)
		p.Update(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
		if len(p.Selected) != 1 || p.Selected[0] != "a" {
			t.Fatalf("Selected = %v, want [a]", p.Selected)
		}
	})
	t.Run("non-left mouse button is ignored", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.View(80, 20)
		_, cmd := p.Update(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseRight})
		if cmd != nil || p.choice != 0 {
			t.Fatalf("right click changed state: choice=%d cmd=%v", p.choice, cmd)
		}
	})
}

func TestPromptDiscardFlowMouseAndKeys(t *testing.T) {
	enterDiscard := func(t *testing.T) *Prompt {
		t.Helper()
		p := NewPrompt(TextPrompt, "T", "L", "H", "")
		p.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		_, cmd := p.Update(promptKey("esc"))
		if cmd != nil || !p.discard {
			t.Fatalf("esc with changes: discard=%v cmd=%v", p.discard, cmd)
		}
		if got := p.Buttons.Labels; len(got) != 2 || got[0] != "Continue" || got[1] != "Discard" {
			t.Fatalf("discard labels = %v", got)
		}
		return p
	}
	t.Run("discard button click emits canceled result", func(t *testing.T) {
		p := enterDiscard(t)
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 1)
		m := promptMsg(t, cmd)
		if !m.Canceled {
			t.Fatalf("discard click not canceled: %+v", m)
		}
	})
	t.Run("continue button click resumes", func(t *testing.T) {
		p := enterDiscard(t)
		cmd := promptClick(t, p, 80, 20, p.Buttons.Labels, 0)
		if cmd != nil {
			t.Fatalf("continue click produced cmd %v", cmd)
		}
		if p.discard {
			t.Fatal("still in discard mode after Continue click")
		}
		if got := p.Buttons.Labels; len(got) != 2 || got[0] != "Save" || got[1] != "Cancel" {
			t.Fatalf("labels after resume = %v", got)
		}
	})
	t.Run("esc resumes editing", func(t *testing.T) {
		p := enterDiscard(t)
		_, cmd := p.Update(promptKey("esc"))
		if cmd != nil || p.discard {
			t.Fatalf("esc in discard: discard=%v cmd=%v", p.discard, cmd)
		}
	})
	t.Run("tab and shift+tab move discard focus", func(t *testing.T) {
		p := enterDiscard(t)
		p.Update(promptKey("tab"))
		if p.Buttons.Focus != 1 {
			t.Fatalf("focus after tab = %d, want 1", p.Buttons.Focus)
		}
		p.Update(promptKey("shift+tab"))
		if p.Buttons.Focus != 0 {
			t.Fatalf("focus after shift+tab = %d, want 0", p.Buttons.Focus)
		}
	})
	t.Run("left moves discard focus to continue", func(t *testing.T) {
		p := enterDiscard(t)
		p.Update(promptKey("tab"))
		p.Update(promptKey("left"))
		if p.Buttons.Focus != 0 {
			t.Fatalf("focus after left = %d, want 0", p.Buttons.Focus)
		}
	})
	t.Run("enter on discard emits canceled result", func(t *testing.T) {
		p := enterDiscard(t)
		p.Update(promptKey("tab"))
		_, cmd := p.Update(promptKey("enter"))
		if !promptMsg(t, cmd).Canceled {
			t.Fatal("enter on Discard did not cancel")
		}
	})
	t.Run("view shows discard question", func(t *testing.T) {
		p := enterDiscard(t)
		if v := p.View(80, 20); !strings.Contains(v, "Discard changes?") {
			t.Fatalf("view missing discard question: %q", v)
		}
	})
}

func TestPromptTextTypingAndSavingGuard(t *testing.T) {
	t.Run("typing updates value and changed flag", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "")
		_, cmd := p.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		if p.Value != "a" || !p.changed {
			t.Fatalf("Value=%q changed=%v (cmd=%v)", p.Value, p.changed, cmd)
		}
	})
	t.Run("saving guard ignores keys", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "keep")
		p.SetSaving(true)
		_, cmd := p.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
		if cmd != nil || p.Value != "keep" {
			t.Fatalf("saving guard: Value=%q cmd=%v", p.Value, cmd)
		}
	})
	t.Run("non-key non-mouse message is ignored", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		_, cmd := p.Update(tea.MouseWheelMsg{})
		if cmd != nil {
			t.Fatalf("wheel message produced cmd %v", cmd)
		}
	})
	t.Run("window size message stored", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		_, cmd := p.Update(tea.WindowSizeMsg{Width: 70, Height: 15})
		if cmd != nil || p.width != 70 || p.height != 15 {
			t.Fatalf("size msg: w=%d h=%d cmd=%v", p.width, p.height, cmd)
		}
	})
	t.Run("initialize syncs input to changed Value", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "")
		p.initialized = false
		p.Value = "synced"
		p.initialize()
		if p.input.Value() != "synced" {
			t.Fatalf("input value = %q, want synced", p.input.Value())
		}
	})
	t.Run("multi choice initialize joins selected", func(t *testing.T) {
		p := NewPrompt(MultiChoicePrompt, "T", "L", "H", "")
		p.Selected = []string{"a", "b"}
		p.initialize()
		if p.Value != "a,b" {
			t.Fatalf("Value = %q, want a,b", p.Value)
		}
	})
}

func TestPromptSecretRevealToggle(t *testing.T) {
	p := NewPrompt(SecretPrompt, "T", "pw", "H", "hunter2")
	t.Run("ctrl+r without AllowReveal does nothing", func(t *testing.T) {
		p.Update(promptKey("ctrl+r"))
		if p.reveal {
			t.Fatal("reveal enabled without AllowReveal")
		}
	})
	t.Run("ctrl+r reveals and shows warning", func(t *testing.T) {
		p.AllowReveal = true
		p.Update(promptKey("ctrl+r"))
		if !p.reveal {
			t.Fatal("reveal not toggled on")
		}
		if v := p.View(60, 10); !strings.Contains(v, "Warning: secret visible") {
			t.Fatalf("view missing reveal warning: %q", v)
		}
	})
	t.Run("ctrl+r hides again", func(t *testing.T) {
		p.Update(promptKey("ctrl+r"))
		if p.reveal {
			t.Fatal("reveal not toggled off")
		}
		if v := p.View(60, 10); strings.Contains(v, "Warning: secret visible") {
			t.Fatalf("warning still shown after hide: %q", v)
		}
	})
}

func TestPromptChoiceNavigation(t *testing.T) {
	opts := []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}, {Label: "C", Value: "c"}}
	t.Run("down wraps at end", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.initialize() // the first Update would reset the choice from Value
		p.choice = 2
		p.Update(promptKey("down"))
		if p.choice != 0 {
			t.Fatalf("choice after down-wrap = %d, want 0", p.choice)
		}
	})
	t.Run("up wraps at start", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.Update(promptKey("up"))
		if p.choice != 2 {
			t.Fatalf("choice after up-wrap = %d, want 2", p.choice)
		}
	})
	t.Run("down then enter accepts into Value", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = opts
		p.Update(promptKey("down"))
		_, cmd := p.Update(promptKey("enter"))
		m := promptMsg(t, cmd)
		if m.Canceled || m.Value != "b" || p.Value != "b" {
			t.Fatalf("enter on choice: msg=%+v p.Value=%q", m, p.Value)
		}
	})
	t.Run("view advances optionOffset when choice beyond slots", func(t *testing.T) {
		many := make([]PromptOption, 20)
		for i := range many {
			many[i] = PromptOption{Label: fmt.Sprintf("o%02d", i), Value: fmt.Sprintf("v%02d", i)}
		}
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = many
		p.initialize()
		p.choice = 10
		p.View(80, 12) // slots = 12-7 = 5
		if p.optionOffset != 6 {
			t.Fatalf("optionOffset = %d, want 6", p.optionOffset)
		}
	})
	t.Run("view rewinds optionOffset when choice above window", func(t *testing.T) {
		many := make([]PromptOption, 20)
		for i := range many {
			many[i] = PromptOption{Label: fmt.Sprintf("o%02d", i), Value: fmt.Sprintf("v%02d", i)}
		}
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = many
		p.initialize()
		p.choice = 2
		p.optionOffset = 10
		p.View(80, 12)
		if p.optionOffset != 2 {
			t.Fatalf("optionOffset = %d, want 2", p.optionOffset)
		}
	})
}

func TestPromptMultiChoiceToggleAndSubmit(t *testing.T) {
	p := NewPrompt(MultiChoicePrompt, "T", "L", "H", "")
	p.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	t.Run("space toggles on then off", func(t *testing.T) {
		p.Update(promptKey(" "))
		if got := strings.Join(p.Selected, ","); got != "a" {
			t.Fatalf("after first space Selected=%q", got)
		}
		p.Update(promptKey(" "))
		if len(p.Selected) != 0 {
			t.Fatalf("after second space Selected=%v, want empty", p.Selected)
		}
	})
	t.Run("enter emits comma-joined result", func(t *testing.T) {
		p.Update(promptKey(" "))
		p.Update(promptKey("down"))
		p.Update(promptKey(" "))
		_, cmd := p.Update(promptKey("enter"))
		m := promptMsg(t, cmd)
		if m.Canceled || strings.Join(m.Selected, ",") != "a,b" || m.Value != "a,b" {
			t.Fatalf("multi submit: %+v (p.Value=%q)", m, p.Value)
		}
	})
}

func TestPromptYesNoKeys(t *testing.T) {
	t.Run("up sets Yes", func(t *testing.T) {
		p := NewPrompt(YesNoPrompt, "T", "L", "H", "")
		p.Update(promptKey("up"))
		if p.Value != "Yes" || !p.changed {
			t.Fatalf("up: Value=%q changed=%v", p.Value, p.changed)
		}
	})
	t.Run("down sets No", func(t *testing.T) {
		p := NewPrompt(YesNoPrompt, "T", "L", "H", "Yes")
		p.Update(promptKey("down"))
		if p.Value != "No" {
			t.Fatalf("down: Value=%q, want No", p.Value)
		}
	})
	t.Run("y and n keys set value", func(t *testing.T) {
		p := NewPrompt(YesNoPrompt, "T", "L", "H", "")
		p.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
		if p.Value != "Yes" {
			t.Fatalf("y: Value=%q", p.Value)
		}
		p.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		if p.Value != "No" {
			t.Fatalf("n: Value=%q", p.Value)
		}
	})
	t.Run("enter with empty value defaults to No", func(t *testing.T) {
		p := NewPrompt(YesNoPrompt, "T", "L", "H", "")
		_, cmd := p.Update(promptKey("enter"))
		m := promptMsg(t, cmd)
		if m.Canceled || m.Value != "No" || p.Value != "No" {
			t.Fatalf("empty yes-no enter: msg=%+v p.Value=%q", m, p.Value)
		}
	})
}

func TestPromptNumberValidationMessages(t *testing.T) {
	min, max := 2, 5
	cases := []struct {
		name    string
		value   string
		wantSub string
	}{
		{"empty", "", "Enter a whole number"},
		{"not a number", "abc", "Enter a whole number"},
		{"below min", "1", "at least"},
		{"above max", "6", "at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPrompt(NumberPrompt, "T", "n", "", tc.value)
			p.Min = &min
			p.Max = &max
			_, cmd := p.Update(promptKey("enter"))
			if cmd != nil {
				t.Fatalf("invalid number produced cmd %v", cmd)
			}
			if p.Error == nil || !strings.Contains(p.Error.Error(), tc.wantSub) {
				t.Fatalf("Error=%v, want containing %q", p.Error, tc.wantSub)
			}
		})
	}
	t.Run("valid number accepted", func(t *testing.T) {
		p := NewPrompt(NumberPrompt, "T", "n", "", "3")
		p.Min = &min
		p.Max = &max
		_, cmd := p.Update(promptKey("enter"))
		m := promptMsg(t, cmd)
		if m.Canceled || m.Value != "3" {
			t.Fatalf("valid number: %+v", m)
		}
	})
	t.Run("view renders error row", func(t *testing.T) {
		p := NewPrompt(NumberPrompt, "T", "n", "", "abc")
		p.SetError(errors.New("boom"))
		v := p.View(60, 10)
		if !strings.Contains(v, "Error:") || !strings.Contains(v, "boom") {
			t.Fatalf("view missing error row: %q", v)
		}
	})
}

func TestPromptTabFocusCycling(t *testing.T) {
	t.Run("tab enters and cycles buttons then leaves", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		p.Update(promptKey("tab"))
		if !p.buttonsFocused || p.Buttons.Focus != 0 {
			t.Fatalf("first tab: focused=%v focus=%d", p.buttonsFocused, p.Buttons.Focus)
		}
		p.Update(promptKey("tab"))
		if p.Buttons.Focus != 1 {
			t.Fatalf("second tab: focus=%d, want 1", p.Buttons.Focus)
		}
		p.Update(promptKey("tab"))
		if p.buttonsFocused || p.Buttons.Focus != 0 {
			t.Fatalf("third tab: focused=%v focus=%d, want false/0", p.buttonsFocused, p.Buttons.Focus)
		}
	})
	t.Run("shift+tab enters on last button", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		p.Update(promptKey("shift+tab"))
		if !p.buttonsFocused || p.Buttons.Focus != len(p.Buttons.Labels)-1 {
			t.Fatalf("shift+tab: focused=%v focus=%d", p.buttonsFocused, p.Buttons.Focus)
		}
	})
	t.Run("enter with focused buttons accepts", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "kept")
		p.Update(promptKey("tab"))
		_, cmd := p.Update(promptKey("enter"))
		m := promptMsg(t, cmd)
		if m.Canceled || m.Value != "kept" {
			t.Fatalf("focused enter: %+v", m)
		}
	})
}

func TestPromptGetAnswerByKind(t *testing.T) {
	t.Run("choice without options errors", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		v, e := p.GetAnswer()
		if e == nil || !strings.Contains(e.Error(), "Choose an option") || v != "" {
			t.Fatalf("GetAnswer = %q, %v", v, e)
		}
	})
	t.Run("choice accept with no options sets Error", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		if cmd := p.accept(); cmd != nil || p.Error == nil {
			t.Fatalf("accept: cmd=%v Error=%v", cmd, p.Error)
		}
	})
	t.Run("choice out of range resets to first", func(t *testing.T) {
		p := NewPrompt(ChoicePrompt, "T", "L", "H", "")
		p.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
		p.choice = -1
		v, e := p.GetAnswer()
		if e != nil || v != "a" || p.choice != 0 {
			t.Fatalf("GetAnswer = %q, %v, choice=%d", v, e, p.choice)
		}
	})
	t.Run("multi choice joins selected", func(t *testing.T) {
		p := NewPrompt(MultiChoicePrompt, "T", "L", "H", "")
		p.Selected = []string{"a", "b"}
		v, e := p.GetAnswer()
		if e != nil || v != "a,b" {
			t.Fatalf("GetAnswer = %q, %v", v, e)
		}
	})
	t.Run("text prompt returns value", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "raw")
		v, e := p.GetAnswer()
		if e != nil || v != "raw" {
			t.Fatalf("GetAnswer = %q, %v", v, e)
		}
	})
	t.Run("validate error blocks accept", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "bad")
		p.Validate = func(string) error { return errors.New("nope") }
		if cmd := p.accept(); cmd != nil || p.Error == nil {
			t.Fatalf("accept with validator: cmd=%v Error=%v", cmd, p.Error)
		}
	})
}

func TestPromptViewLayout(t *testing.T) {
	t.Run("view sets buttonY to last row", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		p.View(80, 20)
		if p.buttonY != 19 {
			t.Fatalf("buttonY = %d, want 19", p.buttonY)
		}
	})
	t.Run("tiny 1x1 view still renders", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		v := p.View(1, 1)
		if v == "" || strings.Contains(v, "\n") {
			t.Fatalf("1x1 view = %q, want single line", v)
		}
	})
	t.Run("zero height view empty", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		if v := p.View(80, 0); v != "" {
			t.Fatalf("zero height view = %q", v)
		}
	})
	t.Run("saving indicator row", func(t *testing.T) {
		p := NewPrompt(TextPrompt, "T", "L", "H", "x")
		p.SetSaving(true)
		if v := p.View(60, 10); !strings.Contains(v, "Saving...") {
			t.Fatalf("view missing saving indicator: %q", v)
		}
	})
}
