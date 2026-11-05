package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
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
	nodesList                           func(string, string, string) ([]NodeEntry, error)
	dialNode                            func(context.Context, string, string, int) (core.NodeDiag, error)
	nodesDlg                            *nodesDialog
	diagDlg                             *diagResultDialog
	diagLabel                           string
	diagCancel                          context.CancelFunc
	diagCanceled                        bool
	preview                             previewData
	previewState                        string // "", "loading", "done", "failed" or "canceled"
	previewKeySeen                      string
	previewCancel                       context.CancelFunc
	previewCanceled                     bool
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
	l.EmptyText = "A provider is a subscription of proxy nodes. Add one to start."
	l.AddLabel = "+ Add provider"
	c := tui.NewCard("Provider", []tui.CardLine{{ID: "name", Label: "Name", Kind: tui.CardReadOnly}, {ID: "subscription", Label: "Subscription", Kind: tui.CardField}, {ID: "via", Label: "Via", Kind: tui.CardField}, {ID: "nodes", Label: "Nodes", Kind: tui.CardReadOnly}, {ID: "countries", Label: "Countries", Kind: tui.CardReadOnly}, {ID: "updated", Label: "Updated", Kind: tui.CardReadOnly}, {ID: "pre", Label: "Pre-hop", Kind: tui.CardField}, {ID: "post", Label: "Post-hop", Kind: tui.CardField}, {ID: "edit-name", Label: "Rename", Kind: tui.CardAction}, {ID: "edit-subscription", Label: "Edit subscription", Kind: tui.CardAction}, {ID: "edit-via", Label: "Edit Via", Kind: tui.CardAction}, {ID: "clear-pre", Label: "Clear pre-hop", Hotkey: "P", Kind: tui.CardAction}, {ID: "clear-post", Label: "Clear post-hop", Hotkey: "O", Kind: tui.CardAction}, {ID: "refresh", Label: "Refresh selected", Hotkey: "r", Kind: tui.CardAction}, {ID: "refresh-all", Label: "Refresh all", Hotkey: "R", Kind: tui.CardAction}, {ID: "countries", Label: "Countries", Hotkey: "c", Kind: tui.CardAction}, {ID: "nodes", Label: "Nodes...", Hotkey: "n", Kind: tui.CardAction}, {ID: "diagnose", Label: "Diagnose...", Hotkey: "g", Kind: tui.CardAction}, {ID: "remove", Label: "Remove", Hotkey: "d", Kind: tui.CardAction}})
	ws := tui.NewWorkspace(l, c)
	theme := e.Theme
	if theme.Accent == nil {
		theme = tui.NewTheme()
	}
	ws.SetTheme(theme)
	return &providersScreen{home: h, env: e, get: g, now: n, list: l, card: c, workspace: ws, providers: map[string]ProviderInfo{}, nodesList: NodeList, dialNode: DiagDialLabel}
}
func (s *providersScreen) Title() string        { return "Providers" }
func (s *providersScreen) CapturingInput() bool { return s.dialog != nil || s.list.Filtering }
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
	return append(b, key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")), key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh all")), key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "countries")), key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")), key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "nodes")), key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "diagnose")))
}
func (s *providersScreen) KeyHints() string {
	if s.dialog != nil {
		if s.confirm != nil {
			return "Dialog: " + strings.TrimSpace(s.confirm.View(s.width, s.height))
		}
		return "Dialog: confirm or cancel"
	}
	return "↑/↓ select  Enter details  a add  r refresh  R refresh all  c countries  n nodes  g diagnose  d remove  / filter"
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
		if s.nodesDlg != nil {
			s.nodesDlg.SetTheme(x.Theme)
		}
		if s.diagDlg != nil {
			s.diagDlg.SetTheme(x.Theme)
		}
		return s, nil
	}
	if x, ok := m.(tui.BackgroundMsg); ok {
		m = x.Value
	}
	if _, ok := m.(nodesCloseMsg); ok {
		s.dialog = nil
		s.nodesDlg = nil
		s.diagDlg = nil
		s.mode = ""
		return s, nil
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
			if s.previewCancel != nil {
				s.previewCancel()
				s.previewCanceled = true
				if s.wizard != nil {
					s.wizard.BusyText = "Canceling..."
				}
				return s, nil
			}
			if s.addCancel != nil {
				s.addCancel()
				s.addCancelRequested = true
				if s.wizard != nil {
					s.wizard.SetError(errors.New("Cancel requested; waiting for provider operation to stop"))
				}
				return s, nil
			}
			s.clearPreview()
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
	if x, ok := m.(tui.PromptResultMsg); ok {
		switch s.mode {
		case "edit-prompt":
			return s.applyEditPrompt(x)
		case "rename-prompt":
			return s.applyRenamePrompt(x)
		case "countries-prompt":
			return s.applyCountriesPrompt(x)
		case "diag-prompt-node":
			return s.applyDiagNodePrompt(x)
		case "diag-prompt-samples":
			return s.applyDiagSamplesPrompt(x)
		}
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		if p := s.syncPreview(); p != nil {
			return s, tea.Batch(c, p)
		}
		return s, c
	}
	if len(s.refreshLines) > 0 && s.mode == "results" {
		return s.updateResults(m)
	}
	if k, ok := m.(tea.KeyPressMsg); ok {
		if !s.list.Filtering {
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
					return s, s.countriesCmd()
				}
			case "n":
				if row, ok := s.list.SelectedRow(); ok && s.nodeActionsEnabled(row.Data.(string)) {
					s.selected = row.Data.(string)
					return s, s.loadNodes("nodes-load")
				}
				return s, nil
			case "g":
				if row, ok := s.list.SelectedRow(); ok && s.nodeActionsEnabled(row.Data.(string)) {
					s.selected = row.Data.(string)
					return s, s.loadNodes("diag-load")
				}
				return s, nil
			case "esc":
				if s.mode == "diag-run" && s.diagCancel != nil {
					s.diagCancel()
					s.diagCanceled = true
					return s, nil
				}
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
	s.clearPreview()
	s.wizard = tui.NewWizard("Add provider", s.addWizardSteps())
	s.wizard.SetTheme(s.workspace.Theme)
	s.dialog = s.wizard
	s.mode = "add-wizard"
}
func (s *providersScreen) startEditPrompt(field string) {
	s.editField = field
	label, hint := "Via URL", "Credential hidden while editing"
	switch field {
	case "url":
		label = "Subscription URL"
	case "pre", "post":
		label = map[string]string{"pre": "Pre-hop", "post": "Post-hop"}[field]
		hint = "socks5://, http:// or https:// URL, or none; empty leaves it unchanged; clearing is the Clear action"
	}
	s.prompt = tui.NewPrompt(tui.SecretPrompt, "Edit provider — "+label, label, hint, "")
	s.prompt.SetTheme(s.workspace.Theme)
	s.dialog = s.prompt
	s.mode = "edit-prompt"
}
func (s *providersScreen) startRenamePrompt() {
	s.editField = "name"
	s.prompt = tui.NewPrompt(tui.TextPrompt, "Rename provider — new name", "Name", "Accounts and stored nodes follow the rename", s.selected)
	s.prompt.SetTheme(s.workspace.Theme)
	s.dialog = s.prompt
	s.mode = "rename-prompt"
}
func (s *providersScreen) startCountriesDialog(res ProviderCountriesResult) {
	opts := make([]tui.PromptOption, 0, len(res.Countries))
	for _, c := range res.Countries {
		opts = append(opts, tui.PromptOption{Label: fmt.Sprintf("%s — %d nodes", c.Code, ptrInt(&c.Nodes)), Value: c.Code})
	}
	p := tui.NewPrompt(tui.MultiChoicePrompt, "Provider "+s.selected+": countries", "", "Space toggles · Enter applies · Esc cancels", "")
	p.Options = opts
	for _, c := range s.providers[s.selected].Countries {
		p.Selected = append(p.Selected, c.Code)
	}
	p.SetTheme(s.workspace.Theme)
	s.prompt = p
	s.dialog = p
	s.mode = "countries-prompt"
}
func (s *providersScreen) startPrompt() {
	s.confirm = &tui.Confirm{Prompt: fmt.Sprintf("Remove %s? Accounts using this provider: %s", s.confirmName, strings.Join(s.confirmDeps, ", "))}
	s.confirm.SetTheme(s.workspace.Theme)
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
func (s *providersScreen) applyEditPrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
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
	if (field == "pre" || field == "post") && strings.TrimSpace(value) == "" {
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		return s, nil
	}
	s.busy = "Saving provider"
	s.prompt.SetSaving(true)
	return s, s.bg(s.busy, func() tea.Msg {
		var o ProviderUpdateOptions
		switch field {
		case "url":
			o.URL = &value
		case "pre":
			o.Pre = &value
		case "post":
			o.Post = &value
		default:
			o.Via = &value
		}
		return providerResultMsg{"edit", nil, ProviderUpdate(s.home, s.selected, o)}
	})
}
func (s *providersScreen) applyRenamePrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		return s, nil
	}
	newName := strings.TrimSpace(x.Value)
	if newName == "" || newName == s.selected {
		s.prompt.SetError(errors.New("Enter a different name"))
		return s, nil
	}
	old := s.selected
	s.busy = "Renaming provider"
	s.prompt.SetSaving(true)
	return s, s.bg(s.busy, func() tea.Msg {
		return providerResultMsg{"rename", [2]string{old, newName}, ProviderRename(s.home, old, newName)}
	})
}
func (s *providersScreen) applyCountriesPrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		return s, nil
	}
	if len(x.Selected) == 0 {
		s.message = "Countries cleared: all nodes qualify"
		s.card.Lines[4].Value = "All countries"
	} else {
		s.message = "Countries applied: " + strings.Join(x.Selected, ", ")
		s.card.Lines[4].Value = strings.Join(x.Selected, ", ")
	}
	// Country codes only: no credentials can appear in this message, and
	// scrubbing would mangle them (the picker value is a country list).
	var cmd tea.Cmd
	if s.env.Toast != nil {
		cmd = s.env.Toast(s.message)
	}
	s.dialog = nil
	s.prompt = nil
	s.mode = ""
	return s, cmd
}
func (s *providersScreen) countriesCmd() tea.Cmd {
	n := s.selected
	s.busy = "Loading countries"
	return s.bg(s.busy, func() tea.Msg { r, e := ProviderCountries(s.home, n); return providerResultMsg{"countries", r, e} })
}

