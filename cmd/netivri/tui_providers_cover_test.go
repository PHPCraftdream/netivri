package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func TestProvidersCoverKeyHintsByState(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	if got := s.KeyHints(); !strings.Contains(got, "refresh") {
		t.Fatalf("default hints: %q", got)
	}
	s.startCountriesDialog(tuiProviderCountriesFixture())
	if got := s.KeyHints(); !strings.Contains(got, "Dialog") {
		t.Fatalf("countries dialog hints: %q", got)
	}
	s.dialog, s.prompt, s.mode = nil, nil, ""
	s.startWizard()
	if got := s.KeyHints(); !strings.Contains(got, "Dialog") {
		t.Fatalf("dialog hints: %q", got)
	}
}

func TestProvidersCoverBackgroundDispatch(t *testing.T) {
	// With env.Background set, bg delegates to it.
	s := newProvidersScreen(t.TempDir(), providerEnv(t.TempDir())).(*providersScreen)
	c := s.bg("label-a", func() tea.Msg { return providerResultMsg{} })
	if c == nil {
		t.Fatal("background cmd nil with env.Background")
	}
	// Without env.Background, bg wraps the work inline as a BackgroundMsg.
	s2 := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	c2 := s2.bg("label-b", func() tea.Msg { return "payload" })
	if c2 == nil {
		t.Fatal("inline cmd nil")
	}
	m := c2()
	b, ok := m.(tui.BackgroundMsg)
	if !ok || b.Label != "label-b" || b.Value != "payload" {
		t.Fatalf("inline bg delivered %T %+v", m, m)
	}
}

func TestProvidersCoverWindowSizeAndTheme(t *testing.T) {
	h := providerSeed(t)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 110, Height: 33})
	if s.width != 110 || s.height != 33 || s.workspace.Width != 110 || s.workspace.Height != 33 {
		t.Fatalf("window size not applied: %d %d %d %d", s.width, s.height, s.workspace.Width, s.workspace.Height)
	}
	s.startWizard()
	light := tui.NewThemeFor(false)
	_, _ = s.Update(tui.ThemeMsg{Theme: light})
	if s.wizard == nil || s.wizard.Theme.Dark {
		t.Fatal("theme not applied to wizard")
	}
}

func TestProvidersCoverThemeAppliedToPromptAndConfirm(t *testing.T) {
	h := providerSeed(t)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	light := tui.NewThemeFor(false)
	s.startEditPrompt("url")
	_, _ = s.Update(tui.ThemeMsg{Theme: light})
	if s.prompt == nil || s.prompt.Theme.Dark {
		t.Fatal("theme not applied to prompt")
	}
	s.confirmName, s.confirmDeps = "p", nil
	s.startPrompt()
	_, _ = s.Update(tui.ThemeMsg{Theme: light})
	if s.confirm == nil || s.confirm.Theme.Dark {
		t.Fatal("theme not applied to confirm")
	}
}

func TestProvidersCoverConfirmRemoveConfirmed(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "gone", URL: "https://gone.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.confirmName = "gone"
	s.confirmDeps = nil
	s.startPrompt()
	_, c := s.Update(tui.ConfirmResultMsg{Confirmed: true})
	if c == nil {
		t.Fatal("confirmed remove produced no command")
	}
	drainProvider(s, c)
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range cfg.Subscriptions {
		if sub.Name == "gone" {
			t.Fatal("confirmed remove did not delete provider")
		}
	}
	if s.dialog != nil || s.mode != "" {
		t.Fatal("confirm dialog not closed after confirmed remove")
	}
}

func TestProvidersCoverConfirmRemoveDeclined(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "keep", URL: "https://keep.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.confirmName = "keep"
	s.startPrompt()
	_, c := s.Update(tui.ConfirmResultMsg{Confirmed: false})
	if c != nil {
		t.Fatal("declined remove should not schedule work")
	}
	if s.dialog != nil || s.mode != "" {
		t.Fatal("declined remove did not close the dialog only")
	}
	cfg, err := loadConfig(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Subscriptions) != 1 {
		t.Fatal("declined remove deleted provider")
	}
}

func TestProvidersCoverWizardCancelWithAddCancel(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.startWizard()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.addCancel = cancel
	s.mode = "add-wizard"
	_, _ = s.Update(tui.WizardResultMsg{Canceled: true})
	if !s.addCancelRequested || s.wizard == nil {
		t.Fatal("cancel with pending add did not keep the wizard")
	}
	if s.wizard.Error == nil || !strings.Contains(s.wizard.Error.Error(), "Cancel requested") {
		if s.wizard.Error != nil {
			t.Fatalf("wizard error: %v", s.wizard.Error)
		}
		t.Fatal("cancel error not set on wizard")
	}
}

