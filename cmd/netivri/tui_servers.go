package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type serversStatusSource func(context.Context, string) (angel.State, angel.Status, error)
type serversScreen struct {
	home             string
	env              tui.Env
	status           serversStatusSource
	rows             []serverView
	angelRunning     bool
	serverRunning    map[string]bool
	detail           *serverView
	detailRunning    bool
	detailParams     map[string]string
	detailAccounts   []string
	pendingDetail    string
	pendingSelect    string
	pendingLink      string
	pendingConfirm   string
	pendingServer    string
	accountLogins    []string
	generation       uint64
	detailGeneration uint64
	errText          string
	theme            tui.Theme
	workspace        *tui.Workspace
	cardName         string
	dialog           *serversDialog
}
type serversLoaded struct {
	id       uint64
	rows     []serverView
	status   angel.Status
	running  bool
	accounts []string
	err      error
}
type serversDetailLoaded struct {
	id       uint64
	name     string
	view     serverView
	params   map[string]string
	accounts []string
	status   angel.Status
	running  bool
	err      error
}

var serversColumns = []tui.ListColumn{{Title: "Name", Min: 5, Max: 18, Flex: 3}, {Title: "Protocol", Min: 8, Max: 14, Flex: 3}, {Title: "Port", Min: 5, Max: 5}, {Title: "State", Min: 8, Max: 14, Flex: 2}}

func newServersScreen(home string, env tui.Env) tui.Screen {
	l := tui.NewList(serversColumns, nil)
	l.Title = "Servers"
	l.EmptyText = "No servers configured"
	c := tui.NewCard("Server", nil)
	s := &serversScreen{home: home, env: env, status: angelProbeLive, theme: env.Theme, workspace: tui.NewWorkspace(l, c)}
	l.AddLabel = "+ Add a server [a]"
	if s.theme.Accent != nil {
		s.workspace.SetTheme(s.theme)
	}
	return s
}
func (s *serversScreen) syncWorkspace() {
	if s.workspace == nil {
		return
	}
	l := s.workspace.List
	rows := make([]tui.Row, 0, len(s.rows))
	for _, v := range s.rows {
		state := serverState(s.angelRunning, s.serverRunning[v.Name])
		rows = append(rows, tui.Row{Data: v.Name, Cells: []string{v.Name, v.Protocol, strconv.Itoa(v.Port), state}})
	}
	l.SetRows(rows)
	l.AddLabel = "+ Add a server [a]"
	if len(rows) == 0 {
		l.EmptyText = "No servers configured"
	}
	if s.detail != nil {
		v := s.detail
		state := serverState(s.angelRunning, s.serverRunning[v.Name])
		info := []string{"Protocol: " + serversSafeDetailValue(v.Protocol), "State: " + state}
		if len(v.Members) > 0 {
			info = append(info, "Members: "+serversSafeDetailValue(strings.Join(v.Members, ", ")))
		}
		info = append(info, fmt.Sprintf("Default: %t", v.Default), fmt.Sprintf("Single user: %t", v.SingleUser))
		if state == "running" {
			info = append(info, fmt.Sprintf("Counters: u:%d s:%d r:%d a:%d", v.Unclassified, v.SlowFirstByte, v.Replayed, v.Ambiguous))
		}
		if !s.angelRunning {
			info = append(info, "Changes saved; applied when angel starts")
		}
		s.workspace.Card.Info = info
		s.workspace.Card.Lines = serverCardLines(s, v)
	} else {
		info := []string{"Select a server to see details."}
		if len(s.rows) == 0 {
			info = []string{"No servers configured. Add a server to start."}
		}
		if s.errText != "" {
			info = []string{s.errText}
		}
		s.workspace.Card.Info = info
		s.workspace.Card.Lines = []tui.CardLine{
			{ID: "add", Label: "Add a server...", Hotkey: "a", Kind: tui.CardAction, Action: true},
			{ID: "reload", Label: "Reload", Hotkey: "r", Kind: tui.CardAction, Action: true},
		}
	}
}

