package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderContractPromptKinds(t *testing.T) {
	min, max := 1, 10
	kinds := []struct {
		name  string
		kind  PromptKind
		value string
	}{
		{"text", TextPrompt, "alice"}, {"secret", SecretPrompt, "fake-secret"},
		{"number", NumberPrompt, "3"}, {"choice", ChoicePrompt, "b"},
		{"multi", MultiChoicePrompt, ""}, {"yes-no", YesNoPrompt, "No"},
		{"confirm", ConfirmPrompt, ""},
	}
	for _, tc := range kinds {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPrompt(tc.kind, "Contract", "Value", "Hint", tc.value)
			p.Theme = NewThemeFor(false)
			p.Min, p.Max = &min, &max
			p.Options = []PromptOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}, {Label: "A deliberately long option", Value: "long", Description: "A deliberately long description"}}
			p.Selected = []string{"a"}
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}, {1, 1}} {
				w, h := size[0], size[1]
				view := p.View(w, h)
				checkRenderContract(t, view, w, h)
			}
			if tc.kind == ConfirmPrompt {
				_, cmd := p.Update(promptKey("enter"))
				if cmd == nil || !cmd().(PromptResultMsg).Canceled {
					t.Fatal("default confirm Enter must cancel")
				}
			}
			if tc.kind == ChoicePrompt {
				p.Update(promptKey("enter"))
				if p.Error != nil || p.Value != "b" {
					t.Fatalf("choice default not accepted: value=%q error=%v", p.Value, p.Error)
				}
			}
		})
	}
}

func TestRenderContractSavingKeepsButtonRows(t *testing.T) {
	p := NewPrompt(TextPrompt, "Contract", "Name", "", "alice")
	before := strings.Split(p.View(80, 24), "\n")
	p.SetSaving(true)
	after := strings.Split(p.View(80, 24), "\n")
	if before[23] != after[23] || before[22] == after[22] {
		t.Fatal("saving changed prompt button row or failed to use its dedicated state row")
	}
	w := NewWizard("Contract", []WizardStep{{Key: "name", Prompt: p}})
	w.View(80, 24)
	before = strings.Split(w.View(80, 24), "\n")
	w.SetSaving(true)
	after = strings.Split(w.View(80, 24), "\n")
	if len(before) != len(after) {
		t.Fatal("saving changed wizard height")
	}
	for i := range before {
		if i != 22 && i != 23 && before[i] != after[i] {
			t.Fatalf("saving changed wizard row %d outside its state area", i)
		}
	}
}

func TestRenderContractWizardNavigationReviewAndBounds(t *testing.T) {
	choice := NewPrompt(ChoicePrompt, "", "Choice", "", "b")
	choice.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	number := NewPrompt(NumberPrompt, "", "Number", "", "3")
	min, max := 1, 10
	number.Min, number.Max = &min, &max
	w := NewWizard("Contract", []WizardStep{
		{Key: "name", Prompt: NewPrompt(TextPrompt, "", "Name", "", "alice")},
		{Key: "secret", Prompt: NewPrompt(SecretPrompt, "", "Secret", "", "fake-secret")},
		{Key: "choice", Prompt: choice}, {Key: "number", Prompt: number},
	})
	for _, expected := range []struct{ key, value string }{{"name", "alice"}, {"secret", "fake-secret"}, {"choice", "b"}, {"number", "3"}} {
		for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}, {1, 1}} {
			view := w.View(size[0], size[1])
			checkRenderContract(t, view, size[0], size[1])
			if strings.Contains(view, "fake-secret") {
				t.Fatalf("secret leaked on wizard step %q at %dx%d", expected.key, size[0], size[1])
			}
		}
		w.View(80, 24)
		w.Update(wizardKey("enter"))
		if got := w.Values[expected.key]; got != expected.value {
			t.Fatalf("%s=%q, want %q", expected.key, got, expected.value)
		}
	}
	if !w.Review {
		t.Fatal("last step did not reach review")
	}
	w.Update(wizardKey("left"))
	if w.Buttons.Focus != 0 {
		t.Fatal("left did not focus review Back")
	}
	w.Update(wizardKey("enter"))
	if w.Review || w.Index != 3 || number.Value != "3" {
		t.Fatalf("Back did not retain final value: review=%v index=%d value=%q", w.Review, w.Index, number.Value)
	}
	w.Update(wizardKey("tab"))
	if !w.buttonsFocused || w.Buttons.Focus != 0 {
		t.Fatal("Tab did not focus first wizard button")
	}
	w.Update(wizardKey("tab"))
	if w.Buttons.Focus != 1 {
		t.Fatal("Tab did not cycle to next wizard button")
	}
	w.SetError(errors.New("fake-secret"))
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}, {1, 1}} {
		w.Review = true
		view := w.View(size[0], size[1])
		checkRenderContract(t, view, size[0], size[1])
		if strings.Contains(view, "fake-secret") {
			t.Fatal("secret leaked from wizard review or error")
		}
	}
}