func TestProvidersCoverPromptEditCancelAndBlankURL(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.startEditPrompt("url")
	_, c := s.Update(tui.PromptResultMsg{Canceled: true})
	if c != nil {
		t.Fatal("canceled edit prompt scheduled work")
	}
	if s.dialog != nil || s.prompt != nil || s.mode != "" {
		t.Fatal("canceled edit prompt did not close")
	}
	s.startEditPrompt("url")
	_, c = s.Update(tui.PromptResultMsg{Value: "   "})
	if c != nil {
		t.Fatal("blank URL edit scheduled work")
	}
	if s.prompt == nil || s.mode != "edit-prompt" {
		t.Fatal("blank URL edit did not stay in the prompt")
	}
	if s.prompt.Error == nil || !strings.Contains(s.prompt.Error.Error(), "cannot be blank") {
		if s.prompt.Error != nil {
			t.Fatalf("prompt error: %v", s.prompt.Error)
		}
		t.Fatal("blank URL error not set")
	}
}

func TestProvidersCoverStartDialogState(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.startWizard()
	if s.mode != "add-wizard" || s.dialog == nil || s.wizard == nil {
		t.Fatal("startWizard state")
	}
	s.dialog, s.wizard = nil, nil
	s.startEditPrompt("via")
	if s.mode != "edit-prompt" || s.dialog == nil || s.prompt == nil || s.editField != "via" {
		t.Fatal("startEditPrompt state")
	}
	s.dialog, s.prompt = nil, nil
	s.confirmName = "p"
	s.startPrompt()
	if s.mode != "confirm-remove" || s.dialog == nil || s.confirm == nil {
		t.Fatal("startPrompt state")
	}
}

func TestProvidersCoverFocusPaneAndSetPaneFocus(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.startWizard()
	if s.FocusPane(false) {
		t.Fatal("FocusPane should refuse while a dialog is open")
	}
	s.dialog = nil
	s.workspace.SetFocus(tui.PaneList)
	if !s.FocusPane(false) || s.workspace.Focus() != tui.PaneCard {
		t.Fatal("forward FocusPane from list did not move to card")
	}
	if s.FocusPane(false) {
		t.Fatal("FocusPane forward should refuse at the card pane")
	}
	s.workspace.SetFocus(tui.PaneCard)
	if !s.FocusPane(true) || s.workspace.Focus() != tui.PaneList {
		t.Fatal("reverse FocusPane from card did not move to list")
	}
	s.SetPaneFocus(int(tui.PaneCard))
	if s.workspace.Focus() != tui.PaneCard {
		t.Fatal("SetPaneFocus did not apply at wide width")
	}
	narrow := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	narrow.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	narrow.SetPaneFocus(int(tui.PaneCard))
	if narrow.workspace.Focus() != tui.PaneList {
		t.Fatal("narrow SetPaneFocus should be ignored")
	}
	if narrow.FocusPane(false) {
		t.Fatal("narrow FocusPane should refuse")
	}
}

func TestProvidersCoverEditResultSuccess(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.startEditPrompt("url")
	sc, c := s.result(providerResultMsg{kind: "edit", value: nil, err: nil})
	if s.mode != "" || s.dialog != nil || s.prompt != nil || sc != s || c == nil {
		t.Fatal("successful edit result did not clear mode and return load cmd")
	}
}

func TestProvidersCoverPreflightResultOpensConfirm(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	_, _ = s.Update(providerResultMsg{kind: "preflight", value: []string{"alice", "bob"}, err: nil})
	if s.mode != "confirm-remove" || s.dialog == nil || s.confirm == nil {
		t.Fatal("preflight success did not open confirm dialog")
	}
	if !strings.Contains(s.confirm.Prompt, "alice") || !strings.Contains(s.confirm.Prompt, "bob") {
		t.Fatalf("confirm prompt missing dependency names: %q", s.confirm.Prompt)
	}
}

func TestProvidersCoverCountriesDialogKeys(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.selected = "p"
	s.startCountriesDialog(tuiProviderCountriesFixture())
	key := func(m tea.KeyPressMsg) { _, _ = s.Update(m) }
	key(tea.KeyPressMsg{Code: tea.KeyDown})
	selectedHas := func(v string) bool {
		for _, x := range s.prompt.Selected {
			if x == v {
				return true
			}
		}
		return false
	}
	key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !selectedHas("NL") {
		t.Fatalf("space after Down did not toggle NL: %v", s.prompt.Selected)
	}
	key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	for _, v := range s.prompt.Selected {
		if v == "NL" {
			t.Fatal("second space did not untoggle NL")
		}
	}
	// Esc returns a result command; the screen closes the dialog on its message.
	_, escCmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if escCmd != nil {
		if m := escCmd(); m != nil {
			_, _ = s.Update(m)
		}
	}
	if s.mode != "" || s.dialog != nil || s.prompt != nil {
		t.Fatal("esc did not leave countries dialog")
	}
}

