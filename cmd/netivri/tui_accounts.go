package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type accountsStatusSource func(string) (angel.Status, error)
type accountsLoaded struct {
	Rows       []accountInfo
	Err        error
	Generation uint64
}
type accountsPollResult struct {
	Status     angel.Status
	Err        error
	At         time.Time
	Generation uint64
}
type accountsChoicesLoaded struct {
	Providers []string
	Nodes     map[string]string
	Countries ProviderCountriesAllResult
	Err       error
}
type accountsDetailLoaded struct {
	Result     AccountShowResult
	Err        error
	Generation uint64
}
type accountsMutationDone struct {
	Rows   []accountInfo
	Err    error
	Notice string
	Login  string
}
type accountsRateTick struct{}
type accountsDialogResult struct {
	Values   map[string]string
	Canceled bool
}
type accountsDialog struct {
	prompt      *tui.Prompt
	wizard      *tui.Wizard
	onResult    func(map[string]string) tea.Cmd
	validate    func(map[string]string) error
	removeLogin string
}

func (d *accountsDialog) SetTheme(t tui.Theme) {
	if d.prompt != nil {
		d.prompt.SetTheme(t)
	}
	if d.wizard != nil {
		d.wizard.SetTheme(t)
	}
}
func (d *accountsDialog) CapturingInput() bool { return true }
func (d *accountsDialog) View(w, h int) string {
	if d.wizard != nil {
		return d.wizard.View(w, h)
	}
	return d.prompt.View(w, h)
}
func (d *accountsDialog) Update(msg tea.Msg) (tui.Modal, tea.Cmd) {
	if d.wizard != nil {
		if d.wizard.Saving {
			return d, nil
		}
		m, c := d.wizard.Update(msg)
		d.wizard = m.(*tui.Wizard)
		if c != nil {
			return d, func() tea.Msg {
				v := c()
				if r, ok := v.(tui.WizardResultMsg); ok {
					return accountsDialogResult{Values: r.Values, Canceled: r.Canceled}
				}
				return v
			}
		}
		return d, c
	}
	if d.prompt != nil && d.prompt.Saving {
		return d, nil
	}
	m, c := d.prompt.Update(msg)
	d.prompt = m.(*tui.Prompt)
	if c != nil {
		return d, func() tea.Msg {
			v := c()
			if r, ok := v.(tui.PromptResultMsg); ok {
				return accountsDialogResult{Values: map[string]string{"value": r.Value}, Canceled: r.Canceled}
			}
			return v
		}
	}
	return d, c
}

type accountsScreen struct {
	env                                              tui.Env
	status                                           accountsStatusSource
	show                                             func(string, string) (AccountShowResult, error)
	rows                                             []accountInfo
	live                                             map[string]angel.AccountActivity
	rates                                            map[string][2]string
	previous                                         map[string]angel.AccountActivity
	previousAt                                       time.Time
	providers                                        []string
	providerNodes                                    map[string]string
	providerCountries                                map[string][]string
	loading, polling                                 bool
	width, height                                    int
	message, statusMessage                           string
	loadGeneration, pollGeneration, detailGeneration uint64
	pollWait                                         func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	workspace                                        *tui.Workspace
	theme                                            tui.Theme
	cardResult                                       *AccountShowResult
	detailLogin                                      string
	dialog                                           *accountsDialog
	pendingLogin                                     string
}

func newAccountsScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	s := &accountsScreen{env: env, live: map[string]angel.AccountActivity{}, rates: map[string][2]string{}, previous: map[string]angel.AccountActivity{}, providerCountries: map[string][]string{}, providerNodes: map[string]string{}}
	s.status = func(h string) (angel.Status, error) {
		ctx, c := context.WithTimeout(context.Background(), angel.ProbeTimeout)
		defer c()
		_, st, e := angelProbeLive(ctx, h)
		return st, e
	}
	s.show = AccountShow
	s.theme = env.Theme
	if s.theme.Accent == nil {
		s.theme = tui.NewTheme()
	}
	l := tui.NewList([]tui.ListColumn{{Title: "LOGIN", Min: 8, Flex: 2}, {Title: "PROVIDER", Min: 8, Flex: 2}, {Title: "EXIT", Min: 5, Flex: 1}}, nil)
	l.Title = "Accounts"
	l.EmptyText = "An account needs a Provider first. Switch to Providers to add one."
	l.AddLabel = "+ Add account [a]"
	s.workspace = tui.NewWorkspace(l, tui.NewCard("Account", nil))
	s.workspace.SetTheme(s.theme)
	s.configureColumns(0)
	return s
}
func (s *accountsScreen) Title() string        { return "Accounts" }
func (s *accountsScreen) CapturingInput() bool { return s.dialog != nil }
func (s *accountsScreen) Breadcrumb() string {
	if s.workspace.Drilled && s.detailLogin != "" {
		return "Accounts › " + s.detailLogin
	}
	return ""
}
func (s *accountsScreen) FocusPane(reverse bool) bool {
	if s.workspace.Width < 100 {
		return false
	}
	old := s.workspace.Focus()
	if reverse {
		s.workspace.CycleFocus(-1)
	} else {
		s.workspace.CycleFocus(1)
	}
	return old != s.workspace.Focus()
}
func (s *accountsScreen) SetPaneFocus(p int) {
	if p < 0 {
		p = tui.PaneCard
	}
	s.workspace.SetFocus(p)
}

