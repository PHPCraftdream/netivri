package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type providersFetchFunc func(context.Context, string, fetch.Options) (*fetch.Result, error)

var providersFetch providersFetchFunc = fetch.Get
var providersNow = time.Now

type providerResultMsg struct {
	kind  string
	value any
	err   error
}
type providersScreen struct {
	home                                string
	env                                 tui.Env
	get                                 providersFetchFunc
	now                                 func() time.Time
	workspace                           *tui.Workspace
	list                                *tui.List
	card                                *tui.Card
	mode, busy, message, hint, selected string
	width, height                       int
	countries                           ProviderCountriesResult
	countryOffset                       int
	refreshLines                        []string
	refreshOffset                       int
	dialog                              tui.Modal
	wizard                              *tui.Wizard
	prompt                              *tui.Prompt
	confirmName                         string
	confirmDeps                         []string
	confirm                             *tui.Confirm
	addCancel                           context.CancelFunc
	addCancelRequested                  bool
	pendingSelect                       string
	providers                           map[string]ProviderInfo
	editField                           string
	pendingToast                        string
}

func newProvidersScreen(h string, e tui.Env) tui.Screen {
	return newProvidersScreenWith(h, e, providersFetch, providersNow)
}
func newProvidersScreenWith(h string, e tui.Env, g providersFetchFunc, n func() time.Time) *providersScreen {
	if e.Home != "" {
		h = e.Home
	}
	if g == nil {
		g = providersFetch
	}
	if n == nil {
		n = providersNow
	}
	l := tui.NewList([]tui.ListColumn{{Title: "Name", Width: 18, Flex: 1}, {Title: "Nodes", Width: 8}, {Title: "State", Width: 12}}, nil)
	l.Title = "Providers"
	l.EmptyText = "No providers yet. Add one to get started."
	l.AddLabel = "+ Add provider"
	c := tui.NewCard("Provider", []tui.CardLine{{ID: "name", Label: "Name", Kind: tui.CardReadOnly}, {ID: "subscription", Label: "Subscription", Kind: tui.CardReadOnly, Secret: true}, {ID: "via", Label: "Via", Kind: tui.CardReadOnly, Secret: true}, {ID: "nodes", Label: "Nodes", Kind: tui.CardReadOnly}, {ID: "countries", Label: "Countries", Kind: tui.CardReadOnly}, {ID: "updated", Label: "Updated", Kind: tui.CardReadOnly}, {ID: "edit-name", Label: "Edit name", Value: "renaming cannot safely update account references and stored snapshots", Kind: tui.CardAction, Disabled: true}, {ID: "edit-subscription", Label: "Edit subscription", Kind: tui.CardAction}, {ID: "edit-via", Label: "Edit Via", Kind: tui.CardAction}, {ID: "refresh", Label: "Refresh selected", Hotkey: "r", Kind: tui.CardAction}, {ID: "refresh-all", Label: "Refresh all", Hotkey: "R", Kind: tui.CardAction}, {ID: "countries", Label: "Countries", Hotkey: "c", Kind: tui.CardAction}, {ID: "remove", Label: "Remove", Hotkey: "d", Kind: tui.CardAction}})
	return &providersScreen{home: h, env: e, get: g, now: n, list: l, card: c, workspace: tui.NewWorkspace(l, c), providers: map[string]ProviderInfo{}}
}
func (s *providersScreen) Title() string        { return "Providers" }
func (s *providersScreen) CapturingInput() bool { return s.dialog != nil }
func (s *providersScreen) Breadcrumb() string {
	if s.width < 100 && s.workspace.Drilled {
		return "Providers / " + s.selected
	}
	return ""
}
func (s *providersScreen) FocusPane(reverse bool) bool {
	if s.dialog != nil || s.width < 100 {
		return false
	}
	if reverse {
		if s.workspace.Focus() == tui.PaneCard {
			s.workspace.SetFocus(tui.PaneList)
			return true
		}
		return false
	}
	if s.workspace.Focus() == tui.PaneList {
		s.workspace.SetFocus(tui.PaneCard)
		return true
	}
	return false
}
func (s *providersScreen) SetPaneFocus(p int) {
	if s.width >= 100 {
		s.workspace.SetFocus(p)
	}
}
func (s *providersScreen) Bindings() tui.ActionHints {
	b := append(tui.ActionHints{}, s.list.Bindings()...)
	b = append(b, s.card.Bindings()...)
	return append(b, key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")), key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh all")), key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "countries")), key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")))
}
func (s *providersScreen) KeyHints() string {
	if s.dialog != nil {
		if s.confirm != nil {
			return "Dialog: " + strings.TrimSpace(s.confirm.View(s.width, s.height))
		}
		return "Dialog: confirm or cancel"
	}
	if s.mode == "countries" {
		return "↑/↓ scroll  Esc back  r reload"
	}
	return "↑/↓ select  Enter details  a add  r refresh  R refresh all  c countries  d remove"
}
func (s *providersScreen) bg(label string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, w)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: w()} }
}
func (s *providersScreen) Init() tea.Cmd { return s.load() }
func (s *providersScreen) load() tea.Cmd {
	s.busy = "Loading providers"
	return s.bg(s.busy, func() tea.Msg { v, e := ProviderList(s.home); return providerResultMsg{"list", v, e} })
}
func (s *providersScreen) fetchSafe(c context.Context, raw string, o fetch.Options) (*fetch.Result, error) {
	r, e := s.get(c, raw, o)
	if e == nil {
		return r, nil
	}
	ce := providerCLIError(e)
	if ce == nil {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "provider fetch failed", Exit: cli.ExitNetwork}
	}
	x := *ce
	x.Message = redactProviderSecrets(x.Message, raw, o.Proxy)
	x.Hint = redactProviderSecrets(x.Hint, raw, o.Proxy)
	return nil, &x
}
func (s *providersScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
	if x, ok := m.(tea.WindowSizeMsg); ok {
		s.width = x.Width
		s.height = x.Height
		s.workspace.SetSize(x.Width, x.Height)
		return s, nil
	}
	if x, ok := m.(tui.ThemeMsg); ok {
		s.workspace.SetTheme(x.Theme)
		if s.wizard != nil {
			s.wizard.SetTheme(x.Theme)
		}
		if s.prompt != nil {
			s.prompt.SetTheme(x.Theme)
		}
		if s.confirm != nil {
			s.confirm.SetTheme(x.Theme)
		}
		return s, nil
	}
	if x, ok := m.(tui.BackgroundMsg); ok {
		m = x.Value
	}
	if x, ok := m.(providerResultMsg); ok {
		return s.result(x)
	}
	if x, ok := m.(tui.ConfirmResultMsg); ok {
		if s.mode != "confirm-remove" {
			return s, nil
		}
		s.dialog = nil
		s.confirm = nil
		s.mode = ""
		if x.Confirmed && !x.Canceled {
			return s, s.remove()
		}
		return s, nil
	}
	if x, ok := m.(tui.WizardResultMsg); ok {
		if x.Canceled {
			if s.addCancel != nil {
				s.addCancel()
				s.addCancelRequested = true
				if s.wizard != nil {
					s.wizard.SetError(errors.New("Cancel requested; waiting for provider operation to stop"))
				}
				return s, nil
			}
			s.dialog = nil
			s.wizard = nil
			s.prompt = nil
			s.mode = ""
			return s, nil
		}
		if s.mode == "add-wizard" {
			s.wizard.SetSaving(true)
			s.busy = "Adding provider"
			vals := x.Values
			ctx, cancel := context.WithCancel(context.Background())
			s.addCancel = cancel
			s.addCancelRequested = false
			return s, s.bg(s.busy, func() tea.Msg {
				v, e := ProviderAdd(s.home, ProviderAddOptions{Name: vals["name"], URL: vals["url"], Via: vals["via"]}, func(_ context.Context, raw string, o fetch.Options) (*fetch.Result, error) {
					return s.fetchSafe(ctx, raw, o)
				}, s.now)
				return providerResultMsg{"add", v, e}
			})
		}
		return s, nil
	}
	if x, ok := m.(tui.PromptResultMsg); ok && s.mode == "edit-prompt" {
		if x.Canceled {
			s.dialog = nil
			s.prompt = nil
			s.mode = ""
			return s, nil
		}
		value := x.Value
		if s.editField == "url" && strings.TrimSpace(value) == "" {
			s.prompt.SetError(errors.New("Subscription URL cannot be blank"))
			return s, nil
		}
		field := s.editField
		s.busy = "Saving provider"
		s.prompt.SetSaving(true)
		return s, s.bg(s.busy, func() tea.Msg {
			var o ProviderUpdateOptions
			if field == "url" {
				o.URL = &value
			} else {
				o.Via = &value
			}
			return providerResultMsg{"edit", nil, ProviderUpdate(s.home, s.selected, o)}
		})
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		return s, c
	}
	if s.mode == "countries" {
		return s.updateCountries(m)
	}
	if len(s.refreshLines) > 0 && s.mode == "results" {
		return s.updateResults(m)
	}
	if k, ok := m.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "a":
			s.startWizard()
			return s, nil
		case "r":
			if row, ok := s.list.SelectedRow(); ok {
				return s, s.refresh(row.Data.(string), false)
			}
			return s, s.load()
		case "R":
			return s, s.refresh("", true)
		case "d":
			if row, ok := s.list.SelectedRow(); ok {
				return s, s.preflight(row.Data.(string))
			}
			return s, nil
		case "c":
			if row, ok := s.list.SelectedRow(); ok {
				s.selected = row.Data.(string)
				s.mode = "countries"
				s.countryOffset = 0
				return s, s.countriesCmd()
			}
		case "esc":
			if s.workspace.Drilled {
				s.workspace.Update(m)
				return s, nil
			}
		case "enter":
			if s.width >= 100 && s.workspace.Focus() == tui.PaneList {
				if row, ok := s.list.SelectedRow(); ok {
					s.selected = row.Data.(string)
					s.updateCard()
				}
				return s, nil
			}
		}
	}
	activated := s.workspace.Update(m)
	if row, ok := s.list.SelectedRow(); ok && row.Data.(string) != s.selected {
		s.selected = row.Data.(string)
		s.updateCard()
	}
	if activated && s.list.IsAdd() {
		s.startWizard()
		return s, nil
	}
	if activated && s.workspace.Focus() == tui.PaneCard {
		return s.activateCard()
	}
	if activated && s.width < 100 && s.workspace.Drilled {
		s.selected = s.selectedName()
		s.updateCard()
	}
	return s, nil
}
func (s *providersScreen) selectedName() string {
	if r, ok := s.list.SelectedRow(); ok {
		return r.Data.(string)
	}
	return ""
}
func (s *providersScreen) startWizard() {
	n := tui.NewPrompt(tui.TextPrompt, "Add provider — name", "Name", "Optional; inferred from URL if blank", "")
	u := tui.NewPrompt(tui.SecretPrompt, "Add provider — URL", "Subscription URL", "The URL is a credential and is hidden", "")
	v := tui.NewPrompt(tui.SecretPrompt, "Add provider — Via", "Via URL", "Optional proxy URL; hidden", "")
	s.wizard = tui.NewWizard("Add provider", []tui.WizardStep{{Key: "name", Prompt: n, Optional: true}, {Key: "url", Prompt: u}, {Key: "via", Prompt: v, Optional: true}})
	s.dialog = s.wizard
	s.mode = "add-wizard"
}
func (s *providersScreen) startEditPrompt(field string) {
	s.editField = field
	label := "Via URL"
	if field == "url" {
		label = "Subscription URL"
	}
	s.prompt = tui.NewPrompt(tui.SecretPrompt, "Edit provider — "+label, label, "Credential hidden while editing", "")
	s.dialog = s.prompt
	s.mode = "edit-prompt"
}
func (s *providersScreen) startPrompt() {
	s.confirm = &tui.Confirm{Prompt: fmt.Sprintf("Remove %s? Accounts using this provider: %s", s.confirmName, strings.Join(s.confirmDeps, ", "))}
	s.dialog = s.confirm
	s.mode = "confirm-remove"
}
func (s *providersScreen) preflight(name string) tea.Cmd {
	s.confirmName = name
	s.busy = "Checking provider dependencies"
	return s.bg(s.busy, func() tea.Msg {
		as, e := AccountList(s.home)
		if e != nil {
			return providerResultMsg{"preflight", nil, e}
		}
		var d []string
		for _, a := range as {
			p, _ := config.ParseProviderList(a.Provider)
			if hasProvider(p, name) {
				d = append(d, a.Login)
			}
		}
		return providerResultMsg{"preflight", d, nil}
	})
}
func (s *providersScreen) remove() tea.Cmd {
	s.busy = "Removing provider"
	name := s.confirmName
	return s.bg(s.busy, func() tea.Msg { d, e := ProviderRemove(s.home, name); return providerResultMsg{"remove", d, e} })
}
func (s *providersScreen) refresh(n string, all bool) tea.Cmd {
	s.mode = "refreshing"
	s.busy = "Refreshing providers"
	return s.bg(s.busy, func() tea.Msg {
		r, e := ProviderRefresh(s.home, n, all, s.fetchSafe, s.now)
		return providerResultMsg{"refresh", r, e}
	})
}
func (s *providersScreen) countriesCmd() tea.Cmd {
	n := s.selected
	s.busy = "Loading countries"
	return s.bg(s.busy, func() tea.Msg { r, e := ProviderCountries(s.home, n); return providerResultMsg{"countries", r, e} })
}
func (s *providersScreen) result(r providerResultMsg) (screen tui.Screen, command tea.Cmd) {
	defer func() {
		if s.pendingToast != "" {
			notice := s.pendingToast
			s.pendingToast = ""
			if s.env.Toast != nil {
				command = tea.Batch(command, s.env.Toast(notice))
			}
		}
	}()
	screen = s
	s.busy = ""
	switch r.kind {
	case "list":
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
		} else {
			s.setProviders(r.value.(ProviderListResult))
			if s.pendingSelect != "" {
				for i, x := range s.list.Rows {
					if x.Data == s.pendingSelect {
						s.list.Selected = i
						break
					}
				}
				s.selected = s.pendingSelect
				s.pendingSelect = ""
			}
			if s.selected == "" {
				s.selected = s.selectedName()
			}
			s.updateCard()
		}
	case "add":
		if s.addCancel != nil {
			s.addCancel()
			s.addCancel = nil
		}
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
			if s.wizard != nil {
				s.wizard.SetSaving(false)
				s.wizard.SetError(errors.New(s.message + hintText(s.hint)))
			}
			s.mode = "add-wizard"
			return s, nil
		}
		x := r.value.(ProviderAddResult)
		s.message = fmt.Sprintf("Added %s: %d nodes", x.Name, x.Nodes)
		s.toast(s.message)
		s.pendingSelect = x.Name
		s.dialog = nil
		s.wizard = nil
		s.mode = ""
		return s, s.load()
	case "edit":
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
			if s.prompt != nil {
				s.prompt.SetSaving(false)
				s.prompt.SetError(errors.New(s.message + hintText(s.hint)))
			}
			return s, nil
		}
		s.message = "Provider updated"
		s.toast(s.message)
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		s.pendingSelect = s.selected
		return s, s.load()
	case "preflight":
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
			return s, nil
		}
		s.confirmDeps = r.value.([]string)
		s.startPrompt()
	case "remove":
		if r.err != nil {
			s.setError(r.err)
			if ce := providerCLIError(r.err); ce != nil && ce.Code == "SUB_IN_USE" {
				if d, ok := r.value.([]string); ok {
					s.message += "; accounts: " + strings.Join(d, ", ")
				}
			}
			s.toast(s.message)
		} else {
			s.message = "Provider removed"
			s.toast(s.message)
			s.hint = ""
			s.selected = ""
		}
		s.dialog = nil
		s.confirm = nil
		s.mode = ""
		return s, s.load()
	case "refresh":
		rs, _ := r.value.([]ProviderRefreshResult)
		s.refreshLines = nil
		for _, x := range rs {
			if x.OK {
				s.refreshLines = append(s.refreshLines, fmt.Sprintf("%s: refreshed (%d nodes)", x.Name, ptrInt(x.Nodes)))
			} else {
				m := "refresh failed"
				if x.Error != nil {
					m = x.Error["message"]
				}
				s.refreshLines = append(s.refreshLines, x.Name+": "+redactProviderSecrets(m))
			}
		}
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
		} else {
			s.message = "Providers refreshed"
			s.toast(s.message)
			s.hint = ""
		}
		s.mode = "results"
		return s, s.load()
	case "countries":
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
		} else {
			s.countries = r.value.(ProviderCountriesResult)
			s.countryOffset = 0
			s.message = ""
			s.hint = ""
		}
	}
	return s, nil
}
func (s *providersScreen) toast(text string) {
	if text != "" {
		s.pendingToast = s.scrub(text)
	}
}
func (s *providersScreen) updateCountries(m tea.Msg) (tui.Screen, tea.Cmd) {
	if k, ok := m.(tea.KeyPressMsg); ok {
		visible := max(1, s.height-4)
		maxoff := max(0, len(s.countries.Countries)-visible)
		switch k.String() {
		case "esc":
			s.mode = ""
			s.countryOffset = 0
		case "r":
			return s, s.countriesCmd()
		case "down", "j":
			s.countryOffset = min(maxoff, s.countryOffset+1)
		case "up", "k":
			s.countryOffset = max(0, s.countryOffset-1)
		case "pgdown":
			s.countryOffset = min(maxoff, s.countryOffset+visible)
		case "pgup":
			s.countryOffset = max(0, s.countryOffset-visible)
		case "home":
			s.countryOffset = 0
		case "end":
			s.countryOffset = maxoff
		}
	}
	return s, nil
}
func (s *providersScreen) updateResults(m tea.Msg) (tui.Screen, tea.Cmd) {
	if k, ok := m.(tea.KeyPressMsg); ok {
		step := max(1, s.height-3)
		switch k.String() {
		case "esc":
			s.mode = ""
			s.refreshLines = nil
			s.message = ""
		case "down", "j":
			s.refreshOffset = min(max(0, len(s.refreshLines)-1), s.refreshOffset+1)
		case "up", "k":
			s.refreshOffset = max(0, s.refreshOffset-1)
		case "pgdown":
			s.refreshOffset = min(max(0, len(s.refreshLines)-1), s.refreshOffset+step)
		case "pgup":
			s.refreshOffset = max(0, s.refreshOffset-step)
		case "home":
			s.refreshOffset = 0
		case "end":
			s.refreshOffset = max(0, len(s.refreshLines)-max(1, s.height-2))
		}
	}
	return s, nil
}
func (s *providersScreen) setProviders(v ProviderListResult) {
	s.providers = make(map[string]ProviderInfo, len(v.Providers))
	rows := make([]tui.Row, 0, len(v.Providers))
	for _, p := range v.Providers {
		s.providers[p.Name] = p
		nodes := "—"
		if p.Nodes != nil {
			nodes = fmt.Sprint(*p.Nodes)
		}
		rows = append(rows, tui.Row{Cells: []string{p.Name, nodes, p.State}, Data: p.Name})
	}
	s.list.SetRows(rows)
	if len(rows) > 0 && s.list.Selected >= len(rows) {
		s.list.Selected = len(rows) - 1
	}
}
func (s *providersScreen) updateCard() {
	name := s.selected
	if name == "" {
		s.card.Title = "Provider details"
		s.card.Info = []string{"Select a provider to view details"}
		return
	}
	p, ok := s.providers[name]
	if !ok {
		s.card.Title = "Provider details"
		s.card.Info = []string{"Details unavailable"}
		return
	}
	nodes := "—"
	if p.Nodes != nil {
		nodes = fmt.Sprint(*p.Nodes)
	}
	updated := "Never"
	if p.FetchedAt != nil {
		updated = *p.FetchedAt
	}
	countries := make([]string, 0, len(p.Countries))
	for _, c := range p.Countries {
		countries = append(countries, fmt.Sprintf("%s:%d", c.Code, c.Nodes))
	}
	s.card.Title = p.Name
	s.card.Info = []string{"State: " + p.State}
	s.card.Lines[0].Value = p.Name
	s.card.Lines[1].Value = map[bool]string{true: "Set", false: "Not set"}[p.Host != ""]
	s.card.Lines[2].Value = map[bool]string{true: "Configured", false: "Not set"}[p.ViaSet]
	s.card.Lines[3].Value = nodes
	s.card.Lines[4].Value = strings.Join(countries, ", ")
	s.card.Lines[5].Value = updated
}
func (s *providersScreen) activateCard() (tui.Screen, tea.Cmd) {
	switch s.card.Lines[s.card.Selected].ID {
	case "edit-subscription":
		s.startEditPrompt("url")
	case "edit-via":
		s.startEditPrompt("via")
	case "refresh":
		return s, s.refresh(s.selected, false)
	case "refresh-all":
		return s, s.refresh("", true)
	case "countries":
		s.mode = "countries"
		s.countryOffset = 0
		return s, s.countriesCmd()
	case "remove":
		return s, s.preflight(s.selected)
	}
	return s, nil
}
func (s *providersScreen) setError(e error) {
	if c := providerCLIError(e); c != nil {
		s.message = s.scrub(c.Message)
		s.hint = s.scrub(c.Hint)
	} else {
		s.message = "Provider operation failed"
		s.hint = ""
	}
}
func (s *providersScreen) scrub(v string) string {
	if s.wizard != nil {
		return redactProviderSecrets(v, s.wizard.Values["url"], s.wizard.Values["via"], s.currentPromptValue("url"), s.currentPromptValue("via"))
	}
	if s.prompt != nil {
		return redactProviderSecrets(v, s.prompt.Value)
	}
	return redactProviderSecrets(v)
}
func (s *providersScreen) currentPromptValue(k string) string {
	if s.wizard == nil || s.wizard.Index < 0 || s.wizard.Index >= len(s.wizard.Steps) {
		return ""
	}
	p := s.wizard.Steps[s.wizard.Index].Prompt
	if p == nil {
		return ""
	}
	if k == "url" && p.Kind == tui.SecretPrompt {
		return p.Value
	}
	if k == "via" && s.wizard.Index == 2 {
		return p.Value
	}
	return ""
}
func (s *providersScreen) View(w, h int) string {
	s.width = w
	s.height = h
	if w <= 0 || h <= 0 {
		return ""
	}
	if s.dialog != nil {
		return s.dialog.View(w, h)
	}
	if s.mode == "countries" {
		return s.viewCountries(w, h)
	}
	if s.mode == "results" {
		return s.viewResults(w, h)
	}
	return s.workspace.View(w, h)
}
func (s *providersScreen) viewCountries(w, h int) string {
	lines := []string{"Countries: " + s.selected}
	if s.busy != "" {
		lines = append(lines, s.busy+"…")
	} else if s.message != "" {
		lines = append(lines, s.scrub(s.message+hintText(s.hint)))
	} else if s.countries.Nodes == nil {
		lines = append(lines, "No stored snapshot")
	} else {
		lines = append(lines, fmt.Sprintf("%d nodes  Unknown: %d", *s.countries.Nodes, ptrInt(s.countries.Unknown)))
		vis := max(0, h-4)
		start := min(s.countryOffset, max(0, len(s.countries.Countries)-vis))
		end := min(len(s.countries.Countries), start+vis)
		for _, c := range s.countries.Countries[start:end] {
			lines = append(lines, fmt.Sprintf("%s: %d", c.Code, c.Nodes))
		}
		if len(s.countries.Countries) > vis && vis > 0 {
			lines = append(lines, fmt.Sprintf("%d-%d of %d", start+1, end, len(s.countries.Countries)))
		}
	}
	lines = append(lines, "Esc back  r reload")
	return boundedProvider(lines, w, h)
}
func (s *providersScreen) viewResults(w, h int) string {
	visible := max(1, h-1)
	start := min(s.refreshOffset, max(0, len(s.refreshLines)-visible))
	end := min(len(s.refreshLines), start+visible)
	lines := append([]string(nil), s.refreshLines[start:end]...)
	lines = append(lines, "PgUp/PgDn results  Esc back")
	return boundedProvider(lines, w, h)
}
func boundedProvider(lines []string, w, h int) string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, x := range lines {
		if lipgloss.Width(x) > w {
			lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(x)
		}
	}
	return strings.Join(lines, "\n")
}
func providerCLIError(e error) *cli.Error {
	var c *cli.Error
	if errors.As(e, &c) {
		return c
	}
	return nil
}
func ptrInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
func hasProvider(p []string, n string) bool {
	for _, x := range p {
		if x == n {
			return true
		}
	}
	return false
}
func hintText(h string) string {
	if h != "" {
		return " — " + h
	}
	return ""
}
func redactProviderSecrets(text string, raws ...string) string {
	var tokens []string
	for _, raw := range raws {
		if raw == "" {
			continue
		}
		tokens = append(tokens, raw)
		u, e := url.Parse(raw)
		if e != nil {
			continue
		}
		if u.User != nil {
			tokens = append(tokens, u.User.String(), u.User.Username())
			if p, ok := u.User.Password(); ok {
				tokens = append(tokens, p)
			}
		}
		tokens = append(tokens, u.Host, u.Hostname(), u.Path, u.RawPath, u.RawQuery, u.RawFragment)
		for _, vs := range u.Query() {
			tokens = append(tokens, vs...)
		}
	}
	sort.Slice(tokens, func(i, j int) bool { return len(tokens[i]) > len(tokens[j]) })
	pairs := []string{}
	last := ""
	for _, t := range tokens {
		if t != "" && t != last {
			pairs = append(pairs, t, "[redacted]")
			last = t
		}
	}
	if len(pairs) > 0 {
		text = strings.NewReplacer(pairs...).Replace(text)
	}
	return text
}
