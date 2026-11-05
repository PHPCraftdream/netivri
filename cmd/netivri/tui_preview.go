package main

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Preview countries step of the provider add wizard: before anything is
// stored it fetches the subscription the way `netivri sub countries --url
// {URL} [--via {PROXY}]` does and shows what it offers. The URL and the via
// proxy are credentials: neither appears in the step, its busy line or an
// error.

type previewData struct {
	nodes   int
	counts  []subscription.CountryCount
	unknown int
}

// addWizardSteps builds the steps of the provider add wizard. The wizard
// (startWizard) and ParityRefs both derive from this list, so no step key is
// a hand-kept string.
func (s *providersScreen) addWizardSteps() []tui.WizardStep {
	n := tui.NewPrompt(tui.TextPrompt, "Add provider — name", "Name", "Optional; inferred from URL if blank", "")
	u := tui.NewPrompt(tui.SecretPrompt, "Add provider — URL", "Subscription URL", "The URL is a credential and is hidden", "")
	v := tui.NewPrompt(tui.SecretPrompt, "Add provider — Via", "Via URL", "Optional proxy URL; hidden", "")
	return []tui.WizardStep{
		{Key: "name", Prompt: n, Optional: true},
		{Key: "url", Prompt: u},
		{Key: "via", Prompt: v, Optional: true},
		{Key: "preview", Body: s.previewBody},
	}
}

// previewIndex is the position of the preview step in the current wizard.
func (s *providersScreen) previewIndex() int {
	if s.wizard == nil {
		return -1
	}
	for i := range s.wizard.Steps {
		if s.wizard.Steps[i].Key == "preview" {
			return i
		}
	}
	return -1
}

// syncPreview starts the preview fetch when the wizard stands on the preview
// step and the entered URL and via have not been previewed yet.
func (s *providersScreen) syncPreview() tea.Cmd {
	if s.wizard == nil || s.mode != "add-wizard" || s.wizard.Review {
		return nil
	}
	if s.previewIndex() != s.wizard.Index {
		return nil
	}
	if s.previewCancel != nil {
		return nil
	}
	if s.previewState != "" && s.previewKey() == s.previewKeySeen {
		return nil
	}
	return s.startPreviewFetch()
}

func (s *providersScreen) previewKey() string {
	return s.wizard.Values["url"] + "\x00" + s.wizard.Values["via"]
}

func (s *providersScreen) startPreviewFetch() tea.Cmd {
	rawURL, via := s.wizard.Values["url"], s.wizard.Values["via"]
	s.previewState = "loading"
	s.previewCanceled = false
	s.previewKeySeen = s.previewKey()
	s.preview = previewData{}
	ctx, cancel := context.WithCancel(context.Background())
	s.previewCancel = cancel
	s.wizard.Busy = true
	s.wizard.BusyText = "Fetching subscription..."
	s.wizard.Error = nil
	s.busy = "Fetching subscription"
	return s.bg(s.busy, func() tea.Msg {
		n, c, u, e := ProviderCountriesURL(s.home, rawURL, via, func(_ context.Context, raw string, o fetch.Options) (*fetch.Result, error) {
			return s.fetchSafe(ctx, raw, o)
		})
		return providerResultMsg{kind: "preview", value: previewData{nodes: n, counts: c, unknown: u}, err: e}
	})
}

// previewBody renders the rows of the preview step: what the subscription
// offers, or the state of the fetch. No URL, no via proxy, no credential.
func (s *providersScreen) previewBody(width, height int) []string {
	slots := height - 5 // the wizard keeps rows 0-1 and the last three
	if slots < 1 {
		slots = 1
	}
	var lines []string
	add := func(x string) {
		if len(lines) < slots {
			lines = append(lines, x)
		}
	}
	switch s.previewState {
	case "":
		add("The subscription has not been fetched yet")
	case "loading":
		add("Fetching the subscription; Esc cancels the fetch")
	case "canceled":
		add("Preview canceled")
		add("Next continues the add; Back returns to the Via step")
	case "failed":
		add("The preview failed; Next adds the provider anyway")
	case "done":
		add(fmt.Sprintf("%s; %d with no country", previewNodesLabel(s.preview.nodes), s.preview.unknown))
		room := slots - len(lines) - 1 // keep a row for the "more" hint
		shown := 0
		for _, c := range s.preview.counts {
			if shown >= max(0, room) {
				break
			}
			add(fmt.Sprintf("%s — %s", c.Code, previewNodesLabel(c.Nodes)))
			shown++
		}
		if shown < len(s.preview.counts) {
			add(fmt.Sprintf("… and %d more countries", len(s.preview.counts)-shown))
		}
	}
	return lines
}

func previewNodesLabel(n int) string {
	if n == 1 {
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}

// clearPreview resets the preview step and stops an in-flight fetch.
func (s *providersScreen) clearPreview() {
	if s.previewCancel != nil {
		s.previewCancel()
		s.previewCancel = nil
	}
	s.previewCanceled = false
	s.previewState = ""
	s.previewKeySeen = ""
	s.preview = previewData{}
}