var accountsActionBindings = []bubbleskey.Binding{
	bubbleskey.NewBinding(bubbleskey.WithKeys("a"), bubbleskey.WithHelp("a", "add")),
	bubbleskey.NewBinding(bubbleskey.WithKeys("e"), bubbleskey.WithHelp("e", "edit provider")),
	bubbleskey.NewBinding(bubbleskey.WithKeys("c"), bubbleskey.WithHelp("c", "clone")),
	bubbleskey.NewBinding(bubbleskey.WithKeys("d"), bubbleskey.WithHelp("d", "remove")),
	bubbleskey.NewBinding(bubbleskey.WithKeys("r"), bubbleskey.WithHelp("r", "reload")),
}

func (s *accountsScreen) Bindings() tui.ActionHints {
	var bindings []bubbleskey.Binding
	if s.workspace != nil && s.workspace.Focus() == tui.PaneCard && s.workspace.Card != nil {
		bindings = append(bindings, s.workspace.Card.Bindings()...)
	} else if s.workspace != nil && s.workspace.List != nil {
		bindings = append(bindings, s.workspace.List.Bindings()...)
	}
	bindings = append(bindings, accountsActionBindings...)
	return tui.ActionHints(bindings)
}
func (s *accountsScreen) KeyHints() string {
	return "↑↓ select · Enter open · a add · e edit · c clone · d remove · r reload · Tab pane"
}
func (s *accountsScreen) Init() tea.Cmd { return tea.Batch(s.reload(), s.loadChoices(), s.rateTick()) }
func (s *accountsScreen) now() time.Time {
	if s.env.Now != nil {
		return s.env.Now()
	}
	return time.Now()
}
func (s *accountsScreen) bg(label string, f func() tea.Msg) tea.Cmd {
	if s.env.Background == nil {
		return nil
	}
	return s.env.Background(label, f)
}
func (s *accountsScreen) reload() tea.Cmd {
	s.loading = true
	s.loadGeneration++
	g := s.loadGeneration
	return s.bg("accounts", func() tea.Msg { r, e := AccountList(s.env.Home); return accountsLoaded{r, e, g} })
}
func (s *accountsScreen) loadChoices() tea.Cmd {
	return s.bg("account choices", func() tea.Msg {
		pl, e := ProviderList(s.env.Home)
		if e != nil {
			return accountsChoicesLoaded{Err: e}
		}
		co, e := ProviderCountriesAll(s.env.Home)
		if e != nil {
			return accountsChoicesLoaded{Err: e}
		}
		ps := []string{}
		nodes := map[string]string{}
		for _, p := range pl.Providers {
			ps = append(ps, p.Name)
			nodes[p.Name] = "unknown"
			if p.Nodes != nil {
				nodes[p.Name] = strconv.Itoa(*p.Nodes)
			}
		}
		return accountsChoicesLoaded{Providers: ps, Nodes: nodes, Countries: co}
	})
}
func (s *accountsScreen) rateTick() tea.Cmd {
	wait := s.pollWait
	if wait == nil {
		wait = tea.Tick
	}
	timer := wait(time.Second, func(time.Time) tea.Msg { return accountsRateTick{} })
	return s.bg("account activity timer", func() tea.Msg { return timer() })
}
func (s *accountsScreen) Update(msg tea.Msg) (tui.Screen, tea.Cmd) {
	if d, ok := msg.(*accountsDialog); ok {
		s.dialog = d
		return s, nil
	}
	if s.dialog != nil {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg:
			d, cmd := s.dialog.Update(msg)
			s.dialog = d.(*accountsDialog)
			return s, cmd
		}
	}
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = max(0, m.Width)
		s.height = max(0, m.Height)
		s.workspace.SetSize(s.width, s.height)
		s.configureColumns(s.width)
		if s.workspace.Card != nil {
			s.workspace.Card.Focused = s.workspace.Focus() == tui.PaneCard
		}
		if s.workspace.List != nil {
			s.workspace.List.Focused = s.workspace.Focus() == tui.PaneList
		}
		return s, nil
	case tui.ThemeMsg:
		s.theme = m.Theme
		s.workspace.SetTheme(m.Theme)
		if s.workspace.Card != nil {
			s.workspace.Card.SetTheme(m.Theme)
		}
		if s.dialog != nil {
			s.dialog.SetTheme(m.Theme)
		}
		return s, nil
	case tui.BackgroundMsg:
		return s, s.updateBackground(m.Value)
	case tui.RegionFocusMsg:
		if m.Tabs {
			s.workspace.SetFocus(tui.PaneList)
		}
		return s, nil
	case accountsDialogResult:
		if s.dialog == nil {
			return s, nil
		}
		if m.Canceled {
			s.dialog = nil
			return s, nil
		}
		d := s.dialog
		if d.onResult == nil {
			s.dialog = nil
			return s, nil
		}
		if d.validate != nil {
			if err := d.validate(m.Values); err != nil {
				d.validationError(err)
				return s, nil
			}
		}
		if d.prompt != nil {
			d.prompt.SetSaving(true)
		}
		if d.wizard != nil {
			d.wizard.SetSaving(true)
		}
		return s, d.onResult(m.Values)
	case accountsRateTick:
		if s.polling {
			return s, nil
		}
		s.polling = true
		s.pollGeneration++
		g := s.pollGeneration
		return s, s.bg("account activity", func() tea.Msg { st, e := s.status(s.env.Home); return accountsPollResult{st, e, s.now(), g} })
	case accountsLoaded:
		if m.Generation != s.loadGeneration {
			return s, nil
		}
		s.loading = false
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			return s, s.notice(s.message)
		}
		selected := s.selectedLogin()
		s.rows = m.Rows
		s.syncRows(selected)
		return s, s.loadDetail(s.selectedLogin())
	case accountsPollResult:
		if m.Generation != s.pollGeneration {
			return s, nil
		}
		s.polling = false
		s.applyStatus(m.Status, m.Err, m.At)
		s.updateLiveRows()
		return s, s.rateTick()
	case accountsDetailLoaded:
		if m.Generation != s.detailGeneration {
			return s, nil
		}
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			return s, s.notice(s.message)
		}
		if m.Result.Login != s.selectedLogin() {
			return s, nil
		}
		s.cardResult = &m.Result
		s.updateCard()
		return s, nil
	case accountsChoicesLoaded:
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			return s, s.notice(s.message)
		}
		s.providers = m.Providers
		s.providerNodes = m.Nodes
		s.providerCountries = map[string][]string{}
		for _, p := range m.Countries.Providers {
			for _, c := range p.Countries {
				s.providerCountries[p.Name] = append(s.providerCountries[p.Name], c.Code)
			}
		}
		return s, nil
	case accountsMutationDone:
		s.loading = false
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			if s.dialog != nil {
				s.dialog.promptError(m.Err)
			}
			return s, s.notice(s.safeNotice(s.message))
		}
		s.rows = m.Rows
		s.message = m.Notice
		s.dialog = nil
		s.syncRows(m.Login)
		s.refreshSelectedCard(true)
		return s, tea.Batch(s.loadDetail(s.selectedLogin()), s.notice(s.message))
	case tui.ConfirmResultMsg:
		login := s.pendingLogin
		s.pendingLogin = ""
		if !m.Confirmed || login == "" {
			return s, nil
		}
		if _, exists := s.accountByLogin(login); !exists || login != s.selectedLogin() {
			return s, nil
		}
		return s, s.mutateRemove(login)
	case tea.MouseClickMsg, tea.MouseWheelMsg:
		before := s.selectedLogin()
		activated := s.workspace.Update(msg)
		if activated && s.workspace.Focus() == tui.PaneCard {
			return s, s.activateCard()
		}
		if s.selectedLogin() == before {
			if activated && s.workspace.List.IsAdd() {
				if len(s.providers) == 0 {
					return s, s.switchProviders()
				}
				return s, s.beginAdd()
			}
			return s, nil
		}
		return s, s.selectionChanged()
	case tea.KeyPressMsg:
		return s.updateKey(m)
	}
	return s, nil
}
func keyMatchesAccounts(msg tea.KeyPressMsg, binding bubbleskey.Binding) bool {
	return bubbleskey.Matches(msg, binding)
}