func TestRenderContractThemePreservesContentAndCellPositions(t *testing.T) {
	getenv := func(string) string { return "" }
	var baseline map[string][2]int
	for _, ascii := range []bool{false, true} {
		for _, noColor := range []bool{false, true} {
			theme := newTheme(false, getenv, noColor)
			theme.ASCII = ascii
			list := NewList([]ListColumn{{Title: "Name", Width: 20}}, []Row{{Cells: []string{"alice"}}})
			list.Title, list.Focused = "Accounts", true
			list.SetTheme(theme)
			lv := list.View(60, 16)
			card := NewCard("Accounts", []CardLine{{ID: "name", Label: "Name", Value: "alice", Kind: CardField}, {ID: "save", Label: "Save", Kind: CardAction, Action: true}})
			card.SetTheme(theme)
			cv := card.View(60, 16)
			positions := map[string][2]int{}
			for _, item := range []struct {
				view, label string
			}{{lv, "Accounts"}, {lv, "Name"}, {lv, "alice"}, {cv, "Accounts"}, {cv, "Name"}, {cv, "alice"}} {
				positions[item.label+"/"+map[bool]string{true: "list", false: "card"}[item.view == lv]] = labelPosition(item.view, item.label)
			}
			if baseline == nil {
				baseline = positions
			} else {
				for label, position := range baseline {
					if got := positions[label]; got != position {
						t.Fatalf("theme changed %s position: got %v, want %v", label, got, position)
					}
				}
			}
			p := NewPrompt(ChoicePrompt, "Theme", "Choice", "", "a")
			p.Options = []PromptOption{{Label: "a", Value: "a", Description: "description"}}
			p.SetTheme(theme)
			lines := strings.Split(p.View(80, 24), "\n")
			if lipgloss.Width(lines[3][:strings.Index(lines[3], "a")]) < 2 {
				t.Fatal("choice marker displaced its label")
			}
			w := NewWizard("Theme", []WizardStep{{Key: "a", Prompt: NewPrompt(TextPrompt, "", "A", "", "a")}})
			w.SetTheme(theme)
			if got := strings.Split(w.View(80, 24), "\n")[23]; got == "" {
				t.Fatal("theme removed wizard button row")
			}
		}
	}
}

func labelPosition(view, label string) [2]int {
	for y, line := range strings.Split(ansi.Strip(view), "\n") {
		if x := strings.Index(line, label); x >= 0 {
			return [2]int{lipgloss.Width(line[:x]), y}
		}
	}
	return [2]int{-1, -1}
}

func checkRenderContract(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("got %d lines, want %d", len(lines), height)
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			t.Fatalf("line %d has width %d, exceeds %d: %q", i, lipgloss.Width(line), width, line)
		}
	}
}

var _ tea.Msg
