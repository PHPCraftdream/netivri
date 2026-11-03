package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWorkspaceResponsiveAndFocusCycle(t *testing.T) {
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Card", nil))
	w.SetSize(120, 6)
	if w.List.width != 42 || w.Card.width != 77 {
		t.Fatalf("wide pane widths %d/%d", w.List.width, w.Card.width)
	}
	w.CycleFocus(1)
	if w.Focus() != PaneCard || !w.Card.Focused || w.List.Focused {
		t.Fatal("wide focus did not move to card")
	}
	w.CycleFocus(-1)
	if w.Focus() != PaneList {
		t.Fatal("wide reverse focus did not move to list")
	}
	w.SetSize(80, 6)
	if w.List.width != 80 || w.Card.width != 80 {
		t.Fatalf("narrow pane widths %d/%d", w.List.width, w.Card.width)
	}
	if got := w.View(80, 6); strings.Contains(got, "Card") {
		t.Fatalf("narrow list view contains card: %q", got)
	}
	if w.Update(tuiTestKey("tab")) || w.Focus() != PaneList {
		t.Fatal("narrow Tab should not change focus or activate")
	}
	if w.Update(tuiTestKey("enter")) || !w.Drilled || w.Focus() != PaneCard {
		t.Fatal("narrow enter should drill into a regular row")
	}
	if !strings.Contains(w.View(80, 6), "Card") {
		t.Fatal("drilled view does not show card")
	}
	if w.Update(tuiTestKey("esc")) || w.Drilled || w.Focus() != PaneList {
		t.Fatal("escape should return to the list without activating")
	}
}

func TestWorkspaceAddActivationOnlyOnEnter(t *testing.T) {
	list := NewList(nil, []Row{{Cells: []string{"row"}}})
	list.Selected = 1 // Add row
	w := NewWorkspace(list, NewCard("Details", nil))
	w.SetSize(80, 8)
	if !w.Update(tuiTestKey("enter")) {
		t.Fatal("enter on Add should report activation")
	}
	if w.Drilled {
		t.Fatal("Add activation should not drill")
	}
	list.Selected = 0
	if w.Update(tuiTestKey("enter")) || !w.Drilled {
		t.Fatal("regular row should drill without reporting activation")
	}
	if w.Update(tuiTestKey("tab")) || w.Update(tuiTestKey("esc")) {
		t.Fatal("navigation keys must not report activation")
	}
}

func TestWorkspaceWideMouseTranslationFocusAndWheel(t *testing.T) {
	list := NewList(nil, []Row{{Cells: []string{"row"}}})
	card := NewCard("T", []CardLine{{Label: "Field", Kind: CardField}, {Label: "Action", Kind: CardAction}})
	w := NewWorkspace(list, card)
	w.SetSize(120, 8) // left pane 42, divider x=42, right pane starts x=43
	if w.Update(tea.MouseClickMsg{X: 47, Y: 2, Button: tea.MouseLeft}) || w.Focus() != PaneCard || card.Selected != 0 {
		t.Fatal("right-pane click did not translate and focus card field")
	}
	if !w.Update(tea.MouseClickMsg{X: 47, Y: 2, Button: tea.MouseLeft}) || w.Focus() != PaneCard {
		t.Fatal("second click on translated field row should activate")
	}
	card.Selected = 0
	card.SetSize(card.width, 3)
	card.Lines = append(card.Lines, CardLine{Label: "Extra", Kind: CardField}, CardLine{Label: "More", Kind: CardField})
	beforeSelected := card.Selected
	beforeScroll := card.Scroll
	w.Update(tea.MouseWheelMsg{X: 44, Button: tea.MouseWheelDown})
	if card.Selected != beforeSelected || card.Scroll == beforeScroll {
		t.Fatalf("wheel selected=%d scroll=%d, want selection unchanged and scroll moved", card.Selected, card.Scroll)
	}
	if w.Focus() != PaneCard {
		t.Fatal("right-pane mouse interaction lost focus")
	}
}

func TestWorkspaceMouseSelectedListRowDoesNotActivate(t *testing.T) {
	list := NewList(nil, []Row{{Cells: []string{"row"}}})
	w := NewWorkspace(list, NewCard("Details", nil))
	w.SetSize(120, 8)
	if w.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft}) {
		t.Fatal("first row click should only select")
	}
	// Spec: clicking a list row selects and, on the second click, activates it.
	if !w.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft}) {
		t.Fatal("second click on the selected row should activate it")
	}
	if w.Drilled {
		t.Fatal("mouse click unexpectedly drilled into row")
	}
}

func TestWorkspaceNarrowAddAndNavigation(t *testing.T) {
	list := NewList(nil, nil)
	w := NewWorkspace(list, NewCard("Details", nil))
	w.SetSize(80, 6)
	if w.Update(tuiTestKey("tab")) || w.Focus() != PaneList {
		t.Fatal("Tab should remain list-focused when narrow")
	}
	if !w.Update(tuiTestKey("enter")) {
		t.Fatal("enter on Add should activate")
	}
	if w.Drilled {
		t.Fatal("Add should not drill")
	}
}

func TestWorkspaceTinyAndCommonDimensions(t *testing.T) {
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Details", nil))
	for _, size := range [][2]int{{1, 1}, {2, 2}, {80, 24}, {120, 24}} {
		got := w.View(size[0], size[1])
		if len(strings.Split(got, "\n")) != size[1] {
			t.Errorf("View(%d,%d) line count = %d", size[0], size[1], len(strings.Split(got, "\n")))
		}
	}
	if got := w.View(0, 4); got != "" {
		t.Fatalf("zero-width view %q", got)
	}
}
