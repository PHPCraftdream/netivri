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
	pendingDetail    string
	generation       uint64
	detailGeneration uint64
	errText          string
	visible          int
	workspace        *tui.Workspace
	cardName         string
}
type serversLoaded struct {
	id      uint64
	rows    []serverView
	status  angel.Status
	running bool
	err     error
}
type serversDetailLoaded struct {
	id      uint64
	name    string
	view    serverView
	status  angel.Status
	running bool
	err     error
}

var serversColumns = []tui.ListColumn{{Title: "Name", Min: 5, Max: 18, Flex: 3}, {Title: "Protocol", Min: 8, Max: 14, Flex: 3}, {Title: "Port", Min: 5, Max: 5}, {Title: "State", Min: 8, Max: 14, Flex: 2}}

func newServersScreen(home string, env tui.Env) tui.Screen {
	l := tui.NewList(serversColumns, nil)
	l.Title = "Servers"
	l.EmptyText = "No servers configured"
	l.AddLabel = ""
	c := tui.NewCard("Server", nil)
	return &serversScreen{home: home, env: env, status: angelProbeLive, workspace: tui.NewWorkspace(l, c)}
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
	l.AddLabel = ""
	if len(rows) == 0 {
		l.EmptyText = "No servers configured"
	}
	if s.detail != nil {
		v := s.detail
		state := serverState(s.angelRunning, s.serverRunning[v.Name])
		info := []string{"Protocol: " + serversSafeDetailValue(v.Protocol), "Bind: " + serversSafeDetailValue(v.Bind), "Port: " + strconv.Itoa(v.Port), "Accounts: " + serversSafeDetailValue(v.Accounts), "Public host: " + serversSafeDetailValue(v.PublicHost), "Members: " + serversSafeDetailValue(strings.Join(v.Members, ", ")), fmt.Sprintf("Default: %t", v.Default), fmt.Sprintf("Single user: %t", v.SingleUser), "Parameters: " + serversSafeDetailValue(strings.Join(v.Params, ", ")), "State: " + state}
		if state == "running" {
			info = append(info, fmt.Sprintf("Counters: u:%d s:%d r:%d a:%d", v.Unclassified, v.SlowFirstByte, v.Replayed, v.Ambiguous))
		}
		if !s.angelRunning {
			info = append(info, "Changes saved; applied when angel starts")
		}
		info = append(info, "Changes: netivri server add · link · rotate · remove")
		s.workspace.Card.Info = info
		s.workspace.Card.Lines = []tui.CardLine{{ID: "reload", Label: "Reload", Hotkey: "r", Kind: tui.CardAction, Action: true}}
	} else {
		info := []string{"Changes: netivri server add · link · rotate · remove"}
		if s.errText != "" {
			info = append([]string{s.errText}, info...)
		}
		s.workspace.Card.Info = info
		s.workspace.Card.Lines = []tui.CardLine{{ID: "reload", Label: "Reload", Hotkey: "r", Kind: tui.CardAction, Action: true}}
	}
}
func (s *serversScreen) Title() string        { return "Servers" }
func (s *serversScreen) CapturingInput() bool { return false }
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
	return "↑/↓ select · Enter open · Tab pane · r Reload · Read-only"
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
		return serversLoaded{id: id, rows: rows, status: st, running: running}
	})
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
		if s.workspace != nil {
			s.workspace.SetSize(m.Width, m.Height)
		}
		return s, nil
	}
	if m, ok := msg.(tui.BackgroundMsg); ok {
		msg = m.Value
	}
	switch v := msg.(type) {
	case tui.ThemeMsg:
		if s.workspace != nil {
			s.workspace.SetTheme(v.Theme)
		}
		return s, nil
	case serversLoaded:
		if v.id != s.generation {
			return s, nil
		}
		if v.err != nil {
			s.errText = serversErrorText(v.err)
			if s.workspace != nil {
				s.workspace.Card.Info = []string{s.errText}
			}
			s.rows = nil
			s.syncWorkspace()
			return s, nil
		}
		s.errText = ""
		s.rows = v.rows
		s.angelRunning = v.running
		s.serverRunning = serverRunningMap(v.status)
		s.syncWorkspace()
		return s, s.detailCmdForSelected()
	case serversDetailLoaded:
		if v.id != s.detailGeneration || v.name != s.pendingDetail {
			return s, nil
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
				return s, s.loadCmd()
			}
			return s, nil
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
		s.syncWorkspace()
		return s, nil
	case tea.MouseClickMsg, tea.MouseWheelMsg:
		if s.workspace != nil {
			old := s.workspace.List.Selected
			activate := s.workspace.Update(msg)
			if s.workspace.List.Selected != old {
				if r, ok := s.workspace.List.SelectedRow(); ok {
					name, _ := r.Data.(string)
					return s, s.detailCmd(name)
				}
			}
			if activate && s.workspace.Width >= 100 && s.detail != nil {
				return s, s.detailCmd(s.detail.Name)
			}
		}
		return s, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "tab":
		if s.workspace != nil && s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneList {
			s.workspace.SetFocus(tui.PaneCard)
			return s, nil
		}
		return s, nil
	case "shift+tab":
		if s.workspace != nil && s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneCard {
			s.workspace.SetFocus(tui.PaneList)
			return s, nil
		}
		return s, nil
	case "r":
		if s.workspace != nil && s.workspace.Width < 100 && s.workspace.Drilled {
			return s, s.detailCmdForSelected()
		}
		return s, s.loadCmd()
	case "esc":
		if s.workspace != nil && s.workspace.Width < 100 && s.workspace.Drilled {
			s.workspace.Update(k)
			s.detailGeneration++
			s.pendingDetail = ""
			s.errText = ""
			if s.detail == nil {
				s.workspace.Card.Info = []string{"Changes: netivri server add · link · rotate · remove"}
			}
			return s, nil
		}
		s.errText = ""
		if s.workspace != nil && s.workspace.Width >= 100 {
			s.workspace.SetFocus(tui.PaneList)
		}
		if s.detail != nil || s.pendingDetail != "" {
			s.detailGeneration++
			s.pendingDetail = ""
		}
		return s, nil
	case "enter":
		if s.workspace != nil {
			if s.workspace.Width < 100 && !s.workspace.Drilled {
				s.workspace.Update(k)
				return s, s.detailCmdForSelected()
			}
			if s.workspace.Width >= 100 && s.workspace.Focus() == tui.PaneCard {
				s.workspace.Update(k)
				if s.detail != nil {
					return s, s.detailCmd(s.detail.Name)
				}
				return s, nil
			}
		}
		return s, s.detailCmdForSelected()
	case "up", "down", "j", "k", "pgup", "pgdown", "home", "end", "left", "right":
		if s.workspace != nil {
			old := s.workspace.List.Selected
			s.workspace.Update(k)
			if s.workspace.List.Selected != old {
				if r, ok := s.workspace.List.SelectedRow(); ok {
					name, _ := r.Data.(string)
					return s, s.detailCmd(name)
				}
			}
		}
	}
	return s, nil
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
		ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
		defer cancel()
		_, st, err := s.status(ctx, s.home)
		if err != nil {
			st = angel.Status{}
		}
		return serversDetailLoaded{id: id, name: name, view: v, status: st, running: err == nil}
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
	if s.workspace == nil {
		return ""
	}
	s.syncWorkspace()
	return s.workspace.View(width, height)
}
