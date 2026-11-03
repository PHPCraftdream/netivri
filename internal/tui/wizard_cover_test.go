package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// errCoverInvalid is a sentinel validation error for the coverage tests.
var errCoverInvalid = errors.New("invalid value")

// coverWizard builds a two-step wizard: a text step followed by another text step.
func coverWizard(t *testing.T) *Wizard {
	t.Helper()
	return NewWizard("Add", []WizardStep{
		{Key: "first", Prompt: NewPrompt(TextPrompt, "", "Name", "", "")},
		{Key: "second", Prompt: NewPrompt(TextPrompt, "", "URL", "", "")},
	})
}

func coverResult(t *testing.T, cmd tea.Cmd) WizardResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected cmd, got nil")
	}
	m := cmd()
	r, ok := m.(WizardResultMsg)
	if !ok {
		t.Fatalf("expected WizardResultMsg, got %#v", m)
	}
	return r
}

// --- Esc without typing cancels (wizard.go ~123-130) ---

func TestCoverWizardEscWithoutTypingCancels(t *testing.T) {
	w := coverWizard(t)
	_, cmd := w.Update(tuiTestKey("esc"))
	r := coverResult(t, cmd)
	if !r.Canceled {
		t.Fatal("expected canceled result")
	}
	if w.discard {
		t.Fatal("discard dialog should not open without typed content")
	}
}

// --- Esc after typing opens the discard dialog (wizard.go ~124-127, hasTyped ~200-209) ---

func TestCoverWizardEscAfterTypingOpensDiscardDialog(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("esc"))
	if !w.discard {
		t.Fatal("expected discard dialog after esc with typed content")
	}
	if w.discardFocus != 0 {
		t.Fatalf("discardFocus=%d, want 0", w.discardFocus)
	}
}

// --- Discard dialog key handling (wizard.go ~102-122) ---

func TestCoverWizardDiscardDialogFocusKeys(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want int
	}{
		{"left moves to Cancel", tuiTestKey("left"), 0},
		{"shift+tab moves to Cancel", tuiTestKey("shift+tab"), 0},
		{"tab moves to Discard", tuiTestKey("tab"), 1},
		{"right moves to Discard", tuiTestKey("right"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coverWizard(t)
			w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
			w.Update(tuiTestKey("esc")) // open dialog, focus 0
			w.Update(tc.key)
			if !w.discard {
				t.Fatal("dialog closed unexpectedly")
			}
			if w.discardFocus != tc.want {
				t.Fatalf("discardFocus=%d, want %d", w.discardFocus, tc.want)
			}
		})
	}
}

func TestCoverWizardDiscardDialogEnterOnCancelResumes(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("esc")) // discardFocus == 0
	_, cmd := w.Update(tuiTestKey("enter"))
	if cmd != nil {
		t.Fatal("enter on Cancel should not emit a result")
	}
	if w.discard {
		t.Fatal("dialog should close on Cancel")
	}
}

func TestCoverWizardDiscardDialogEnterOnDiscardCancels(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("esc"))
	w.Update(tuiTestKey("right")) // focus Discard
	_, cmd := w.Update(tuiTestKey("enter"))
	r := coverResult(t, cmd)
	if !r.Canceled {
		t.Fatal("expected canceled result from Discard")
	}
}

func TestCoverWizardDiscardDialogEscResumes(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("esc"))
	w.Update(tuiTestKey("esc"))
	if w.discard {
		t.Fatal("esc should close the discard dialog")
	}
	if w.Index != 0 {
		t.Fatalf("Index=%d, want 0 (stay on step)", w.Index)
	}
}

// --- Tab/shift+tab button focus on steps (wizard.go ~156-171) ---

func TestCoverWizardTabFocusesButtons(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want int
	}{
		{"tab focuses Back", tuiTestKey("tab"), 0},
		{"shift+tab focuses Next", tuiTestKey("shift+tab"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coverWizard(t)
			w.Update(tc.key)
			if w.Buttons.Focus != tc.want {
				t.Fatalf("Focus=%d, want %d", w.Buttons.Focus, tc.want)
			}
		})
	}
}

