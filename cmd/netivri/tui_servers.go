package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type serversStatusSource func(context.Context, string) (angel.State, angel.Status, error)
type serversScreen struct {
	home          string
	env           tui.Env
	status        serversStatusSource
	rows          []serverView
	angelRunning  bool
	serverRunning map[string]bool
	table         *tui.Table
	detail        *serverView
	detailRunning bool
	pendingDetail string
	generation    uint64
	errText       string
	visible       int
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

var serversColumns = []tui.Column{{Title: "Name", Width: 16}, {Title: "Protocol", Width: 14}, {Title: "Bind", Width: 16}, {Title: "Port", Width: 6}, {Title: "Accounts", Width: 16}, {Title: "State", Width: 14}, {Title: "Counters", Width: 24}}

func newServersScreen(home string, env tui.Env) tui.Screen {
	return &serversScreen{home: home, env: env, status: angelProbeLive, table: tui.NewTable(serversColumns, nil)}
}
func (s *serversScreen) Title() string        { return "Servers" }
func (s *serversScreen) CapturingInput() bool { return s.detail != nil }
func (s *serversScreen) KeyHints() string {
	if s.detail != nil {
		return "Esc/q back  r reload"
	}
	return "↑/↓ select  Enter details  r reload  q quit"
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
		s.visible = max(1, m.Height-4)
		return s, nil
	}
	if m, ok := msg.(tui.BackgroundMsg); ok {
		msg = m.Value
	}
	switch v := msg.(type) {
	case serversLoaded:
		if v.id != s.generation {
			return s, nil
		}
		if v.err != nil {
			s.errText = serversErrorText(v.err)
			s.rows = nil
			s.table.SetRows(nil)
			return s, nil
		}
		s.errText = ""
		s.detail = nil
		s.rows = v.rows
		s.angelRunning = v.running
		s.serverRunning = serverRunningMap(v.status)
		s.table.SetRows(serverRows(s.rows, s.angelRunning, s.serverRunning))
		return s, nil
	case serversDetailLoaded:
		if v.id != s.generation || v.name != s.pendingDetail {
			return s, nil
		}
		s.pendingDetail = ""
		if v.err != nil {
			s.errText = serversErrorText(v.err)
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
		return s, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch k.String() {
	case "r":
		if s.detail != nil {
			return s, s.detailCmd(s.detail.Name)
		}
		return s, s.loadCmd()
	case "esc", "q":
		s.errText = ""
		if s.detail != nil || s.pendingDetail != "" {
			s.nextID()
			s.detail = nil
			s.pendingDetail = ""
		}
		return s, nil
	case "enter":
		if s.detail == nil {
			return s, s.detailCmdForSelected()
		}
	case "up", "down", "j", "k", "pgup", "pgdown", "home", "end":
		if s.detail == nil {
			s.table.UpdateKey(k.String(), max(1, s.visible))
		}
	}
	return s, nil
}
func (s *serversScreen) detailCmdForSelected() tea.Cmd {
	r, ok := s.table.SelectedRow()
	if !ok {
		return nil
	}
	name, _ := r.Data.(string)
	return s.detailCmd(name)
}
func (s *serversScreen) detailCmd(name string) tea.Cmd {
	id := s.nextID()
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
	s.visible = max(1, height-4)
	var lines []string
	if s.errText != "" {
		lines = []string{s.errText}
	} else if v := s.detail; v != nil {
		lines = []string{serversSafeDetailValue(v.Name), "Protocol: " + serversSafeDetailValue(v.Protocol), "Bind: " + serversSafeDetailValue(v.Bind), "Port: " + strconv.Itoa(v.Port), "Accounts: " + serversSafeDetailValue(v.Accounts), "Public host: " + serversSafeDetailValue(v.PublicHost), "Members: " + serversSafeDetailValue(strings.Join(v.Members, ", ")), fmt.Sprintf("Default: %t", v.Default), fmt.Sprintf("Single user: %t", v.SingleUser), "Parameters: " + serversSafeDetailValue(strings.Join(v.Params, ", "))}
		state := serverState(s.angelRunning, s.serverRunning[v.Name])
		lines = append(lines, "State: "+state)
		if state == "running" {
			lines = append(lines, fmt.Sprintf("Counters: u:%d s:%d r:%d a:%d", v.Unclassified, v.SlowFirstByte, v.Replayed, v.Ambiguous))
		}
	} else {
		lines = append(lines, serversTableHeader(s.table, width))
		if len(s.rows) == 0 {
			lines = append(lines, "No servers configured")
		} else {
			lines = append(lines, strings.Split(s.table.View(width, max(1, height-4)), "\n")...)
		}
	}
	lines = append(lines, "", "Changes via netivri server add, server link, server rotate, server remove")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(l)
	}
	return strings.Join(lines, "\n")
}
func serversTableHeader(t *tui.Table, width int) string {
	h := tui.NewTable(t.Columns, []tui.Row{{Cells: serversColumnNames(t.Columns)}})
	h.Selected = -1
	return strings.TrimPrefix(h.View(width, 1), "> ")
}
func serversColumnNames(c []tui.Column) []string {
	r := make([]string, len(c))
	for i := range c {
		r[i] = c[i].Title
	}
	return r
}