func tuiProviderCountriesFixture() ProviderCountriesResult {
	n := 2
	u := 1
	return ProviderCountriesResult{Name: "p", Nodes: &n, Countries: []subscription.CountryCount{{Code: "DE", Nodes: 2}, {Code: "NL", Nodes: 1}}, Unknown: &u}
}

func TestProvidersCoverResultsPagingKeys(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	s.mode = "results"
	s.refreshLines = []string{"a", "b", "c"}
	s.height = 10
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.refreshOffset != 1 {
		t.Fatalf("down offset %d", s.refreshOffset)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.refreshOffset != 2 {
		t.Fatalf("pgdown offset %d", s.refreshOffset)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if s.refreshOffset != 1 {
		t.Fatalf("up offset %d", s.refreshOffset)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if s.refreshOffset != 0 {
		t.Fatalf("home offset %d", s.refreshOffset)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if s.refreshOffset != 0 {
		t.Fatalf("end offset %d", s.refreshOffset)
	}
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.mode != "" || s.refreshLines != nil {
		t.Fatal("esc did not clear results view")
	}
}

func TestProvidersCoverActivateCardBranches(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected = "p"
	s.providers = map[string]ProviderInfo{"p": {Name: "p"}}
	lineIdx := func(id string) int {
		for i, l := range s.card.Lines {
			if l.ID == id {
				return i
			}
		}
		t.Fatalf("card line %q missing", id)
		return 0
	}
	s.card.Selected = lineIdx("refresh")
	if _, c := s.activateCard(); c == nil {
		t.Fatal("refresh branch returned no cmd")
	}
	s.mode = ""
	s.card.Selected = lineIdx("refresh-all")
	if _, c := s.activateCard(); c == nil {
		t.Fatal("refresh-all branch returned no cmd")
	}
	s.mode = ""
	s.card.Selected = lineIdx("countries")
	if _, c := s.activateCard(); c == nil {
		t.Fatal("countries branch returned no cmd")
	}
	s.mode = ""
	s.card.Selected = lineIdx("remove")
	if _, c := s.activateCard(); c == nil {
		t.Fatal("remove branch returned no preflight cmd")
	}
	s.card.Selected = lineIdx("edit-subscription")
	if _, _ = s.activateCard(); s.mode != "edit-prompt" || s.editField != "url" || s.dialog == nil {
		t.Fatal("edit-subscription did not open prompt")
	}
	s.dialog = nil
	s.card.Selected = lineIdx("edit-via")
	if _, _ = s.activateCard(); s.mode != "edit-prompt" || s.editField != "via" || s.dialog == nil {
		t.Fatal("edit-via did not open prompt")
	}
}

func TestProvidersCoverViewHeightsDialogAndScrub(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.selected = "p"
	s.providers = map[string]ProviderInfo{"p": {Name: "p"}}
	s.startCountriesDialog(tuiProviderCountriesFixture())
	for _, hgt := range []int{6, 12, 20} {
		v := s.View(80, hgt)
		if strings.Count(v, "\n")+1 != hgt {
			t.Fatalf("countries dialog view height %d != %d", strings.Count(v, "\n")+1, hgt)
		}
	}
	s.dialog = nil
	s.prompt = nil
	s.mode = ""
	s.mode = "results"
	s.refreshLines = []string{"one: refreshed (1 nodes)", "two: refreshed (2 nodes)"}
	for _, hgt := range []int{3, 9} {
		v := s.View(80, hgt)
		if strings.Count(v, "\n")+1 != hgt {
			t.Fatalf("results view height %d != %d", strings.Count(v, "\n")+1, hgt)
		}
	}
	s.mode = ""
	s.startWizard()
	if v := s.View(100, 24); !strings.Contains(v, "Add provider") {
		t.Fatal("dialog view passthrough failed")
	}
	s.dialog = nil
	s.wizard = nil
	s.startEditPrompt("url")
	s.prompt.Value = "https://u:hush-secret@quiet.invalid"
	got := s.scrub("fetch failed at hush-secret")
	if strings.Contains(got, "hush-secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("scrub with prompt did not redact: %q", got)
	}
}
