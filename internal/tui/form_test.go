package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestFormRedactionPerFieldAndRetention(t *testing.T) {
	f := &Form{Fields: []Field{{Name: "Password", Kind: PasswordField, Value: "secret", Validate: func(v string) error { return errors.New("bad " + v) }}, {Name: "Confirm", Kind: PasswordField, Value: "other"}}}
	f.submit()
	view := f.View(60, 10)
	for _, s := range []string{"secret", "other"} {
		if strings.Contains(view, s) {
			t.Fatalf("secret leaked: %q", view)
		}
	}
	if f.Fields[0].Error == "" || f.Fields[0].Value != "secret" {
		t.Fatal("field validation not retained")
	}
}
func TestFormSubmitErrorRedactsSecretsAndSuccessIncludesValue(t *testing.T) {
	f := &Form{Fields: []Field{{Name: "Password", Kind: PasswordField, Value: "pw"}}, Submit: func(map[string]string) error { return errors.New("failed pw") }}
	f.submit()
	if strings.Contains(f.View(40, 6), "failed pw") {
		t.Fatal("submit error leaked secret")
	}
	f.Submit = nil
	m := f.submit()().(FormResultMsg)
	if m.Values["Password"] != "pw" {
		t.Fatal("success missing value")
	}
}
func TestFormFocusChoiceYesNoMultiAndAsModal(t *testing.T) {
	f := &Form{Fields: []Field{{Name: "Single", Kind: SingleChoiceField, Options: []string{"a", "b"}, Value: "a"}, {Name: "Multiple", Kind: MultiChoiceField, Options: []string{"alpha", "atom", "beta"}}, {Name: "Yes", Kind: YesNoField}}}
	f.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if f.Fields[0].Value != "b" {
		t.Fatal("single choice down")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})
	if f.Fields[1].Value != "alpha" {
		t.Fatal("multiple choice highlight")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(f.Fields[1].Selected) != 1 {
		t.Fatal("space should select highlighted option")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	if f.Fields[2].Value != "Yes" {
		t.Fatal("yes input")
	}
	modal := f.AsModal()
	modal, _ = modal.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := modal.(formModal); !ok {
		t.Fatal("modal adapter")
	}
}
func TestFormSpaceKeyIsLiteralForTextAndPasswordAndTogglesChoices(t *testing.T) {
	f := &Form{Fields: []Field{
		{Name: "Text", Kind: TextField},
		{Name: "Password", Kind: PasswordField},
		{Name: "Multi", Kind: MultiChoiceField, Options: []string{"alpha"}, Value: "alpha"},
		{Name: "YesNo", Kind: YesNoField},
	}}
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	f.Update(space)
	if f.Fields[0].Value != " " {
		t.Fatal("text field did not accept literal space")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f.Update(space)
	if f.Fields[1].Value != " " {
		t.Fatal("password field did not accept literal space")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f.Update(space)
	if len(f.Fields[2].Selected) != 1 || f.Fields[2].Selected[0] != "alpha" {
		t.Fatal("Space did not toggle highlighted multi-choice")
	}
	f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f.Update(space)
	if f.Fields[3].Value != "Yes" {
		t.Fatal("Space did not toggle yes/no")
	}
}

func TestFormValidationAndSubmitErrorInlineAndRetry(t *testing.T) {
	f := &Form{Fields: []Field{{Name: "Name", Kind: TextField, Validate: func(v string) error {
		if v == "" {
			return errors.New("required")
		}
		return nil
	}}}}
	if f.submit() != nil || f.Fields[0].Error != "required" || !strings.Contains(f.View(40, 5), "Error: required") {
		t.Fatal("field error not inline")
	}
	f.Fields[0].Value = "Ada"
	if got := f.submit()().(FormResultMsg); got.Values["Name"] != "Ada" {
		t.Fatal("retry failed")
	}
}

func TestFormScrollKeepsFocusedFieldErrorAndHints(t *testing.T) {
	fields := make([]Field, 8)
	for i := range fields {
		fields[i] = Field{Name: "Field" + string(rune('0'+i)), Kind: TextField}
	}
	fields[7].Validate = func(string) error { return errors.New("invalid") }
	f := &Form{TitleText: "Edit", Fields: fields}
	for range fields {
		f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	f.submit()
	view := f.View(80, 5)
	for _, want := range []string{"> Field7:", "Error: invalid", "Tab/Shift-Tab"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %q", want, view)
		}
	}
}

func TestFormScrollResizeSubmitErrorAndMasking(t *testing.T) {
	fields := make([]Field, 7)
	for i := range fields {
		fields[i] = Field{Name: "Field", Kind: TextField}
	}
	fields[6] = Field{Name: "Password", Kind: PasswordField, Value: "secret"}
	f := &Form{Fields: fields, Focus: 6, Submit: func(map[string]string) error { return errors.New("save failed") }}
	f.submit()
	large := f.View(80, 8)
	if strings.Contains(large, "secret") || !strings.Contains(large, "save failed") || !strings.Contains(large, "Tab/Shift-Tab") {
		t.Fatalf("bad large view: %q", large)
	}
	priorScroll := f.scroll
	f.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
	if f.width != 80 || f.height != 4 || f.scroll <= priorScroll {
		t.Fatalf("resize did not recompute layout: scroll %d -> %d, size %dx%d", priorScroll, f.scroll, f.width, f.height)
	}
	small := f.View(80, 4)
	if strings.Contains(small, "secret") || !strings.Contains(small, "save failed") || !strings.Contains(small, "Tab/Shift-Tab") || !strings.Contains(small, "> Password:") {
		t.Fatalf("bad resized view: %q", small)
	}
}

func TestFormFocusedChoiceErrorFitsShortView(t *testing.T) {
	f := &Form{TitleText: "Edit", Fields: []Field{{Name: "Mode", Kind: MultiChoiceField, Value: "fast", Options: []string{"fast", "safe"}, Selected: []string{"safe"}, Validate: func(string) error { return errors.New("choose again") }}}}
	f.submit()
	view := f.View(80, 3)
	for _, want := range []string{"> Mode: fast", "Error: choose again", "Tab/Shift-Tab"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %q", want, view)
		}
	}
}

func TestFormViewZeroAndTinyDimensions(t *testing.T) {
	f := &Form{Fields: []Field{{Name: "Name", Kind: TextField}}, Focus: 0}
	for _, size := range [][2]int{{0, 0}, {1, 1}, {2, 2}} {
		view := f.View(size[0], size[1])
		if view != "" && strings.Count(view, "\n")+1 > size[1] {
			t.Fatalf("height exceeded %v: %q", size, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatalf("width exceeded %v: %q", size, view)
			}
		}
	}
}