func serverCardLines(s *serversScreen, v *serverView) []tui.CardLine {
	lines := []tui.CardLine{
		{ID: "bind", Label: "Bind", Value: serversSafeDetailValue(v.Bind), Kind: tui.CardField},
		{ID: "port", Label: "Port", Value: strconv.Itoa(v.Port), Kind: tui.CardField},
		{ID: "publichost", Label: "Public host", Value: serversSafeDetailValue(v.PublicHost), Kind: tui.CardField},
		{ID: "accounts", Label: "Accounts", Value: serversSafeDetailValue(v.Accounts), Kind: tui.CardField},
	}
	if proto, ok := serverProtocolByName(v.Protocol); ok {
		for _, p := range proto.Parameters {
			val := s.detailParams[p.Name]
			if p.Secret {
				if val == "" {
					val = "not set"
				} else {
					val = "set"
				}
			}
			lines = append(lines, tui.CardLine{ID: serverParamPrefix + p.Name, Label: p.Label, Value: serversSafeDetailValue(val), Kind: tui.CardField})
		}
	}
	return append(lines,
		tui.CardLine{ID: "add", Label: "Add a server...", Hotkey: "a", Kind: tui.CardAction, Action: true},
		tui.CardLine{ID: "link", Label: "Show client link...", Hotkey: "l", Kind: tui.CardAction, Action: true},
		tui.CardLine{ID: "rotate", Label: "Rotate keys...", Hotkey: "t", Kind: tui.CardAction, Action: true},
		tui.CardLine{ID: "remove", Label: "Remove...", Hotkey: "d", Kind: tui.CardAction, Action: true},
		tui.CardLine{ID: "reload", Label: "Reload", Hotkey: "r", Kind: tui.CardAction, Action: true})
}
func (s *serversScreen) Title() string        { return "Servers" }
func (s *serversScreen) CapturingInput() bool { return s.dialog != nil }
func (s *serversScreen) SetPaneFocus(pane int) {
	if s.workspace == nil || s.workspace.Width < 100 {
		return
	}
	s.workspace.SetFocus(pane)
}
func (s *serversScreen) FocusPane(reverse bool) bool {
	if s.workspace == nil || s.workspace.Width < 100 {
		return false
	}
	if reverse && s.workspace.Focus() == tui.PaneCard {
		s.workspace.SetFocus(tui.PaneList)
		return true
	}
	if !reverse && s.workspace.Focus() == tui.PaneList {
		s.workspace.SetFocus(tui.PaneCard)
		return true
	}
	return false
}
func (s *serversScreen) Bindings() tui.ActionHints {
	if s.workspace != nil && s.workspace.Focus() == tui.PaneCard {
		return s.workspace.Card.Bindings()
	}
	return append(s.workspace.List.Bindings(), key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")))
}
func (s *serversScreen) KeyHints() string {
	return "↑/↓ select · Enter open · a Add · Tab pane · r Reload"
}
func (s *serversScreen) Init() tea.Cmd  { return s.loadCmd() }
func (s *serversScreen) nextID() uint64 { s.generation++; return s.generation }
func (s *serversScreen) deliver(label string, work func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, work)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
}
func (s *serversScreen) loadCmd() tea.Cmd {
	id := s.nextID()
	return s.deliver("servers", func() tea.Msg {
		rows, err := ServerList(s.home)
		if err != nil {
			return serversLoaded{id: id, err: safeServersError(err)}
		}
		clearServerRuntime(rows)
		ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
		defer cancel()
		_, st, err := s.status(ctx, s.home)
		running := err == nil
		if running {
			applyServersRuntime(rows, st)
		} else {
			st = angel.Status{}
		}
		return serversLoaded{id: id, rows: rows, status: st, running: running, accounts: serverAccountLogins(s.home)}
	})
}
func serverAccountLogins(home string) []string {
	c, e := loadConfig(home)
	if e != nil {
		return nil
	}
	out := make([]string, 0, len(c.Accounts))
	for _, a := range c.Accounts {
		out = append(out, a.Login)
	}
	return out
}
func serverConfigFacts(home, name string) (map[string]string, []string) {
	c, e := loadConfig(home)
	if e != nil {
		return nil, nil
	}
	entry, _ := c.GetServer(name)
	if entry == nil {
		return nil, nil
	}
	params := cloneParams(entry.Params)
	accounts := []string{}
	if entry.Accounts == "*" {
		if specs, e := c.ResolveServers(); e == nil {
			for _, sp := range specs {
				if sp.Name == name {
					accounts = append(accounts, sp.Accounts...)
					break
				}
			}
		}
	} else {
		accounts = strings.Split(entry.Accounts, ",")
	}
	return params, accounts
}
func safeServersError(err error) error {
	var ce *cli.Error
	if errors.As(err, &ce) {
		msg, hint := ce.Message, ce.Hint
		if ce.Code == "CONFIG_INVALID" || ce.Code == "INTERNAL" {
			msg = "could not load server configuration"
			hint = "check the home configuration and try r to reload"
		}
		return &cli.Error{Code: ce.Code, Message: msg, Hint: hint, Exit: ce.Exit}
	}
	return &cli.Error{Code: "INTERNAL", Message: "could not load server configuration", Hint: "check the home configuration and try r to reload", Exit: cli.ExitInternal}
}
func (s *serversScreen) Update(msg tea.Msg) (tui.Screen, tea.Cmd) {
	if m, ok := msg.(tea.WindowSizeMsg); ok {
		s.updateWindowSize(msg, m)
		return s, nil
	}
	if d, ok := msg.(*serversDialog); ok {
		s.dialog = d
		return s, nil
	}
	if s.dialog != nil {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg:
			return s, s.updateDialog(msg)
		}
	}
	if m, ok := msg.(tui.BackgroundMsg); ok {
		msg = m.Value
	}
	if cmd, ok := s.updateScreenMsg(msg); ok {
		return s, cmd
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || s.workspace == nil {
		return s, nil
	}
	return s, s.updateKey(k)
}
func (s *serversScreen) updateWindowSize(msg tea.Msg, m tea.WindowSizeMsg) {
	if s.dialog != nil {
		s.dialog.Update(msg)
	}
	if s.workspace != nil {
		s.workspace.SetSize(m.Width, m.Height)
	}
}
func (s *serversScreen) updateDialog(msg tea.Msg) tea.Cmd {
	m, cmd := s.dialog.Update(msg)
	if sd, ok := m.(*serversDialog); ok {
		s.dialog = sd
	}
	return cmd
}