// nodeActionsEnabled reports whether the selected provider has a stored
// snapshot, which both Nodes... and Diagnose... need.
func (s *providersScreen) nodeActionsEnabled(name string) bool {
	p, ok := s.providers[name]
	return ok && p.Nodes != nil
}

func (s *providersScreen) loadNodes(mode string) tea.Cmd {
	if s.selected == "" {
		return nil
	}
	name := s.selected
	s.mode = mode
	s.busy = "Loading nodes"
	return s.bg(s.busy, func() tea.Msg {
		v, e := s.nodesList(s.home, name, "")
		return providerResultMsg{kind: "nodes", value: v, err: e}
	})
}

func (s *providersScreen) startNodesDialog(name string, entries []NodeEntry) {
	d := newNodesDialog(name, entries)
	d.SetTheme(s.workspace.Theme)
	s.nodesDlg = d
	s.dialog = d
	s.mode = "nodes-dialog"
}

func (s *providersScreen) startDiagnose(entries []NodeEntry) {
	if len(entries) == 0 {
		s.mode = ""
		s.message = "No stored nodes for this provider"
		s.hint = ""
		s.toast(s.message)
		return
	}
	opts := make([]tui.PromptOption, 0, len(entries))
	for _, e := range entries {
		opts = append(opts, tui.PromptOption{Label: e.Label(), Value: e.Label()})
	}
	p := tui.NewPrompt(tui.ChoicePrompt, "Diagnose provider "+s.selected+": node", "Node", "The stored nodes of this provider", opts[0].Value)
	p.Options = opts
	p.SetTheme(s.workspace.Theme)
	s.prompt = p
	s.dialog = p
	s.mode = "diag-prompt-node"
}

