package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func wizardKey(s string) tea.KeyPressMsg { return tuiTestKey(s) }
func TestWizardEnterAdvancesAndValidates(t *testing.T) {
	p := NewPrompt(TextPrompt, "", "Name", "", "alice")
	w := NewWizard("Add", []WizardStep{{Key: "name", Prompt: p}, {Key: "url", Prompt: NewPrompt(TextPrompt, "", "URL", "", "")}})
	w.Update(wizardKey("enter"))
	if w.Index != 1 || w.Values["name"] != "alice" {
		t.Fatalf("index=%d values=%v", w.Index, w.Values)
	}
}
func TestWizardChoiceDefaultReviewAndCreate(t *testing.T) {
	p := NewPrompt(ChoicePrompt, "", "Choice", "", "")
	p.Options = []PromptOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	w := NewWizard("Add", []WizardStep{{Key: "choice", Prompt: p}})
	w.Update(wizardKey("enter"))
	if !w.Review || w.Values["choice"] != "a" {
		t.Fatalf("review=%v values=%v", w.Review, w.Values)
	}
	_, cmd := w.Update(wizardKey("enter"))
	if cmd == nil {
		t.Fatal("create did not emit result")
	}
	m := cmd()
	r, ok := m.(WizardResultMsg)
	if !ok || r.Values["choice"] != "a" {
		t.Fatalf("result=%#v", m)
	}
}
func TestWizardReviewMasksSecret(t *testing.T) {
	p := NewPrompt(SecretPrompt, "Secret", "Token", "", "hidden-token")
	w := NewWizard("Add", []WizardStep{{Key: "token", Prompt: p}})
	w.Values["token"] = "hidden-token"
	w.Review = true
	if got := w.View(60, 12); contains(got, "hidden-token") || !contains(got, "********") {
		t.Fatal("secret review masking failed")
	}
}

func TestWizardReviewRedactsSecretErrors(t *testing.T) {
	secret := "https://alice:password@example.test/path?token=query-secret"
	p := NewPrompt(SecretPrompt, "Secret", "Token", "", "")
	w := NewWizard("Add", []WizardStep{{Key: "token", Prompt: p, Optional: true}})
	w.Values["token"] = secret
	w.Review = true
	w.SetError(errors.New("bad password query-secret example.test alice"))
	view := w.View(80, 12)
	for _, raw := range []string{"password", "query-secret", "example.test", "alice"} {
		if strings.Contains(view, raw) {
			t.Fatal("secret data exposed in review error")
		}
	}
}

func TestWizardValidationErrorRedactsSecret(t *testing.T) {
	secret := "https://alice:password@example.test/path?token=query-secret"
	p := NewPrompt(SecretPrompt, "Secret", "Token", "", secret)
	p.Validate = func(string) error { return errors.New("invalid query-secret for alice") }
	w := NewWizard("Add", []WizardStep{{Key: "token", Prompt: p}})
	w.SetError(errors.New("validation failed for query-secret password"))
	view := w.View(80, 12)
	for _, raw := range []string{"query-secret", "password", "alice"} {
		if strings.Contains(view, raw) {
			t.Fatal("secret data exposed in validation error")
		}
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