func TestCoverWizardTabTwiceMovesFocusBackToNext(t *testing.T) {
	w := coverWizard(t)
	w.Update(tuiTestKey("tab")) // focus Back (buttonsFocused=true)
	w.Update(tuiTestKey("tab")) // wrap: focus leaves buttons
	if w.Buttons.Focus != 1 {
		t.Fatalf("Focus=%d, want 1", w.Buttons.Focus)
	}
}

// --- Back button on steps (activate i==0, wizard.go ~226-235) ---

func TestCoverWizardBackButtonReturnsToPreviousStep(t *testing.T) {
	w := coverWizard(t)
	w.Steps[0].Prompt.Value = "a"
	w.Values["first"] = "a"
	w.Index = 1
	w.Update(tuiTestKey("tab")) // focus Back
	w.Update(tuiTestKey("enter"))
	if w.Index != 0 {
		t.Fatalf("Index=%d, want 0 after Back", w.Index)
	}
	if w.Review {
		t.Fatal("should not be in Review after Back")
	}
}

// --- Enter advances and validates (activate(1), wizard.go ~186-188, 236-250, saveCurrent ~252-280) ---

func TestCoverWizardEnterSavesCurrentStepValues(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("enter"))
	if w.Index != 1 {
		t.Fatalf("Index=%d, want 1", w.Index)
	}
	if w.Values["first"] != "x" {
		t.Fatalf("Values[first]=%q, want x", w.Values["first"])
	}
}

func TestCoverWizardEnterOnLastStepEntersReview(t *testing.T) {
	w := coverWizard(t)
	w.Index = 1
	w.Update(tuiTestKey("enter"))
	if !w.Review {
		t.Fatal("expected Review on last step advance")
	}
	if w.Buttons.Focus != 1 {
		t.Fatalf("review Focus=%d, want 1 (Create)", w.Buttons.Focus)
	}
}

func TestCoverWizardEnterWithValidationFailureKeepsIndexAndSetsError(t *testing.T) {
	p := NewPrompt(TextPrompt, "", "Name", "", "bad")
	p.Validate = func(string) error { return errCoverInvalid }
	w := NewWizard("Add", []WizardStep{{Key: "first", Prompt: p}, {Key: "second", Prompt: NewPrompt(TextPrompt, "", "URL", "", "")}})
	w.Update(tuiTestKey("enter"))
	if w.Index != 0 {
		t.Fatalf("Index=%d, want 0 (validation failed)", w.Index)
	}
	if w.Error == nil {
		t.Fatal("expected non-nil Error after failed validation")
	}
}

func TestCoverWizardStepWithoutPromptIsSavedSkipped(t *testing.T) {
	w := NewWizard("Add", []WizardStep{{Key: "info"}, {Key: "second", Prompt: NewPrompt(TextPrompt, "", "URL", "", "")}})
	w.Update(tuiTestKey("enter"))
	if w.Index != 1 {
		t.Fatalf("Index=%d, want 1 (promptless step saved without validation)", w.Index)
	}
	if w.Error != nil {
		t.Fatalf("unexpected error: %v", w.Error)
	}
}

// --- Review navigation (wizard.go ~131-155) ---

func coverReview(t *testing.T, steps int) *Wizard {
	t.Helper()
	ss := make([]WizardStep, steps)
	for i := range ss {
		ss[i] = WizardStep{Key: "k", Prompt: NewPrompt(TextPrompt, "", "Name", "", "v")}
	}
	w := NewWizard("Add", ss)
	w.Values["k"] = "v"
	w.Review = true
	w.Buttons = Buttons{Labels: []string{"Back", "Create"}, Focus: 1}
	w.View(80, 12) // sets w.height for scroll slot math
	return w
}