// updateScreenMsg reports false for messages that are not handled here (keys, unknown types).
func (s *serversScreen) updateScreenMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case tui.ThemeMsg:
		s.onTheme(v)
		return nil, true
	case serversLoaded:
		return s.onLoaded(v), true
	case serversDetailLoaded:
		return s.onDetailLoaded(v), true
	case serversDialogResult:
		return s.onDialogResult(v), true
	case serversModalAction:
		return s.onModalAction(v), true
	case serversAddDone:
		return s.onAddDone(v), true
	case serversUpdateDone:
		return s.onUpdateDone(v), true
	case serversRotateDone:
		return s.onRotateDone(v), true
	case serversRemoveDone:
		return s.onRemoveDone(v), true
	case serversLinkDone:
		return s.onLinkDone(v), true
	case tui.ConfirmResultMsg:
		return s.onConfirm(v), true
	case tea.MouseClickMsg, tea.MouseWheelMsg:
		return s.onMouse(msg), true
	}
	return nil, false
}
func (s *serversScreen) onTheme(v tui.ThemeMsg) {
	s.theme = v.Theme
	if s.workspace != nil {
		s.workspace.SetTheme(v.Theme)
	}
	if s.dialog != nil {
		s.dialog.SetTheme(v.Theme)
	}
}
func (s *serversScreen) onLoaded(v serversLoaded) tea.Cmd {
	if v.id != s.generation {
		return nil
	}
	if v.err != nil {
		s.errText = serversErrorText(v.err)
		if s.workspace != nil {
			s.workspace.Card.Info = []string{s.errText}
		}
		s.rows = nil
		s.syncWorkspace()
		return nil
	}
	s.errText = ""
	s.rows = v.rows
	s.angelRunning = v.running
	s.serverRunning = serverRunningMap(v.status)
	s.accountLogins = v.accounts
	s.syncWorkspace()
	cmd := s.detailCmdForSelected()
	if s.pendingSelect != "" {
		for i, r := range s.workspace.List.Rows {
			if n, ok := r.Data.(string); ok && n == s.pendingSelect {
				s.workspace.List.Selected = i
				cmd = s.detailCmd(n)
				break
			}
		}
		s.pendingSelect = ""
	}
	return cmd
}
func (s *serversScreen) onDetailLoaded(v serversDetailLoaded) tea.Cmd {
	if v.id != s.detailGeneration || v.name != s.pendingDetail {
		return nil
	}
	s.pendingDetail = ""
	if v.err != nil {
		s.errText = serversErrorText(v.err)
		if s.workspace != nil {
			s.workspace.Card.Info = []string{s.errText}
		}
		var ce *cli.Error
		if errors.As(v.err, &ce) && ce.Code == "SERVER_NOT_FOUND" {
			s.detail = nil
			return s.loadCmd()
		}
		return nil
	}
	s.errText = ""
	vv := []serverView{v.view}
	clearServerRuntime(vv)
	if v.running {
		applyServersRuntime(vv, v.status)
	}
	s.angelRunning = v.running
	s.serverRunning = serverRunningMap(v.status)
	s.detailRunning = v.running && s.serverRunning[v.name]
	s.detail = &vv[0]
	s.cardName = v.name
	s.detailParams = v.params
	s.detailAccounts = v.accounts
	s.syncWorkspace()
	if s.pendingLink == v.name {
		s.pendingLink = ""
		return s.showLinkChoice(v.name)
	}
	return nil
}
func (s *serversScreen) onDialogResult(v serversDialogResult) tea.Cmd {
	if s.dialog == nil {
		return nil
	}
	if v.Canceled {
		s.dialog = nil
		return nil
	}
	d := s.dialog
	if d.onResult == nil {
		s.dialog = nil
		return nil
	}
	if d.wizard != nil {
		d.wizard.SetSaving(true)
	} else if d.prompt != nil {
		d.prompt.SetSaving(true)
	}
	return d.onResult(v.Values)
}
func (s *serversScreen) onModalAction(v serversModalAction) tea.Cmd {
	if s.dialog == nil || s.dialog.modal == nil {
		return nil
	}
	m := s.dialog.modal
	s.dialog = nil
	if v.Index >= 0 && m.onPick != nil {
		return m.onPick(v.Index)
	}
	return nil
}
func (s *serversScreen) onAddDone(v serversAddDone) tea.Cmd {
	if v.err != nil {
		if s.dialog != nil && s.dialog.wizard != nil {
			s.dialog.wizard.SetSaving(false)
			s.dialog.wizard.SetError(errors.New(serversSafePromptError(v.err)))
			return nil
		}
		return s.notice(serversSafePromptError(v.err))
	}
	name := v.result.Name
	s.pendingSelect = name
	s.dialog = &serversDialog{modal: newServersMenuModal(s.theme, "Server added", serverAddResultLines(v.result), []string{"Show client link", "Close"}, 1, func(i int) tea.Cmd {
		if i == 0 {
			return s.beginShowLink(name)
		}
		return nil
	})}
	return tea.Batch(s.loadCmd(), s.notice("Server added"))
}
func (s *serversScreen) onUpdateDone(v serversUpdateDone) tea.Cmd {
	if v.err != nil {
		if s.dialog != nil && s.dialog.prompt != nil {
			s.dialog.prompt.SetSaving(false)
			s.dialog.prompt.SetError(errors.New(serversSafePromptError(v.err, s.dialog.prompt.Value)))
			return nil
		}
		return s.notice(serversSafePromptError(v.err))
	}
	s.dialog = nil
	var cmds []tea.Cmd
	if s.detail != nil && s.detail.Name == v.name {
		cmds = append(cmds, s.detailCmd(v.name))
	}
	cmds = append(cmds, s.loadCmd(), s.notice("Server updated"))
	return tea.Batch(cmds...)
}
func (s *serversScreen) onRotateDone(v serversRotateDone) tea.Cmd {
	if v.err != nil {
		return s.notice(serversSafePromptError(v.err))
	}
	msg := "Keys rotated for " + v.result.Name
	if v.result.SharedCredentials {
		msg += " (shared credentials)"
	}
	var cmds []tea.Cmd
	if s.detail != nil && s.detail.Name == v.result.Name {
		cmds = append(cmds, s.detailCmd(v.result.Name))
	}
	cmds = append(cmds, s.loadCmd(), s.notice(msg))
	return tea.Batch(cmds...)
}
func (s *serversScreen) onRemoveDone(v serversRemoveDone) tea.Cmd {
	if v.err != nil {
		return s.notice(serversSafePromptError(v.err))
	}
	s.dialog = nil
	s.detail = nil
	s.detailParams = nil
	s.detailAccounts = nil
	if s.workspace != nil && s.workspace.Drilled {
		s.workspace.Drilled = false
		s.workspace.SetFocus(tui.PaneList)
	}
	s.syncWorkspace()
	return tea.Batch(s.loadCmd(), s.notice("Server removed"))
}
func (s *serversScreen) onLinkDone(v serversLinkDone) tea.Cmd {
	if v.err != nil {
		s.dialog = nil
		return s.notice(serversSafePromptError(v.err))
	}
	s.dialog = &serversDialog{modal: newServersLinkModal(s.theme, v.name, v.link, v.format)}
	return nil
}
func (s *serversScreen) onConfirm(v tui.ConfirmResultMsg) tea.Cmd {
	action, name := s.pendingConfirm, s.pendingServer
	s.pendingConfirm, s.pendingServer = "", ""
	if !v.Confirmed || action == "" {
		return nil
	}
	switch action {
	case "rotate":
		return s.deliver("rotate server", func() tea.Msg {
			r, e := ServerRotate(s.home, name, "")
			return serversRotateDone{result: r, err: e}
		})
	case "remove":
		return s.deliver("remove server", func() tea.Msg {
			r, e := ServerRemove(s.home, name)
			return serversRemoveDone{result: r, err: e}
		})
	}
	return nil
}
func (s *serversScreen) onMouse(msg tea.Msg) tea.Cmd {
	if s.workspace == nil {
		return nil
	}
	old := s.workspace.List.Selected
	activate := s.workspace.Update(msg)
	if s.workspace.List.Selected != old {
		if r, ok := s.workspace.List.SelectedRow(); ok {
			name, _ := r.Data.(string)
			return s.detailCmd(name)
		}
	}
	if activate {
		if s.workspace.List.IsAdd() {
			return s.beginAddWizard()
		}
		if s.workspace.Focus() == tui.PaneCard || s.workspace.Width < 100 && s.workspace.Drilled {
			return s.activateCard()
		}
		if s.workspace.Width >= 100 && s.detail != nil {
			return s.detailCmd(s.detail.Name)
		}
	}
	return nil
}
func (s *serversScreen) updateKey(k tea.KeyPressMsg) tea.Cmd {
	keyName := k.String()
	switch keyName {
	case "a":
		return s.keyAdd()
	case "l", "t", "d":
		return s.keyCardAction(keyName)
	case "tab":
		s.keyTab()
		return nil
	case "shift+tab":
		s.keyShiftTab()
		return nil
	case "r":
		return s.keyReload()
	case "esc":
		s.keyEsc(k)
		return nil
	case "enter":
		return s.keyEnter(k)
	case "up", "down", "j", "k", "pgup", "pgdown", "home", "end", "left", "right":
		return s.moveSelection(k)
	}
	return nil
}
func (s *serversScreen) keyAdd() tea.Cmd {
	if s.dialog == nil && (s.workspace.Width >= 100 || !s.workspace.Drilled) {
		return s.beginAddWizard()
	}
	return nil
}
func (s *serversScreen) keyCardAction(keyName string) tea.Cmd {
	if s.dialog == nil && s.detail != nil && (s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneCard || s.workspace.Width < 100 && s.workspace.Drilled) {
		id := map[string]string{"l": "link", "t": "rotate", "d": "remove"}[keyName]
		return s.cardAction(id)
	}
	return nil
}
func (s *serversScreen) keyTab() {
	if s.workspace != nil && s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneList {
		s.workspace.SetFocus(tui.PaneCard)
	}
}
func (s *serversScreen) keyShiftTab() {
	if s.workspace != nil && s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneCard {
		s.workspace.SetFocus(tui.PaneList)
	}
}
func (s *serversScreen) keyReload() tea.Cmd {
	if s.workspace != nil && (s.workspace.Width < 100 && s.workspace.Drilled || s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneCard) {
		return s.detailCmdForSelected()
	}
	return s.loadCmd()
}
func (s *serversScreen) keyEsc(k tea.KeyPressMsg) {
	if s.workspace != nil && s.workspace.Width < 100 && s.workspace.Drilled {
		s.workspace.Update(k)
		s.detailGeneration++
		s.pendingDetail = ""
		s.errText = ""
		s.detail = nil
		s.syncWorkspace()
		return
	}
	s.errText = ""
	if s.workspace != nil && s.workspace.Width >= 100 {
		s.workspace.SetFocus(tui.PaneList)
	}
	if s.detail != nil || s.pendingDetail != "" {
		s.detailGeneration++
		s.pendingDetail = ""
	}
	s.detail = nil
	s.syncWorkspace()
}
func (s *serversScreen) keyEnter(k tea.KeyPressMsg) tea.Cmd {
	if s.workspace.List.IsAdd() {
		return s.beginAddWizard()
	}
	if s.workspace.Width < 100 {
		if !s.workspace.Drilled {
			s.workspace.Update(k)
			return s.detailCmdForSelected()
		}
		if s.workspace.Card.Update(k) {
			return s.activateCard()
		}
		return nil
	}
	if s.workspace.Focus() == tui.PaneCard {
		s.workspace.Update(k)
		return s.activateCard()
	}
	return s.detailCmdForSelected()
}
func (s *serversScreen) moveSelection(k tea.KeyPressMsg) tea.Cmd {
	if s.workspace == nil {
		return nil
	}
	old := s.workspace.List.Selected
	s.workspace.Update(k)
	if s.workspace.List.Selected != old {
		if r, ok := s.workspace.List.SelectedRow(); ok {
			name, _ := r.Data.(string)
			return s.detailCmd(name)
		}
	}
	return nil
}
func (s *serversScreen) detailCmdForSelected() tea.Cmd {
	r, ok := s.workspace.List.SelectedRow()
	if !ok {
		return nil
	}
	name, _ := r.Data.(string)
	return s.detailCmd(name)
}
func (s *serversScreen) detailCmd(name string) tea.Cmd {
	id := s.detailGeneration + 1
	s.detailGeneration = id
	s.pendingDetail = name
	return s.deliver("server detail", func() tea.Msg {
		v, err := ServerShow(s.home, name)
		if err != nil {
			return serversDetailLoaded{id: id, name: name, err: safeServersError(err)}
		}
		params, accounts := serverConfigFacts(s.home, name)
		ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
		defer cancel()
		_, st, err := s.status(ctx, s.home)
		if err != nil {
			st = angel.Status{}
		}
		return serversDetailLoaded{id: id, name: name, view: v, params: params, accounts: accounts, status: st, running: err == nil}
	})
}
func clearServerRuntime(rows []serverView) {
	for i := range rows {
		rows[i].Unclassified = 0
		rows[i].SlowFirstByte = 0
		rows[i].Replayed = 0
		rows[i].Ambiguous = 0
	}
}
func applyServersRuntime(rows []serverView, st angel.Status) {
	by := map[string]angel.ServerInfo{}
	for _, v := range st.Servers {
		by[v.Name] = v
	}
	for i := range rows {
		if x, ok := by[rows[i].Name]; ok {
			rows[i].Unclassified = x.Unclassified
			rows[i].SlowFirstByte = x.SlowFirstByte
			rows[i].Replayed = x.Replayed
			rows[i].Ambiguous = x.Ambiguous
		}
	}
}
func serverRunningMap(st angel.Status) map[string]bool {
	out := make(map[string]bool, len(st.Servers))
	for _, v := range st.Servers {
		out[v.Name] = true
	}
	return out
}
func serverState(angelRunning, serverRunning bool) string {
	if !angelRunning {
		return "angel stopped"
	}
	if !serverRunning {
		return "not applied"
	}
	return "running"
}
func serverRows(rows []serverView, angelRunning bool, running map[string]bool) []tui.Row {
	out := make([]tui.Row, 0, len(rows))
	for _, v := range rows {
		state := serverState(angelRunning, running[v.Name])
		counters := ""
		if state == "running" {
			counters = fmt.Sprintf("u:%d s:%d r:%d a:%d", v.Unclassified, v.SlowFirstByte, v.Replayed, v.Ambiguous)
		}
		out = append(out, tui.Row{Data: v.Name, Cells: []string{v.Name, v.Protocol, v.Bind, strconv.Itoa(v.Port), v.Accounts, state, counters}})
	}
	return out
}
func serversErrorText(err error) string {
	var ce *cli.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case "CONFIG_INVALID", "INTERNAL":
			return "could not load server configuration — check the home configuration and try r to reload"
		case "SERVER_NOT_FOUND":
			return "server no longer exists; press r to reload"
		}
		msg := ce.Message
		if ce.Hint != "" {
			msg += " — " + ce.Hint
		}
		return msg
	}
	return "could not load servers"
}
func serversSafeDetailValue(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, v)
}
func (s *serversScreen) View(width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	if s.dialog != nil {
		s.dialog.SetTheme(s.theme)
		return s.dialog.View(width, height)
	}
	if s.workspace == nil {
		return ""
	}
	s.syncWorkspace()
	return s.workspace.View(width, height)
}
