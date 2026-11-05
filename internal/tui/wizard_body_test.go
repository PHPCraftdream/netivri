package tui

import (
	"errors"
	"testing"
)

func TestWizardBodyStepViewAndReviewSkip(t *testing.T) {
	p := NewPrompt(TextPrompt, "", "Name", "", "alice")
	w := NewWizard("Add", []WizardStep{{Key: "name", Prompt: p}, {Key: "preview", Body: func(width, height int) []string { return []string{"4 nodes; 1 with no country", "DE — 2 nodes"} }}})
	w.Update(wizardKey("enter"))
	if w.Index != 1 {
		t.Fatalf("enter did not advance to the body step: index=%d", w.Index)
	}
	v := w.View(60, 16)
	if !contains(v, "4 nodes; 1 with no country") || !contains(v, "DE — 2 nodes") {
		t.Fatalf("body step not rendered: %q", v)
	}
	w.Update(wizardKey("enter"))
	if !w.Review {
		t.Fatal("enter on the last step did not reach the review")
	}
	w.Values["preview"] = "would-leak"
	v = w.View(60, 16)
	if !contains(v, "Name: alice") || contains(v, ": would-leak") {
		t.Fatalf("review did not skip the promptless step: %q", v)
	}
	w.Review = false
	w.Index = 1
	w.SetError(errors.New("Fetch failed"))
	v = w.View(60, 16)
	if !contains(v, "Error: Fetch failed") {
		t.Fatalf("error row missing on the body step: %q", v)
	}
}

func TestWizardBusyGatesInputAndEscCancels(t *testing.T) {
	p := NewPrompt(TextPrompt, "", "Name", "", "alice")
	w := NewWizard("Add", []WizardStep{{Key: "name", Prompt: p}})
	w.Busy = true
	w.BusyText = "Fetching subscription..."
	if _, cmd := w.Update(wizardKey("enter")); cmd != nil || w.Index != 0 || w.Review {
		t.Fatalf("busy enter was not gated: cmd=%v index=%d review=%v", cmd, w.Index, w.Review)
	}
	if v := w.View(60, 12); !contains(v, "Fetching subscription...") {
		t.Fatalf("busy text not shown: %q", v)
	}
	_, cmd := w.Update(wizardKey("esc"))
	if cmd == nil {
		t.Fatal("busy esc produced no command")
	}
	r, ok := cmd().(WizardResultMsg)
	if !ok || !r.Canceled {
		t.Fatalf("busy esc did not cancel: %#v", cmd())
	}
	_, _ = w.Update(wizardKey("x"))
}