func TestCoverWizardReviewTabMovesButtonFocus(t *testing.T) {
	w := coverReview(t, 2)
	w.Update(tuiTestKey("tab"))
	if w.Buttons.Focus != 0 {
		t.Fatalf("Focus after tab=%d, want 0", w.Buttons.Focus)
	}
}

func TestCoverWizardReviewLeftRightMoveFocus(t *testing.T) {
	w := coverReview(t, 2)
	w.Update(tuiTestKey("left")) // 1 -> 0
	if w.Buttons.Focus != 0 {
		t.Fatalf("Focus after left=%d, want 0", w.Buttons.Focus)
	}
	w.Update(tuiTestKey("right")) // 0 -> 1
	if w.Buttons.Focus != 1 {
		t.Fatalf("Focus after right=%d, want 1", w.Buttons.Focus)
	}
}

func TestCoverWizardReviewEnterOnBackReturnsToLastStep(t *testing.T) {
	w := coverReview(t, 3)
	w.Update(tuiTestKey("left")) // focus Back
	w.Update(tuiTestKey("enter"))
	if w.Review {
		t.Fatal("Back from review should leave Review")
	}
	if w.Index != 2 {
		t.Fatalf("Index=%d, want 2 (last step)", w.Index)
	}
	if len(w.Buttons.Labels) != 2 || w.Buttons.Labels[1] != "Next" {
		t.Fatalf("buttons not reset after leaving review: %v", w.Buttons.Labels)
	}
}

func TestCoverWizardReviewEnterOnCreateEmitsResult(t *testing.T) {
	w := coverReview(t, 2)
	w.Values["k"] = "saved"
	_, cmd := w.Update(tuiTestKey("enter")) // Focus starts at 1 (Create)
	r := coverResult(t, cmd)
	if r.Canceled {
		t.Fatal("Create should not cancel")
	}
	if r.Values["k"] != "saved" {
		t.Fatalf("result values=%v, want k=saved", r.Values)
	}
}

func TestCoverWizardReviewScrollKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []tea.KeyPressMsg
		want int
	}{
		{"down scrolls one row", []tea.KeyPressMsg{tuiTestKey("down")}, 1},
		{"up at top stays clamped", []tea.KeyPressMsg{tuiTestKey("up")}, 0},
		{"pgdown scrolls five rows clamped to max", []tea.KeyPressMsg{tuiTestKey("pgdown")}, 2},
		{"pgup clamps at zero", []tea.KeyPressMsg{tuiTestKey("pgup")}, 0},
		{"home resets offset", []tea.KeyPressMsg{tuiTestKey("down"), tuiTestKey("home")}, 0},
		{"end jumps to last page", []tea.KeyPressMsg{tuiTestKey("end")}, 2},
	}
	// 7 steps, View height 12 -> slots = 5 -> max offset = 2
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coverReview(t, 7)
			for _, k := range tc.keys {
				w.Update(k)
			}
			if w.reviewOffset != tc.want {
				t.Fatalf("reviewOffset=%d, want %d", w.reviewOffset, tc.want)
			}
		})
	}
}

// --- Mouse clicks (wizard.go ~75-96) ---

func TestCoverWizardMouseClickOnBackReturnsToPreviousStep(t *testing.T) {
	w := coverWizard(t)
	w.Values["first"] = "a"
	w.Index = 1
	w.View(80, 24)
	w.Update(tea.MouseClickMsg{X: 3, Y: w.height - 1, Button: tea.MouseLeft})
	if w.Index != 0 {
		t.Fatalf("Index=%d, want 0 after clicking Back", w.Index)
	}
}

func TestCoverWizardMouseClickOnNextAdvancesToReview(t *testing.T) {
	w := coverWizard(t)
	w.Index = 1
	w.View(80, 24)
	// Segments: "  [ Back ]" (width 10), gap 3, then "  [ Next ]" -> X in [13,23).
	w.Update(tea.MouseClickMsg{X: 15, Y: w.height - 1, Button: tea.MouseLeft})
	if !w.Review {
		t.Fatalf("expected Review after clicking Next on last step (Index=%d)", w.Index)
	}
}

