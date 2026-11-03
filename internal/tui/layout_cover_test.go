package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWorkspaceCoverSetThemeNilSafeAndSetFocusValidation(t *testing.T) {
	// layout.go 29-36 + 46-51: SetTheme tolerates nil panes; SetFocus ignores invalid panes.
	empty := &Workspace{}
	empty.SetTheme(NewTheme()) // must not panic with nil List/Card
	if empty.Theme.Accent == nil {
		t.Fatal("SetTheme did not store theme: Accent is nil")
	}
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Details", nil))
	w.SetFocus(7)
	if w.Focus() != PaneList {
		t.Fatalf("SetFocus(7) changed focus to %d, want %d", w.Focus(), PaneList)
	}
	if !w.List.Focused || w.Card.Focused {
		t.Fatalf("focus flags wrong after invalid pane: list=%t card=%t", w.List.Focused, w.Card.Focused)
	}
	w.SetFocus(PaneCard)
	if w.Focus() != PaneCard || !w.Card.Focused {
		t.Fatalf("SetFocus(PaneCard): focus=%d cardFocused=%t", w.Focus(), w.Card.Focused)
	}
	NewWorkspace(nil, nil).SetTheme(NewTheme()) // must not panic
}

func TestWorkspaceCoverCycleFocusNarrow(t *testing.T) {
	// layout.go 56-73: narrow CycleFocus depends on Drilled; wide depends on direction; 0 is a no-op.
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Details", nil))
	w.SetSize(80, 6)
	w.Drilled = true
	w.CycleFocus(1)
	if w.Focus() != PaneCard {
		t.Fatalf("narrow drilled CycleFocus(1) focus = %d, want %d", w.Focus(), PaneCard)
	}
	w.Drilled = false
	w.CycleFocus(1)
	if w.Focus() != PaneList {
		t.Fatalf("narrow undrilled CycleFocus(1) focus = %d, want %d", w.Focus(), PaneList)
	}
	w.SetSize(120, 6)
	w.SetFocus(PaneList)
	w.CycleFocus(0)
	if w.Focus() != PaneList {
		t.Fatalf("CycleFocus(0) moved focus to %d, want %d", w.Focus(), PaneList)
	}
	w.CycleFocus(1)
	if w.Focus() != PaneCard {
		t.Fatalf("wide CycleFocus(1) focus = %d, want %d", w.Focus(), PaneCard)
	}
	w.CycleFocus(-1)
	if w.Focus() != PaneList {
		t.Fatalf("wide CycleFocus(-1) focus = %d, want %d", w.Focus(), PaneList)
	}
}

func TestWorkspaceCoverBindingsContent(t *testing.T) {
	// layout.go 106-108: Bindings exposes tab / shift+tab / esc / enter.
	got := map[string]string{}
	for _, b := range NewWorkspace(nil, nil).Bindings() {
		for _, k := range b.Keys() {
			got[k] = b.Help().Desc
		}
	}
	want := map[string]string{"tab": "next pane", "shift+tab": "previous pane", "esc": "back", "enter": "open / activate"}
	for k, desc := range want {
		if got[k] != desc {
			t.Fatalf("binding %q desc = %q, want %q", k, got[k], desc)
		}
	}
	if len(got) != 4 {
		t.Fatalf("Bindings produced %d keys, want 4", len(got))
	}
}

func TestWorkspaceCoverNarrowEscAndEnterEdges(t *testing.T) {
	// layout.go 127-153: esc undrills (incl. card focus without drill); enter past end / nil list returns false.
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Details", nil))
	w.SetSize(80, 6)
	if w.Update(tuiTestKey("esc")) || w.Drilled || w.Focus() != PaneList {
		t.Fatalf("esc on list-focused workspace: activated=%t drilled=%t focus=%d", false, w.Drilled, w.Focus())
	}
	w.SetFocus(PaneCard)
	if w.Update(tuiTestKey("esc")) || w.Drilled || w.Focus() != PaneList {
		t.Fatalf("esc from card focus: drilled=%t focus=%d, want false/%d", w.Drilled, w.Focus(), PaneList)
	}
	empty := NewWorkspace(NewList(nil, nil), NewCard("Details", nil))
	empty.List.AddLabel = ""
	empty.SetSize(80, 6)
	if empty.Update(tuiTestKey("enter")) || empty.Drilled {
		t.Fatalf("enter past end of rows: activated=%t drilled=%t, want false/false", true, empty.Drilled)
	}
	var nilList Workspace
	if nilList.Update(tuiTestKey("enter")) {
		t.Fatal("enter with nil List reported activation, want false")
	}
	add := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), NewCard("Details", nil))
	add.List.Selected = 1 // Add row
	add.SetSize(80, 6)
	if !add.Update(tuiTestKey("enter")) || add.Drilled {
		t.Fatalf("enter on Add: activated=%t drilled=%t, want true/false", add.Drilled, add.Drilled)
	}
}