func (s *providersScreen) applyDiagNodePrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		return s, nil
	}
	s.diagLabel = x.Value
	lo, hi := 1, maxDiagSamples
	p := tui.NewPrompt(tui.NumberPrompt, "Diagnose "+s.diagLabel+": samples", "Samples", "Requests per phase, 1..20", strconv.Itoa(defaultDiagSamples))
	p.Min, p.Max = &lo, &hi
	p.SetTheme(s.workspace.Theme)
	s.prompt = p
	s.dialog = p
	s.mode = "diag-prompt-samples"
	return s, nil
}

func (s *providersScreen) applyDiagSamplesPrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		return s, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(x.Value))
	if err != nil || n < 1 || n > maxDiagSamples {
		s.prompt.SetError(errors.New("the samples count must be a whole number between 1 and 20"))
		return s, nil
	}
	return s, s.runDiagnose(s.diagLabel, n)
}

func (s *providersScreen) runDiagnose(label string, samples int) tea.Cmd {
	s.dialog = nil
	s.prompt = nil
	ctx, cancel := context.WithCancel(context.Background())
	s.diagLabel = label
	s.diagCancel = cancel
	s.diagCanceled = false
	s.mode = "diag-run"
	s.busy = "Diagnosing " + label
	s.updateCard()
	dial := s.dialNode
	home := s.home
	return s.bg(s.busy, func() tea.Msg {
		v, e := dial(ctx, home, label, samples)
		return providerResultMsg{kind: "diag", value: v, err: e}
	})
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
				s.list.SelectData(s.pendingSelect)
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
		s.clearPreview()
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
	case "rename":
		oldNew, _ := r.value.([2]string)
		if r.err != nil {
			s.setError(r.err)
			s.toast(s.message)
			if s.prompt != nil {
				s.prompt.SetSaving(false)
				s.prompt.SetError(errors.New(s.message + hintText(s.hint)))
			}
			return s, nil
		}
		s.message = "Provider renamed"
		s.toast(s.message)
		s.dialog = nil
		s.prompt = nil
		s.mode = ""
		s.selected = oldNew[1]
		s.pendingSelect = oldNew[1]
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
			s.message = ""
			s.hint = ""
			s.startCountriesDialog(s.countries)
		}
	case "preview":
		if s.previewCancel != nil {
			s.previewCancel()
			s.previewCancel = nil
		}
		if s.wizard != nil {
			s.wizard.Busy = false
			s.wizard.BusyText = ""
		}
		switch {
		case s.previewCanceled || (r.err != nil && errors.Is(r.err, context.Canceled)):
			s.previewCanceled = false
			s.previewState = "canceled"
			s.message = ""
			s.hint = ""
		case r.err != nil:
			s.previewState = "failed"
			s.setError(r.err)
			s.toast(s.message)
			if s.wizard != nil {
				s.wizard.SetError(errors.New(s.message + hintText(s.hint)))
			}
		default:
			s.previewState = "done"
			s.preview = r.value.(previewData)
			s.message = ""
			s.hint = ""
		}
	case "nodes":
		if r.err != nil {
			s.mode = ""
			s.setError(r.err)
			s.toast(s.message)
			return s, nil
		}
		entries, _ := r.value.([]NodeEntry)
		switch s.mode {
		case "diag-load":
			s.mode = ""
			s.startDiagnose(entries)
		case "nodes-load":
			s.mode = ""
			s.startNodesDialog(s.selected, entries)
		}
	case "diag":
		if s.diagCancel != nil {
			s.diagCancel()
			s.diagCancel = nil
		}
		diag, _ := r.value.(core.NodeDiag)
		switch {
		case s.diagCanceled || (r.err != nil && errors.Is(r.err, context.Canceled)):
			s.diagCanceled = false
			s.mode = ""
			s.message = "Diagnosis canceled"
			s.hint = ""
			s.toast(s.message)
		case r.err != nil:
			s.mode = ""
			s.setError(r.err)
			s.toast(s.message)
		default:
			s.updateCard()
			d := newDiagResultDialog(diag)
			d.SetTheme(s.workspace.Theme)
			s.diagDlg = d
			s.dialog = d
			s.mode = "diag-result"
		}
	}
	return s, nil
}
func (s *providersScreen) toast(text string) {
	if text != "" {
		s.pendingToast = s.scrub(text)
	}
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
	if s.list.Filter == "" {
		// no filter active: behave exactly like before (clamp, then reselect)
		if len(rows) > 0 && s.list.Selected >= len(rows) {
			s.list.Selected = len(rows) - 1
		}
		if s.selected != "" {
			s.list.SelectData(s.selected)
		}
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
	if s.busy != "" {
		s.card.Info = append(s.card.Info, s.busy+"…")
	}
	s.card.Lines[0].Value = p.Name
	s.card.Lines[1].Value = map[bool]string{true: "•••••• (set)", false: "Not set"}[p.Host != ""]
	s.card.Lines[2].Value = map[bool]string{true: "Configured", false: "Not set"}[p.ViaSet]
	s.card.Lines[3].Value = nodes
	s.card.Lines[4].Value = strings.Join(countries, ", ")
	s.card.Lines[5].Value = updated
	for _, h := range []struct{ id, value string }{{"pre", p.Pre}, {"post", p.Post}} {
		for i := range s.card.Lines {
			switch s.card.Lines[i].ID {
			case h.id:
				s.card.Lines[i].Value = providerHopLabel(h.value)
			case "clear-" + h.id:
				s.card.Lines[i].Disabled = h.value == ""
			}
		}
	}
	noNodes := p.Nodes == nil
	for i := range s.card.Lines {
		switch s.card.Lines[i].ID {
		case "nodes", "diagnose":
			if s.card.Lines[i].Kind == tui.CardAction {
				s.card.Lines[i].Disabled = noNodes
			}
		}
	}
}

// providerHopLabel is the state of a provider hop; the URL is never shown.
func providerHopLabel(value string) string {
	switch {
	case value == "":
		return "inherited"
	case strings.EqualFold(value, "none"):
		return "none"
	}
	return "set"
}

func (s *providersScreen) clearHop(side string) tea.Cmd {
	empty := ""
	var o ProviderUpdateOptions
	if side == "pre" {
		o.Pre = &empty
	} else {
		o.Post = &empty
	}
	s.busy = "Saving provider"
	return s.bg(s.busy, func() tea.Msg { return providerResultMsg{"edit", nil, ProviderUpdate(s.home, s.selected, o)} })
}
func (s *providersScreen) activateCard() (tui.Screen, tea.Cmd) {
	switch s.card.Lines[s.card.Selected].ID {
	case "edit-subscription":
		s.startEditPrompt("url")
	case "edit-via":
		s.startEditPrompt("via")
	case "subscription":
		s.startEditPrompt("url")
	case "via":
		s.startEditPrompt("via")
	case "pre", "post":
		s.startEditPrompt(s.card.Lines[s.card.Selected].ID)
	case "clear-pre":
		return s, s.clearHop("pre")
	case "clear-post":
		return s, s.clearHop("post")
	case "refresh":
		return s, s.refresh(s.selected, false)
	case "refresh-all":
		return s, s.refresh("", true)
	case "countries":
		return s, s.countriesCmd()
	case "nodes":
		return s, s.loadNodes("nodes-load")
	case "diagnose":
		return s, s.loadNodes("diag-load")
	case "edit-name":
		s.startRenamePrompt()
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
	if s.mode == "results" {
		return s.viewResults(w, h)
	}
	return s.workspace.View(w, h)
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
	for len(lines) < h {
		lines = append(lines, "")
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
	return providerURLScheme.ReplaceAllString(text, "[redacted]")
}

// providerURLScheme matches a URL that still carries a replaced secret, so a
// shortened or reordered copy of the URL in an error never shows its scheme.
var providerURLScheme = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S*\[redacted\]\S*`)
