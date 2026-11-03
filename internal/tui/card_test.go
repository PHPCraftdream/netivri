package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestCardViewInitialSizeAndTinyRecovery(t *testing.T) {
	c := NewCard("Title", []CardLine{{Label: "Value", Value: "present"}})
	for _, size := range [][2]int{{60, 16}} {
		got := c.View(size[0], size[1])
		lines := strings.Split(got, "\n")
		if len(lines) != size[1] {
			t.Fatalf("View(%d,%d) has %d rows", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if width := lipgloss.Width(line); width != size[0] {
				t.Errorf("View(%d,%d) row %d width = %d", size[0], size[1], i, width)
			}
		}
		if !strings.Contains(got, "Title") || !strings.Contains(got, "present") {
			t.Fatalf("initial view missing title or value: %q", got)
		}
	}
	c.View(1, 1)
	got := c.View(60, 16)
	if !strings.Contains(got, "Title") || !strings.Contains(got, "present") {
		t.Fatalf("view did not recover after tiny size: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 16 {
		t.Fatalf("recovered view has %d rows, want 16", len(lines))
	}
	for i, line := range lines {
		if width := lipgloss.Width(line); width != 60 {
			t.Errorf("recovered row %d width = %d, want 60", i, width)
		}
	}
}

func TestCardViewUsesExactOuterDimensions(t *testing.T) {
	c := NewCard("Title", []CardLine{{Label: "A"}, {Label: "B"}, {Label: "C"}})
	for _, size := range [][2]int{{64, 26}, {30, 6}, {8, 3}, {2, 2}} {
		got := c.View(size[0], size[1])
		lines := strings.Split(got, "\n")
		if len(lines) != size[1] {
			t.Errorf("View(%d,%d) has %d rows", size[0], size[1], len(lines))
			continue
		}
		for i, line := range lines {
			if width := lipgloss.Width(line); width != size[0] {
				t.Errorf("View(%d,%d) row %d width = %d", size[0], size[1], i, width)
			}
		}
		if lipgloss.Width(lines[size[1]-1]) != size[0] || !strings.Contains(lines[size[1]-1], lipgloss.RoundedBorder().BottomLeft) {
			t.Errorf("View(%d,%d) missing bottom border at requested bounds: %q", size[0], size[1], lines[size[1]-1])
		}
	}
}

func TestCardMasksSecret(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NETIVRI_ASCII", "0")
	t.Setenv("NO_COLOR", "1")
	c := NewCard("Details", []CardLine{{Label: "Password", Value: "sensitive", Kind: CardField, Secret: true}})
	got := c.View(30, 5)
	if strings.Contains(got, "sensitive") || !strings.Contains(got, "********") {
		t.Fatalf("secret view %q", got)
	}
	if !c.Update(tuiTestKey("enter")) {
		t.Fatal("secret field should remain editable")
	}
}

func TestCardNormalizationSkipsDisabledAndReadOnly(t *testing.T) {
	c := NewCard("", []CardLine{
		{Label: "disabled", Disabled: true},
		{Label: "readonly", Kind: CardReadOnly},
		{Label: "field", Kind: CardField},
	})
	if c.Selected != 2 {
		t.Fatalf("initial selection %d, want 2", c.Selected)
	}
	c.Selected = 0
	c.SetSize(20, 8)
	if c.Selected != 2 {
		t.Fatalf("normalized selection %d, want 2", c.Selected)
	}
	c.Lines[2].Disabled = true
	c.normalize()
	if c.Selected != 0 {
		t.Fatalf("all-ineligible fallback %d, want 0", c.Selected)
	}
}

func TestCardHotkeyOnlyActivatesEligibleMatchingLine(t *testing.T) {
	c := NewCard("", []CardLine{
		{Label: "Disabled", Kind: CardAction, Hotkey: "x", Disabled: true},
		{Label: "Visible", Kind: CardAction, Hotkey: "v"},
	})
	if c.Update(tuiTestKey("x")) || c.Selected != 1 {
		t.Fatalf("disabled hotkey activated or selection changed: %d", c.Selected)
	}
	if !c.Update(tuiTestKey("v")) || c.Selected != 1 {
		t.Fatalf("visible hotkey not activated: %d", c.Selected)
	}
	if c.Update(tuiTestKey("z")) {
		t.Fatal("unmatched key activated")
	}
}

func TestCardMouseFieldFirstClickAndActionSecondClick(t *testing.T) {
	c := NewCard("T", []CardLine{{Label: "Field", Kind: CardField}, {Label: "Action", Kind: CardAction}})
	c.SetSize(20, 6)
	// The title is the first body row, followed by the field and action.
	if c.Update(tea.MouseClickMsg{X: 10, Y: 2, Button: tea.MouseLeft}) || c.Selected != 0 {
		t.Fatal("first field click should select without activating")
	}
	if !c.Update(tea.MouseClickMsg{X: 10, Y: 2, Button: tea.MouseLeft}) || c.Selected != 0 {
		t.Fatal("field click on edit affordance should activate")
	}
	if c.Update(tea.MouseClickMsg{X: 4, Y: 3, Button: tea.MouseLeft}) || c.Selected != 1 {
		t.Fatal("first action click should select, not activate")
	}
	if !c.Update(tea.MouseClickMsg{X: 4, Y: 3, Button: tea.MouseLeft}) {
		t.Fatal("second action click should activate")
	}
}

func TestCardNavigationScrollsAcrossAllLines(t *testing.T) {
	lines := make([]CardLine, 8)
	for i := range lines {
		lines[i] = CardLine{Label: "line"}
	}
	c := NewCard("T", lines)
	c.SetSize(20, 6) // four visible rows, including the title.
	c.Focused = true
	c.Update(tuiTestKey("home"))
	c.Update(tuiTestKey("end"))
	if c.Selected != 0 {
		t.Fatalf("home selection = %d, want 0", c.Selected)
	}
	c.Update(tuiTestKey("pgdown"))
	if c.Selected != 4 || c.Scroll != 2 {
		t.Fatalf("first page-down selection/scroll = %d/%d", c.Selected, c.Scroll)
	}
	c.Update(tuiTestKey("pgdown"))
	if c.Selected != 7 || c.Scroll != 5 {
		t.Fatalf("second page-down selection/scroll = %d/%d", c.Selected, c.Scroll)
	}
	c.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if c.Selected != 7 || c.Scroll != 5 {
		t.Fatalf("wheel selection/scroll = %d/%d", c.Selected, c.Scroll)
	}
	c.Update(tuiTestKey("pgup"))
	if c.Selected != 3 || c.Scroll != 4 {
		t.Fatalf("page-up selection/scroll = %d/%d", c.Selected, c.Scroll)
	}
}

func TestCardTinyAndCommonDimensions(t *testing.T) {
	c := NewCard("T", []CardLine{{Label: "A"}})
	for _, size := range [][2]int{{1, 1}, {2, 2}, {8, 3}, {30, 6}} {
		got := c.View(size[0], size[1])
		for _, line := range strings.Split(got, "\n") {
			if width := lipgloss.Width(line); width > size[0] {
				t.Errorf("View(%d,%d) line width = %d", size[0], size[1], width)
			}
		}
	}
	if got := c.View(0, 4); got != "" {
		t.Fatalf("zero-width view %q", got)
	}
}

func TestCardFocusedMarkerHasStableASCIIAndNoColorPosition(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	c := NewCard("T", []CardLine{{Label: "A"}})
	c.Focused = true
	got := c.View(12, 4)
	if !strings.Contains(got, "› A") {
		t.Fatalf("unicode focused marker missing: %q", got)
	}
	t.Setenv("NETIVRI_ASCII", "1")
	got = c.View(12, 4)
	if !strings.Contains(got, "> A") {
		t.Fatalf("ASCII focused marker missing: %q", got)
	}
}