func TestCoverWizardMouseClickAboveButtonRowIgnored(t *testing.T) {
	w := coverWizard(t)
	w.View(80, 24)
	w.Update(tea.MouseClickMsg{X: 3, Y: 10, Button: tea.MouseLeft})
	if w.Index != 0 {
		t.Fatalf("Index=%d, want 0 (click ignored)", w.Index)
	}
	if w.Review {
		t.Fatal("click above button row must not advance")
	}
}

func TestCoverWizardMouseRightClickIgnored(t *testing.T) {
	w := coverWizard(t)
	w.Index = 1
	w.View(80, 24)
	w.Update(tea.MouseClickMsg{X: 3, Y: w.height - 1, Button: tea.MouseRight})
	if w.Index != 1 {
		t.Fatalf("Index=%d, want 1 (right click ignored)", w.Index)
	}
}

// --- View / review rendering paths (wizard.go ~339-386, focusLabel ~388-393) ---

func TestCoverWizardDiscardViewShowsQuestionAndFocusLabel(t *testing.T) {
	w := coverWizard(t)
	w.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	w.Update(tuiTestKey("esc")) // discardFocus == 0 -> [Cancel]
	view := w.View(80, 12)
	if !strings.Contains(view, "Discard all entered values?") {
		t.Fatal("discard question not rendered")
	}
	if !strings.Contains(view, "[Cancel]") || !strings.Contains(view, "Discard") {
		t.Fatal("focus labels not rendered in discard view")
	}
}

func TestCoverWizardReviewViewShowsStepsAndValues(t *testing.T) {
	w := coverWizard(t)
	w.Values["first"] = "alpha"
	w.Values["second"] = "beta"
	w.Review = true
	view := w.View(80, 12)
	if !strings.Contains(view, "Review") {
		t.Fatal("review header not rendered")
	}
	if !strings.Contains(view, "Name: alpha") {
		t.Fatal("step value not rendered in review")
	}
}

func TestCoverWizardReviewMasksSecretValues(t *testing.T) {
	p := NewPrompt(SecretPrompt, "", "Token", "", "")
	w := NewWizard("Add", []WizardStep{{Key: "token", Prompt: p}})
	w.Values["token"] = "supersecret"
	w.Review = true
	view := w.View(80, 12)
	if strings.Contains(view, "supersecret") {
		t.Fatal("secret leaked in review view")
	}
	if !strings.Contains(view, "********") {
		t.Fatal("secret not masked in review view")
	}
}

func TestCoverWizardReviewViewRendersErrorLine(t *testing.T) {
	w := coverWizard(t)
	w.Review = true
	w.SetError(errCoverInvalid)
	view := w.View(80, 12)
	if !strings.Contains(view, "Error:") {
		t.Fatal("error line not rendered in review view")
	}
}

// --- redactWizardValue (wizard.go ~394-411) ---

func TestCoverRedactWizardValue(t *testing.T) {
	p := NewPrompt(SecretPrompt, "", "Token", "", "")
	if got := redactWizardValue(p, "hunter2"); got != "********" {
		t.Fatalf("secret value %q, want ********", got)
	}
	u := NewPrompt(TextPrompt, "", "URL", "", "")
	got := redactWizardValue(u, "https://alice:pw@example.test/path?q=secret")
	if strings.Contains(got, "alice") || strings.Contains(got, "pw") || strings.Contains(got, "secret") {
		t.Fatalf("URL credentials leaked: %q", got)
	}
	if !strings.HasPrefix(got, "https://") {
		t.Fatalf("redacted URL malformed: %q", got)
	}
	if plain := redactWizardValue(u, "plain-value"); plain != "plain-value" {
		t.Fatalf("plain value changed: %q", plain)
	}
}
