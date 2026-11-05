package main

import "github.com/PHPCraftdream/netivri/internal/tui"

// parityScreen is implemented by the shell screens so the parity check
// (docs/design/parity.md) can read what a screen offers from the live widgets
// instead of from a hand-kept list.
type parityScreen interface{ ParityRefs() []string }

// parityRefs lists what a screen offers: the list, its `+ Add` row and every
// card line the user can act on (fields and actions), as "screen/id".
func parityRefs(screen string, list *tui.List, card *tui.Card) []string {
	refs := []string{screen + "/list"}
	if list != nil && list.AddLabel != "" {
		refs = append(refs, screen+"/add")
	}
	if card == nil {
		return refs
	}
	seen := map[string]bool{screen + "/add": true}
	for _, l := range card.Lines {
		if l.Kind == tui.CardReadOnly || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		refs = append(refs, screen+"/"+l.ID)
	}
	return refs
}

func (s *providersScreen) ParityRefs() []string { return parityRefs("providers", s.list, s.card) }
func (s *accountsScreen) ParityRefs() []string {
	return parityRefs("accounts", s.workspace.List, s.workspace.Card)
}
func (s *serversScreen) ParityRefs() []string {
	return parityRefs("servers", s.workspace.List, s.workspace.Card)
}
