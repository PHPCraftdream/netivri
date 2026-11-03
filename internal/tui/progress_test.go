package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestProgressClampsRendersASCIIAndFits(t *testing.T) {
	p := Progress{Label: "sync", Value: 150}
	if p.Percent() != 100 {
		t.Fatal("percent clamp")
	}
	view := p.View(20)
	if lipgloss.Width(view) > 20 || !strings.Contains(view, "100%") {
		t.Fatalf("bounded percentage %q", view)
	}
	p.Value = -3
	if p.Percent() != 0 {
		t.Fatal("negative clamp")
	}
	if p.View(0) != "" {
		t.Fatal("zero size")
	}
}