func (s *accountsScreen) updateKey(m tea.KeyPressMsg) (tui.Screen, tea.Cmd) {
	if s.dialog != nil {
		d, c := s.dialog.Update(m)
		s.dialog = d.(*accountsDialog)
		return s, c
	}
	k := m.String()
	if k == "tab" && s.width >= 100 {
		s.workspace.CycleFocus(1)
		return s, nil
	}
	if k == "shift+tab" && s.width >= 100 {
		s.workspace.CycleFocus(-1)
		return s, nil
	}
	if k == "esc" {
		if s.workspace.Drilled || s.workspace.Focus() == tui.PaneCard {
			s.workspace.Drilled = false
			s.workspace.SetFocus(tui.PaneList)
			return s, nil
		}
	}
	if keyMatchesAccounts(m, accountsActionBindings[4]) && s.workspace.Focus() == tui.PaneList {
		return s, tea.Batch(s.reload(), s.loadChoices())
	}
	if keyMatchesAccounts(m, accountsActionBindings[0]) || keyMatchesAccounts(m, accountsActionBindings[1]) || keyMatchesAccounts(m, accountsActionBindings[2]) || keyMatchesAccounts(m, accountsActionBindings[3]) {
		if keyMatchesAccounts(m, accountsActionBindings[0]) {
			if len(s.providers) == 0 {
				return s, s.switchProviders()
			}
			return s, s.beginAdd()
		}
		a, ok := s.selectedAccount()
		if !ok {
			return s, nil
		}
		switch {
		case keyMatchesAccounts(m, accountsActionBindings[1]):
			return s, s.openEdit(a, "provider")
		case keyMatchesAccounts(m, accountsActionBindings[2]):
			return s, s.beginClone(a)
		case keyMatchesAccounts(m, accountsActionBindings[3]):
			if s.env.OpenModal != nil {
				s.pendingLogin = a.Login
				return s, s.env.OpenModal(&tui.Confirm{Prompt: "Remove account " + a.Login + "?"})
			}
		}
	}
	if k == "enter" && s.workspace.List.IsAdd() {
		if len(s.providers) == 0 {
			return s, s.switchProviders()
		}
		return s, s.beginAdd()
	}
	if k == "enter" && s.width < 100 && !s.workspace.Drilled {
		s.workspace.Drilled = true
		s.workspace.SetFocus(tui.PaneCard)
		return s, s.loadDetail(s.selectedLogin())
	}
	if k == "enter" && s.width >= 100 && s.workspace.Focus() == tui.PaneList {
		s.workspace.SetFocus(tui.PaneCard)
		return s, s.loadDetail(s.selectedLogin())
	}
	if s.workspace.Focus() == tui.PaneCard && (s.width >= 100 || s.workspace.Drilled) {
		if s.workspace.Card.Update(m) {
			return s, s.activateCard()
		}
		return s, nil
	}
	before := -1
	if s.workspace.List != nil {
		before = s.workspace.List.Selected
	}
	if s.workspace.List.Update(m) {
		if s.workspace.List.IsAdd() {
			if len(s.providers) == 0 {
				return s, s.switchProviders()
			}
			return s, s.beginAdd()
		}
		return s, s.selectionChanged()
	}
	if before >= 0 && s.workspace.List.Selected != before && !s.workspace.List.IsAdd() {
		s.refreshSelectedCard(true)
		return s, s.loadDetail(s.selectedLogin())
	}
	return s, nil
}
func (s *accountsScreen) selectionChanged() tea.Cmd {
	s.refreshSelectedCard(true)
	return s.loadDetail(s.selectedLogin())
}
func (d *accountsDialog) validationError(e error) {
	if d.prompt != nil {
		d.prompt.SetSaving(false)
		d.prompt.SetError(e)
	}
	if d.wizard != nil {
		d.wizard.SetSaving(false)
		d.wizard.SetError(e)
	}
}
func (d *accountsDialog) promptError(e error) {
	if d.prompt != nil {
		d.prompt.SetSaving(false)
		d.prompt.SetError(errors.New(accountsSafePromptError(e, d.prompt.Value)))
	}
	if d.wizard != nil {
		d.wizard.SetSaving(false)
		d.wizard.SetError(errors.New(accountsErrorText(e)))
	}
}
func (s *accountsScreen) updateBackground(v tea.Msg) tea.Cmd {
	switch m := v.(type) {
	case accountsRateTick:
		_, c := s.Update(m)
		return c
	case accountsLoaded:
		if m.Generation == s.loadGeneration {
			_, c := s.Update(m)
			return c
		}
	case accountsPollResult:
		if m.Generation == s.pollGeneration {
			_, c := s.Update(m)
			return c
		}
	case accountsChoicesLoaded, accountsDetailLoaded, accountsMutationDone:
		_, c := s.Update(m)
		return c
	}
	return nil
}
func (s *accountsScreen) selectedLogin() string {
	if r, ok := s.workspace.List.SelectedRow(); ok {
		if a, ok := r.Data.(accountInfo); ok {
			return a.Login
		}
	}
	return ""
}
func (s *accountsScreen) accountByLogin(login string) (accountInfo, bool) {
	for _, a := range s.rows {
		if a.Login == login {
			return a, true
		}
	}
	return accountInfo{}, false
}
func (s *accountsScreen) selectedAccount() (accountInfo, bool) {
	return s.accountByLogin(s.selectedLogin())
}
func (s *accountsScreen) updateLiveRows() {
	for i := range s.workspace.List.Rows {
		row := &s.workspace.List.Rows[i]
		a, ok := row.Data.(accountInfo)
		if !ok || len(row.Cells) < 6 {
			continue
		}
		row.Cells = append([]string(nil), row.Cells...)
		row.Cells[3], row.Cells[4], row.Cells[5] = "-", "-", "-"
		if live, ok := s.live[a.Login]; ok {
			row.Cells[3] = strconv.Itoa(live.Connections)
			if rates, ok := s.rates[a.Login]; ok {
				row.Cells[4], row.Cells[5] = rates[0], rates[1]
			}
		}
	}
	s.updateLiveCardInfo()
}
func (s *accountsScreen) updateLiveCardInfo() {
	if s.workspace.Card == nil || s.workspace.Card.Title == "Account" {
		return
	}
	login := strings.TrimPrefix(s.workspace.Card.Title, "Account ")
	activity, up, down := "-", "-", "-"
	if live, ok := s.live[login]; ok {
		activity = strconv.Itoa(live.Connections)
		if rates, ok := s.rates[login]; ok {
			up, down = rates[0], rates[1]
		}
	}
	line := "Live · Conn " + activity + " · Up/s " + up + " · Down/s " + down
	info := s.workspace.Card.Info
	if len(info) > 0 && strings.HasPrefix(info[0], "Live · Conn ") {
		info[0] = line
	} else {
		s.workspace.Card.Info = append([]string{line}, info...)
	}
}
func (s *accountsScreen) configureColumns(width int) {
	s.workspace.List.Columns = []tui.ListColumn{{Title: "LOGIN", Min: 4, Flex: 2}, {Title: "PROVIDER", Min: 4, Flex: 2}, {Title: "EXIT", Min: 2, Flex: 1}, {Title: "CONN", Min: 2, Flex: 1}, {Title: "UP/s", Min: 3, Flex: 1}, {Title: "DOWN", Min: 3, Flex: 1}}
}