func TestWorkspaceCoverWideMouseEdgesAndWheelTranslation(t *testing.T) {
	// layout.go 156-187: divider and out-of-bounds clicks ignored; wheel translated to the right pane.
	list := NewList(nil, []Row{{Cells: []string{"one"}}, {Cells: []string{"two"}}})
	card := NewCard("T", nil)
	for i := 0; i < 8; i++ {
		card.Lines = append(card.Lines, CardLine{Label: "L", Kind: CardField})
	}
	w := NewWorkspace(list, card)
	w.SetSize(120, 8) // left = 42, divider x=42, right pane starts x=43
	if w.Update(tea.MouseClickMsg{X: 42, Y: 2, Button: tea.MouseLeft}) || w.Focus() != PaneList {
		t.Fatalf("divider click: activated=%t focus=%d, want false/%d", true, w.Focus(), PaneList)
	}
	if w.Update(tea.MouseClickMsg{X: 200, Y: 2, Button: tea.MouseLeft}) {
		t.Fatal("click with X beyond width reported activation, want false")
	}
	if w.Update(tea.MouseClickMsg{X: 2, Y: 50, Button: tea.MouseLeft}) {
		t.Fatal("click with Y beyond height reported activation, want false")
	}
	if w.Update(tea.MouseWheelMsg{X: 200, Y: 2, Button: tea.MouseWheelDown}) {
		t.Fatal("wheel with X beyond width reported activation, want false")
	}
	if w.Update(tea.MouseWheelMsg{X: 44, Y: 2, Button: tea.MouseWheelDown}) || w.Focus() != PaneCard || card.Scroll != 1 {
		t.Fatalf("right wheel: activated=%t focus=%d cardScroll=%d, want false/%d/1", w.Focus() != PaneList, w.Focus(), card.Scroll, PaneCard)
	}
	if w.Update(tea.MouseWheelMsg{X: 10, Y: 2, Button: tea.MouseWheelDown}) || w.Focus() != PaneList || list.Selected != 1 {
		t.Fatalf("left wheel: focus=%d selected=%d, want %d/1", w.Focus(), list.Selected, PaneList)
	}
}

func TestWorkspaceCoverNarrowDrilledRoutesKeysToCard(t *testing.T) {
	// layout.go 193-195: narrow drilled workspace routes keys to the Card, not the List.
	card := NewCard("Details", []CardLine{{Label: "A", Kind: CardField}, {Label: "B", Kind: CardField}})
	w := NewWorkspace(NewList(nil, []Row{{Cells: []string{"row"}}}), card)
	w.SetSize(80, 6)
	if w.Update(tuiTestKey("enter")) || !w.Drilled {
		t.Fatalf("enter did not drill: activated=%t drilled=%t", w.Drilled, w.Drilled)
	}
	if w.Update(tuiTestKey("down")) {
		t.Fatal("down while drilled reported activation, want false")
	}
	if card.Selected != 1 {
		t.Fatalf("card.Selected = %d, want 1 (key routed to card)", card.Selected)
	}
	if w.List.Selected != 0 {
		t.Fatalf("list.Selected = %d, want 0 (key must not reach list)", w.List.Selected)
	}
}

func TestWorkspaceCoverNarrowMouseSelectThenDrill(t *testing.T) {
	// layout.go 196-207: first click selects a row, second click on the selected row drills.
	list := NewList(nil, []Row{{Cells: []string{"one"}}, {Cells: []string{"two"}}})
	w := NewWorkspace(list, NewCard("Details", nil))
	w.SetSize(80, 8)
	if w.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft}) || w.Drilled || list.Selected != 1 {
		t.Fatalf("first click: activated=%t drilled=%t selected=%d, want false/false/1", w.Drilled, w.Drilled, list.Selected)
	}
	if w.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft}) {
		t.Fatal("second click on selected row reported activation, want false (narrow drill)")
	}
	if !w.Drilled || w.Focus() != PaneCard {
		t.Fatalf("second click: drilled=%t focus=%d, want true/%d", w.Drilled, w.Focus(), PaneCard)
	}
}

func TestWorkspaceCoverViewBranches(t *testing.T) {
	// layout.go 210-222: zero sizes give empty view; narrow shows list, drilled shows card.
	w := NewWorkspace(NewList([]ListColumn{{Title: "Name"}}, []Row{{Cells: []string{"alpha"}}}), NewCard("Details", nil))
	if got := w.View(0, 5); got != "" {
		t.Fatalf("View(0,5) = %q, want empty", got)
	}
	if got := w.View(80, 0); got != "" {
		t.Fatalf("View(80,0) = %q, want empty", got)
	}
	narrow := w.View(80, 6)
	if !strings.Contains(narrow, "alpha") {
		t.Fatalf("narrow undrilled view missing list row: %q", narrow)
	}
	if strings.Contains(narrow, "Details") {
		t.Fatalf("narrow undrilled view leaked card: %q", narrow)
	}
	w.Update(tuiTestKey("enter"))
	drilled := w.View(80, 6)
	if !strings.Contains(drilled, "Details") {
		t.Fatalf("narrow drilled view missing card: %q", drilled)
	}
	if strings.Contains(drilled, "alpha") {
		t.Fatalf("narrow drilled view leaked list: %q", drilled)
	}
}