func (s *accountsScreen) syncRows(preferred string) {
	if s.workspace.Card != nil {
		s.workspace.Card.Focused = s.workspace.Focus() == tui.PaneCard
	}
	selected := s.selectedLogin()
	rs := []tui.Row{}
	for _, a := range s.rows {
		provider := displayAny(a.Provider)
		exit := displayAny(a.Exit)
		conn, up, down := "-", "-", "-"
		if x, ok := s.live[a.Login]; ok {
			conn = strconv.Itoa(x.Connections)
			r := s.rates[a.Login]
			up, down = r[0], r[1]
		}
		rs = append(rs, tui.Row{Data: a, Cells: []string{a.Login, provider, exit, conn, up, down}})
	}
	s.workspace.List.SetRows(rs)
	if preferred != "" {
		for i, r := range rs {
			if a, ok := r.Data.(accountInfo); ok && a.Login == preferred {
				s.workspace.List.Selected = i
				break
			}
		}
	}
	if s.selectedLogin() != selected {
		s.refreshSelectedCard(true)
	}
}
func (s *accountsScreen) updateCard() {
	selected, scroll, focused := 0, 0, false
	if s.workspace.Card != nil {
		selected, scroll, focused = s.workspace.Card.Selected, s.workspace.Card.Scroll, s.workspace.Card.Focused
	}
	s.refreshSelectedCard(true)
	if s.workspace.Card != nil {
		s.workspace.Card.Selected = min(selected, max(0, len(s.workspace.Card.Lines)-1))
		s.workspace.Card.Scroll = scroll
		s.workspace.Card.Focused = focused
	}
}
func (s *accountsScreen) refreshSelectedCard(force bool) {
	a, ok := s.selectedAccount()
	if !ok {
		if force || s.workspace.Card == nil || s.workspace.Card.Title != "Account" {
			s.workspace.Card = tui.NewCard("Account", nil)
			s.workspace.Card.SetTheme(s.theme)
			s.workspace.Card.Info = []string{"Select an account to see details."}
		}
		return
	}
	if !force && s.workspace.Card != nil && s.workspace.Card.Title == "Account "+a.Login {
		return
	}
	pw := "waiting for the first login"
	if a.PasswordSet {
		pw = "set"
	}
	pre, post := accountsHopLabel(a.PreSOCKS5.Source, a.PreSOCKS5.Value), accountsHopLabel(a.PostSOCKS5.Source, a.PostSOCKS5.Value)
	lines := []tui.CardLine{{ID: "provider", Label: "Provider", Value: displayAny(a.Provider), Kind: tui.CardField}, {ID: "countries", Label: "Countries", Value: displayAny(a.ExitCountry), Kind: tui.CardField}, {ID: "exit", Label: "Exit", Value: displayAny(a.Exit), Kind: tui.CardField}, {ID: "clients", Label: "Allowed clients", Value: displayAny(a.AllowedClients), Kind: tui.CardField}, {ID: "pre", Label: "Pre-hop", Value: pre, Kind: tui.CardField, Secret: true}, {ID: "clear-pre", Label: "Clear pre-hop", Kind: tui.CardAction, Action: true, Hotkey: "P", Disabled: pre == "none"}, {ID: "post", Label: "Post-hop", Value: post, Kind: tui.CardField, Secret: true}, {ID: "clear-post", Label: "Clear post-hop", Kind: tui.CardAction, Action: true, Hotkey: "O", Disabled: post == "none"}, {ID: "password", Label: "Password", Value: pw, Kind: tui.CardReadOnly}, {ID: "clone", Label: "Clone account", Kind: tui.CardAction, Action: true, Hotkey: "c"}, {ID: "remove", Label: "Remove account", Kind: tui.CardAction, Action: true, Hotkey: "d"}}
	if s.workspace.Card != nil {
		s.workspace.Card.Focused = true
	}
	s.workspace.Card = tui.NewCard("Account "+a.Login, lines)
	s.workspace.Card.SetTheme(s.theme)
	s.workspace.Card.Focused = s.workspace.Focus() == tui.PaneCard
	if a, ok := s.selectedAccount(); ok {
		activity := "-"
		up, down := "-", "-"
		if live, ok := s.live[a.Login]; ok {
			activity = strconv.Itoa(live.Connections)
			if rates, ok := s.rates[a.Login]; ok {
				up, down = rates[0], rates[1]
			}
		}
		s.workspace.Card.Info = []string{"Live · Conn " + activity + " · Up/s " + up + " · Down/s " + down}
	}
	if s.cardResult != nil && s.cardResult.Login == a.Login {
		s.workspace.Card.Info = append(s.workspace.Card.Info, "Route tiers and fallback order:", accountsTiersText(s.cardResult.Tiers), fmt.Sprintf("Live %t · fallback connections %s", s.cardResult.Live, accountsFallbackText(s.cardResult.FallbackConnections)))
	}
	s.updateLiveCardInfo()
}
func displayAny(s string) string {
	if s == "" {
		return "any"
	}
	return s
}
func (s *accountsScreen) loadDetail(login string) tea.Cmd {
	if login == "" {
		return nil
	}
	s.detailLogin = login
	s.detailGeneration++
	g := s.detailGeneration
	return s.bg("account detail", func() tea.Msg {
		r, e := s.show(s.env.Home, login)
		return accountsDetailLoaded{Result: r, Err: e, Generation: g}
	})
}
func (s *accountsScreen) activateCard() tea.Cmd {
	a, ok := s.selectedAccount()
	if !ok {
		return nil
	}
	l := s.workspace.Card.Lines[s.workspace.Card.Selected]
	switch l.ID {
	case "provider", "countries", "exit", "clients", "pre", "post":
		return s.openEdit(a, l.ID)
	case "clear-pre":
		return s.mutateEdit(a, "pre", map[string]string{"value": "", "clear": "true"})
	case "clear-post":
		return s.mutateEdit(a, "post", map[string]string{"value": "", "clear": "true"})
	case "clone":
		return s.beginClone(a)
	case "remove":
		if s.env.OpenModal != nil {
			s.pendingLogin = a.Login
			return s.env.OpenModal(&tui.Confirm{Prompt: "Remove account " + a.Login + "?"})
		}
	}
	return nil
}
func (s *accountsScreen) switchProviders() tea.Cmd {
	if s.env.SwitchTab != nil {
		return s.env.SwitchTab(0)
	}
	s.message = "Add a Provider first, then return to Accounts."
	return nil
}
func (s *accountsScreen) View(w, h int) string {
	w = max(0, w)
	h = max(0, h)
	if s.dialog != nil {
		s.dialog.SetTheme(s.theme)
		return s.dialog.View(w, h)
	}
	s.width, s.height = w, h
	s.workspace.SetSize(w, h)
	return s.workspace.View(w, h)
}
func (s *accountsScreen) applyStatus(st angel.Status, e error, at time.Time) {
	s.live = map[string]angel.AccountActivity{}
	s.rates = map[string][2]string{}
	if e != nil {
		s.previous = map[string]angel.AccountActivity{}
		s.previousAt = at
		s.statusMessage = "Live status unavailable"
		return
	}
	s.statusMessage = ""
	elapsed := at.Sub(s.previousAt).Seconds()
	for _, a := range st.Activity {
		s.live[a.Login] = a
		up, down := 0.0, 0.0
		if p, ok := s.previous[a.Login]; ok && elapsed > 0 && a.UploadBytes >= p.UploadBytes && a.DownloadBytes >= p.DownloadBytes {
			up = float64(a.UploadBytes-p.UploadBytes) / elapsed
			down = float64(a.DownloadBytes-p.DownloadBytes) / elapsed
		}
		s.rates[a.Login] = [2]string{humanRate(up), humanRate(down)}
	}
	s.previous = accountsActivity(st.Activity)
	s.previousAt = at
}
func accountsActivity(as []angel.AccountActivity) map[string]angel.AccountActivity {
	m := map[string]angel.AccountActivity{}
	for _, a := range as {
		m[a.Login] = a
	}
	return m
}
func accountsHopLabel(source, value string) string {
	if value == "" || value == "none" {
		if source == "" {
			return "none"
		}
		return source + " none"
	}
	if source == "" {
		return "set"
	}
	return source + " set"
}
func accountsFallbackText(v *int) string {
	if v == nil {
		return "unknown"
	}
	return strconv.Itoa(*v)
}
func accountsTiersText(ts []accountShowTier) string {
	if len(ts) == 0 {
		return "No route tiers"
	}
	out := []string{}
	for _, t := range ts {
		kind := "primary"
		if t.Fallback {
			kind = "fallback"
		}
		if t.Skipped {
			kind = "skipped"
		}
		nodes := []string{}
		for _, n := range t.Nodes {
			x := n.Name
			if x == "" {
				x = n.Label
			}
			nodes = append(nodes, x)
		}
		line := fmt.Sprintf("%d %s country=%s providers=%s nodes=%s", t.Tier, kind, t.Country, strings.Join(t.Providers, ","), strings.Join(nodes, ","))
		if t.Reason != "" {
			line += " reason=" + t.Reason
		}
		out = append(out, line)
	}
	return strings.Join(out, "; ")
}
func (s *accountsScreen) countryFieldHint(ps []string) string {
	if len(ps) == 0 || len(ps) == 1 && ps[0] == "any" {
		ps = s.providers
	}
	codes := []string{}
	seen := map[string]bool{}
	for _, p := range ps {
		for _, c := range s.providerCountries[p] {
			if !seen[c] {
				seen[c] = true
				codes = append(codes, c)
			}
		}
	}
	if len(codes) == 0 {
		return "Any or country codes; known countries unavailable"
	}
	return "Any or country codes: " + strings.Join(codes, ", ")
}
func (s *accountsScreen) beginAdd() tea.Cmd {
	if len(s.providers) == 0 {
		return s.switchProviders()
	}
	steps := []tui.WizardStep{{Key: "Login", Prompt: prompt(tui.TextPrompt, "Add account · Login", "Login", "Client login name.", "")}, {Key: "Provider", Prompt: choicePrompt("Provider", "Choose a provider; all configured providers are available.", append([]tui.PromptOption{{Label: "Any provider", Value: "any", Description: "Use any configured provider."}}, s.providerOptions()...), "any")}, {Key: "Country", Prompt: prompt(tui.TextPrompt, "Add account · Country", "Countries", s.countryFieldHint(nil), "any")}, {Key: "Exit", Prompt: choicePrompt("Exit", "Route through nodes or connect directly.", []tui.PromptOption{{Label: "Route", Value: "route", Description: "Use provider nodes."}, {Label: "Direct", Value: "direct", Description: "Do not route through a proxy node."}}, "route")}, {Key: "Clients", Prompt: choicePrompt("Allowed clients", "Who can authenticate?", []tui.PromptOption{{Label: "Everyone", Value: "everyone"}, {Label: "This computer only", Value: "loopback"}, {Label: "Custom CIDRs", Value: "custom"}}, "everyone")}, {Key: "CustomClients", Prompt: prompt(tui.TextPrompt, "Add account · Custom clients", "CIDR ranges", "Used when Custom CIDRs was chosen.", "")}, {Key: "Pre-hop", Prompt: prompt(tui.SecretPrompt, "Add account · Pre-hop", "Pre-hop URL", "Optional proxy before the VPN node.", "")}, {Key: "Post-hop", Prompt: prompt(tui.SecretPrompt, "Add account · Post-hop", "Post-hop URL", "Optional proxy after the VPN node.", "")}}
	s.dialog = &accountsDialog{wizard: tui.NewWizard("Add account", steps)}
	s.dialog.validate = func(v map[string]string) error {
		if !config.ValidAccountLogin(v["Login"]) {
			return errors.New("invalid account login")
		}
		if v["Clients"] == "custom" {
			if _, e := config.ParseAllowedClients(v["CustomClients"]); e != nil {
				return errors.New("invalid custom client CIDR")
			}
		}
		if country := countryAny(v["Country"]); country != "" {
			if _, ok := config.ParseCountryList(country); !ok {
				return errors.New("invalid country code")
			}
		}
		return nil
	}
	s.dialog.onResult = func(v map[string]string) tea.Cmd {
		clients := v["Clients"]
		switch clients {
		case "everyone":
			clients = "*"
		case "custom":
			clients = v["CustomClients"]
		}
		s.pendingLogin = v["Login"]
		return s.bg("add account", func() tea.Msg {
			_, e := AccountAdd(s.env.Home, AccountAddOptions{Login: v["Login"], Provider: providerValue(v["Provider"]), Country: countryAny(v["Country"]), Clients: clients, Exit: v["Exit"], ExitGiven: true, PreSOCKS5: v["Pre-hop"], PostSOCKS5: v["Post-hop"]})
			return s.finishMutation(e, "Account added · first login sets password", v["Login"])
		})
	}
	return nil
}
func prompt(k tui.PromptKind, title, label, hint, value string) *tui.Prompt {
	return tui.NewPrompt(k, title, label, hint, value)
}
func choicePrompt(label, hint string, opts []tui.PromptOption, value string) *tui.Prompt {
	p := prompt(tui.ChoicePrompt, "Account setup · "+label, label, hint, value)
	p.Options = opts
	return p
}
func (s *accountsScreen) providerOptions() []tui.PromptOption {
	out := []tui.PromptOption{}
	for _, p := range s.providers {
		out = append(out, tui.PromptOption{Label: p, Value: p, Description: s.providerNodes[p] + " nodes"})
	}
	return out
}
func (s *accountsScreen) beginClone(a accountInfo) tea.Cmd {
	p := prompt(tui.TextPrompt, "Clone account", "New login", "Other fields, password state and hops are copied.", "")
	p.Validate = func(v string) error {
		if !config.ValidAccountLogin(v) {
			return errors.New("invalid account login")
		}
		return nil
	}
	s.dialog = &accountsDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd {
		s.pendingLogin = v["value"]
		return s.bg("clone account", func() tea.Msg {
			_, e := AccountClone(s.env.Home, AccountCloneOptions{Source: a.Login, Login: v["value"]})
			return s.finishMutation(e, "Account cloned", v["value"])
		})
	}}
	return nil
}
func (s *accountsScreen) openEdit(a accountInfo, id string) tea.Cmd {
	f := map[string]struct {
		label, hint, value string
		kind               tui.PromptKind
	}{"provider": {"Provider", "Comma-separated providers; blank means any.", a.Provider, tui.TextPrompt}, "countries": {"Countries", s.countryFieldHint(strings.Split(a.Provider, ",")), a.ExitCountry, tui.TextPrompt}, "exit": {"Exit", "Route or direct.", a.Exit, tui.ChoicePrompt}, "clients": {"Allowed clients", "Everyone, loopback or custom CIDR ranges.", a.AllowedClients, tui.TextPrompt}, "pre": {"Pre-hop", "Proxy URL; enter none to clear. Empty cancels.", "", tui.SecretPrompt}, "post": {"Post-hop", "Proxy URL; enter none to clear. Empty cancels.", "", tui.SecretPrompt}}[id]
	if f.label == "" {
		return nil
	}
	p := prompt(f.kind, "Account "+a.Login+" · "+f.label, f.label, f.hint, f.value)
	if id == "countries" {
		p.Validate = func(v string) error {
			if country := countryAny(v); country != "" {
				if _, ok := config.ParseCountryList(country); !ok {
					return errors.New("invalid country code")
				}
			}
			return nil
		}
	}
	if id == "clients" {
		p.Validate = func(v string) error {
			if strings.EqualFold(strings.TrimSpace(v), "everyone") {
				return nil
			}
			if _, err := config.ParseAllowedClients(v); err != nil {
				return errors.New("invalid custom client CIDR")
			}
			return nil
		}
	}
	if id == "exit" {
		p.Options = []tui.PromptOption{{Label: "Route", Value: "route"}, {Label: "Direct", Value: "direct"}}
	}
	s.dialog = &accountsDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd { s.pendingLogin = a.Login; return s.mutateEdit(a, id, v) }}
	return nil
}
func (s *accountsScreen) mutateEdit(a accountInfo, id string, v map[string]string) tea.Cmd {
	x := v["value"]
	if id == "pre" || id == "post" {
		if x == "" && v["clear"] != "true" {
			s.dialog = nil
			return nil
		}
	}
	o := AccountUpdateOptions{Login: a.Login}
	switch id {
	case "provider":
		z := providerValue(x)
		o.Provider = &z
	case "countries":
		z := countryAny(x)
		o.Country = &z
	case "exit":
		o.Exit = &x
	case "clients":
		if strings.EqualFold(strings.TrimSpace(x), "everyone") {
			x = "*"
		}
		o.Clients = &x
	case "pre":
		o.PreSOCKS5 = &x
	case "post":
		o.PostSOCKS5 = &x
	}
	return s.bg("update account", func() tea.Msg {
		_, e := AccountUpdate(s.env.Home, o)
		return s.finishMutation(e, "Account updated", a.Login)
	})
}
func (s *accountsScreen) mutateRemove(login string) tea.Cmd {
	return s.bg("remove account", func() tea.Msg {
		e := AccountRemove(s.env.Home, login)
		return s.finishMutation(e, "Account removed", "")
	})
}
func (s *accountsScreen) notice(text string) tea.Cmd {
	if s.env.Toast == nil || text == "" {
		return nil
	}
	return s.env.Toast(s.safeNotice(text))
}
func (s *accountsScreen) safeNotice(text string) string {
	secrets := []string{}
	if d := s.dialog; d != nil {
		if d.prompt != nil {
			secrets = append(secrets, d.prompt.Value)
		}
		if d.wizard != nil {
			for _, v := range d.wizard.Values {
				secrets = append(secrets, v)
			}
			for _, step := range d.wizard.Steps {
				if step.Prompt != nil {
					secrets = append(secrets, step.Prompt.Value)
				}
			}
		}
	}
	return redactProviderSecrets(text, secrets...)
}
func (s *accountsScreen) finishMutation(e error, n, login string) tea.Msg {
	var rows []accountInfo
	if e == nil {
		rows, e = AccountList(s.env.Home)
	}
	return accountsMutationDone{Rows: rows, Err: e, Notice: n, Login: login}
}
func providerValue(v string) string {
	if v == "any" {
		return ""
	}
	return strings.TrimSpace(v)
}
func countryAny(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "any") {
		return ""
	}
	return strings.TrimSpace(v)
}
func accountsSafePromptError(e error, value string) string {
	text := accountsErrorText(e)
	if value != "" {
		text = strings.ReplaceAll(text, value, "[redacted]")
		if u, err := url.Parse(value); err == nil && u.User != nil {
			text = strings.ReplaceAll(text, u.User.String(), "[redacted]")
			if p, ok := u.User.Password(); ok {
				text = strings.ReplaceAll(text, p, "[redacted]")
			}
		}
	}
	return text
}
func accountsErrorText(err error) string {
	var ce *cli.Error
	if errors.As(err, &ce) && ce.Message != "" {
		if ce.Hint != "" {
			return ce.Message + " — " + ce.Hint
		}
		return ce.Message
	}
	return "Account operation failed; check the account fields."
}
