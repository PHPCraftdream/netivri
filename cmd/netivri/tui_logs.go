package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Logs screen: the tail of the angel log, the shell side of `netivri
// logs`, through the same reading (ops_logs.go): follow, level filter, since
// window, tail limit and search.

const logsScreenTail = 200

var logsLevels = []string{angel.LevelDebug, angel.LevelInfo, angel.LevelWarn, angel.LevelError}

type logsReadMsg struct {
	entries []angel.LogEntry
	offset  int64
	missing bool
	err     error
}
type logsPollMsg struct {
	entries []angel.LogEntry
	offset  int64
	err     error
}
type logsTick struct{}

type logsScreen struct {
	home          string
	env           tui.Env
	theme         tui.Theme
	workspace     *tui.Workspace
	list          *tui.List
	width, height int

	entries []angel.LogEntry
	offset  int64
	missing bool
	notice  string

	follow bool
	level  string
	since  string
	tail   int

	dialog     tui.Modal
	prompt     *tui.Prompt
	mode       string
	lastFilter string

	read     func(string, angel.LogQuery) ([]angel.LogEntry, int64, error)
	poll     func(string, int64, angel.LogQuery) ([]angel.LogEntry, int64, error)
	follower *angel.LogFollower
	wait     func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
}

func newLogsScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	theme := env.Theme
	if theme.Accent == nil {
		theme = tui.NewTheme()
	}
	l := tui.NewList([]tui.ListColumn{
		{Title: "Time", Width: 20, Min: 10},
		{Title: "Level", Width: 5, Min: 5},
		{Title: "Message", Flex: 1, Min: 8},
	}, nil)
	l.AddLabel = ""
	s := &logsScreen{home: home, env: env, theme: theme, list: l,
		follow: true, level: angel.LevelInfo, tail: logsScreenTail, read: opsLogsRead}
	s.poll = s.pollStep
	s.workspace = tui.NewWorkspace(l, s.buildCard())
	s.workspace.SetTheme(theme)
	s.refresh()
	return s
}

func (s *logsScreen) Title() string        { return "Logs" }
func (s *logsScreen) CapturingInput() bool { return s.dialog != nil }
func (s *logsScreen) Breadcrumb() string {
	if s.width < 100 && s.workspace.Drilled {
		return "Logs / " + s.workspace.Card.Title
	}
	return ""
}
func (s *logsScreen) FocusPane(reverse bool) bool {
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
func (s *logsScreen) SetPaneFocus(p int) {
	if s.width >= 100 {
		s.workspace.SetFocus(p)
	}
}
func (s *logsScreen) Bindings() tui.ActionHints {
	b := append(tui.ActionHints{}, s.list.Bindings()...)
	b = append(b, s.workspace.Card.Bindings()...)
	return append(b,
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow")),
		key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "level")),
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "since")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "tail")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")))
}
func (s *logsScreen) KeyHints() string {
	if s.dialog != nil {
		return "Dialog: confirm or cancel"
	}
	return "↑/↓ scroll  End live  f follow  l level  s since  n tail  / search"
}

func (s *logsScreen) bg(label string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, w)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: w()} }
}

func (s *logsScreen) tickCmd() tea.Cmd {
	wait := s.wait
	if wait == nil {
		wait = tea.Tick
	}
	return wait(angel.FollowEvery, func(time.Time) tea.Msg { return logsTick{} })
}

func (s *logsScreen) query() (angel.LogQuery, error) {
	return opsLogsQuery(s.level, s.since, s.tail, s.env.Now)
}

func (s *logsScreen) readCmd() tea.Cmd {
	return s.bg("Reading the log", func() tea.Msg {
		q, err := s.query()
		if err != nil {
			return logsReadMsg{err: err}
		}
		entries, offset, err := s.read(s.home, q)
		if errors.Is(err, os.ErrNotExist) {
			return logsReadMsg{missing: true}
		}
		return logsReadMsg{entries: entries, offset: offset, err: err}
	})
}

func (s *logsScreen) pollStep(home string, from int64, q angel.LogQuery) ([]angel.LogEntry, int64, error) {
	if s.follower == nil {
		s.follower = angel.NewLogFollower(s.home)
	}
	return s.follower.Poll(from, q)
}

func (s *logsScreen) pollCmd() tea.Cmd {
	return s.bg("Reading new log lines", func() tea.Msg {
		q, err := opsLogsQuery(s.level, s.since, 0, s.env.Now)
		if err != nil {
			return logsPollMsg{err: err}
		}
		entries, offset, err := s.poll(s.home, s.offset, q)
		return logsPollMsg{entries: entries, offset: offset, err: err}
	})
}

func (s *logsScreen) Init() tea.Cmd { return tea.Batch(s.readCmd(), s.tickCmd()) }

func logsErrorText(err error) string {
	if ce, ok := err.(*cli.Error); ok {
		if ce.Hint != "" {
			return ce.Message + " (" + ce.Hint + ")"
		}
		return ce.Message
	}
	return "Reading the log failed."
}

func logsUsageText(err error) string {
	if ce, ok := err.(*cli.Error); ok {
		return strings.TrimPrefix(ce.Message, "logs: ")
	}
	return "That value is not valid."
}

func (s *logsScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
	switch x := m.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = x.Width, x.Height
		s.workspace.SetSize(x.Width, x.Height)
		return s, nil
	case tui.ThemeMsg:
		s.theme = x.Theme
		s.workspace.SetTheme(x.Theme)
		if s.prompt != nil {
			s.prompt.SetTheme(x.Theme)
		}
		return s, nil
	case tui.BackgroundMsg:
		return s.Update(x.Value)
	case logsReadMsg:
		if x.err != nil {
			s.notice = logsErrorText(x.err)
		} else {
			s.notice = ""
			s.missing = x.missing
			if !x.missing {
				s.entries, s.offset = x.entries, x.offset
			}
		}
		s.refresh()
		if s.follow {
			s.pinBottom()
		}
		return s, nil
	case logsPollMsg:
		if x.err != nil {
			if errors.Is(x.err, os.ErrNotExist) {
				s.missing = true
			} else {
				s.notice = logsErrorText(x.err)
			}
		} else {
			s.missing = false
			s.notice = ""
			s.offset = x.offset
			s.entries = append(s.entries, x.entries...)
			if s.tail > 0 && len(s.entries) > s.tail {
				dropped := len(s.entries) - s.tail
				s.entries = s.entries[dropped:]
				if !s.follow && s.list.Filter == "" {
					s.list.Selected = max(0, s.list.Selected-dropped)
				}
			}
		}
		s.refresh()
		if s.follow {
			s.pinBottom()
		}
		return s, nil
	case logsTick:
		return s, tea.Batch(s.pollCmd(), s.tickCmd())
	case tui.PromptResultMsg:
		switch s.mode {
		case "since-prompt":
			return s.applySince(x)
		case "tail-prompt":
			return s.applyTail(x)
		}
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		return s, c
	}
	if k, ok := m.(tea.KeyPressMsg); ok && !s.list.Filtering {
		switch k.String() {
		case "f":
			s.follow = !s.follow
			s.refresh()
			if s.follow {
				s.pinBottom()
			}
			return s, nil
		case "l":
			s.level = nextLogLevel(s.level)
			s.refresh()
			return s, s.readCmd()
		case "s":
			s.openSince()
			return s, nil
		case "n":
			s.openTail()
			return s, nil
		case "/":
			s.startSearch()
			return s, nil
		case "end":
			if !s.follow {
				s.follow = true
				s.refresh()
			}
		case "up", "pgup", "home":
			if s.follow {
				s.follow = false
				s.refresh()
			}
		case "esc":
			if s.list.Filter != "" {
				s.list.EndFilter(false)
				s.lastFilter = s.list.Filter
				s.refresh()
				return s, nil
			}
		}
	}
	if w, ok := m.(tea.MouseWheelMsg); ok && w.Button == tea.MouseWheelUp && s.follow {
		s.follow = false
		s.refresh()
	}
	activated := s.workspace.Update(m)
	if s.list.Filter != s.lastFilter {
		s.lastFilter = s.list.Filter
		s.refresh()
	}
	if s.follow {
		s.pinBottom()
	}
	if activated && s.workspace.Focus() == tui.PaneCard {
		return s.activateCard()
	}
	return s, nil
}

func nextLogLevel(level string) string {
	if level == "" {
		return angel.LevelInfo
	}
	for i, l := range logsLevels {
		if l == level {
			return logsLevels[(i+1)%len(logsLevels)]
		}
	}
	return angel.LevelWarn
}

// pinBottom keeps the newest line selected; with a filter the user drives the
// selection, so the bottom is left alone.
func (s *logsScreen) pinBottom() {
	if s.list.Filter != "" {
		return
	}
	s.list.Selected = max(0, len(s.list.Rows)-1)
	if s.width > 0 {
		s.workspace.SetSize(s.width, s.height)
	}
}

func (s *logsScreen) openSince() {
	p := tui.NewPrompt(tui.TextPrompt, "Logs — since", "Show the last",
		"A duration like 30m, 2h or 7d; empty shows all", s.since)
	p.SetTheme(s.theme)
	s.prompt, s.dialog, s.mode = p, p, "since-prompt"
}

func (s *logsScreen) openTail() {
	p := tui.NewPrompt(tui.NumberPrompt, "Logs — tail", "Newest lines",
		"How many of the newest lines to show; 0 for all", strconv.Itoa(s.tail))
	zero := 0
	p.Min = &zero
	p.SetTheme(s.theme)
	s.prompt, s.dialog, s.mode = p, p, "tail-prompt"
}

func (s *logsScreen) applySince(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.closePrompt()
		return s, nil
	}
	if _, err := opsLogsQuery(s.level, x.Value, s.tail, s.env.Now); err != nil {
		s.prompt.SetError(errors.New(logsUsageText(err)))
		return s, nil
	}
	s.closePrompt()
	s.since = x.Value
	s.refresh()
	return s, s.readCmd()
}

func (s *logsScreen) applyTail(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.closePrompt()
		return s, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(x.Value))
	if err != nil || n < 0 {
		s.prompt.SetError(errors.New("the number of lines must be a whole number, 0 for all"))
		return s, nil
	}
	s.closePrompt()
	s.tail = n
	s.refresh()
	return s, s.readCmd()
}

func (s *logsScreen) closePrompt() {
	s.dialog, s.prompt, s.mode = nil, nil, ""
}

func (s *logsScreen) startSearch() {
	s.workspace.Drilled = false
	s.workspace.SetFocus(tui.PaneList)
	s.list.StartFilter()
}

func (s *logsScreen) activateCard() (tui.Screen, tea.Cmd) {
	card := s.workspace.Card
	if card == nil || card.Selected < 0 || card.Selected >= len(card.Lines) {
		return s, nil
	}
	switch card.Lines[card.Selected].ID {
	case "follow":
		s.follow = !s.follow
		s.refresh()
		if s.follow {
			s.pinBottom()
		}
		return s, nil
	case "level":
		s.level = nextLogLevel(s.level)
		s.refresh()
		return s, s.readCmd()
	case "since":
		s.openSince()
	case "tail":
		s.openTail()
	case "search":
		s.startSearch()
	}
	return s, nil
}

// refresh rebuilds the rows and the card from the current data, keeping the
// selected line and the scroll position of the card.
func (s *logsScreen) refresh() {
	s.syncRows()
	followState := "off"
	if s.follow {
		followState = "on"
	}
	s.list.Title = fmt.Sprintf("Logs — %s and above · follow %s", strings.ToLower(s.level), followState)
	old := s.workspace.Card
	card := s.buildCard()
	card.SetTheme(s.theme)
	if old != nil && old.Title == card.Title {
		var id string
		if old.Selected >= 0 && old.Selected < len(old.Lines) {
			id = old.Lines[old.Selected].ID
		}
		for i, l := range card.Lines {
			if l.ID == id {
				card.Selected = i
			}
		}
		card.Scroll = old.Scroll
		card.Focused = old.Focused
	}
	s.workspace.Card = card
	if s.width > 0 {
		s.workspace.SetSize(s.width, s.height)
	}
}

func (s *logsScreen) syncRows() {
	rows := make([]tui.Row, 0, len(s.entries))
	for _, e := range s.entries {
		rows = append(rows, tui.Row{Cells: []string{logTime(e), e.Level, e.Message}})
	}
	s.list.SetRows(rows)
	switch {
	case s.missing:
		s.list.EmptyText = "The angel has not written a log yet."
	case len(s.entries) == 0:
		s.list.EmptyText = "No entries yet."
	}
}

func (s *logsScreen) buildCard() *tui.Card {
	followState, sinceValue, searchValue := "off", "—", "—"
	if s.follow {
		followState = "on"
	}
	if s.since != "" {
		sinceValue = s.since
	}
	if s.list.Filter != "" {
		searchValue = s.list.Filter
	}
	c := tui.NewCard("Log view — "+strings.ToLower(s.level)+" and above", []tui.CardLine{
		{ID: "follow", Label: "Follow", Value: followState, Hotkey: "f", Kind: tui.CardAction, Action: true},
		{ID: "level", Label: "Level", Value: strings.ToLower(s.level), Hotkey: "l", Kind: tui.CardAction, Action: true},
		{ID: "since", Label: "Since", Value: sinceValue, Hotkey: "s", Kind: tui.CardField, Action: true},
		{ID: "tail", Label: "Tail", Value: strconv.Itoa(s.tail), Hotkey: "n", Kind: tui.CardField, Action: true},
		{ID: "search", Label: "Search", Value: searchValue, Hotkey: "/", Kind: tui.CardField, Action: true},
	})
	c.Info = s.cardInfo()
	return c
}

func (s *logsScreen) cardInfo() []string {
	var info []string
	if s.notice != "" {
		info = append(info, s.notice)
	}
	if s.missing {
		info = append(info, "The angel has not written a log yet.")
	}
	if s.follow {
		info = append(info, "Following; new lines appear at the bottom.")
	} else {
		info = append(info, "Follow off; f or End returns to the live tail.")
	}
	if s.since != "" {
		info = append(info, "Showing the last "+s.since+".")
	}
	if s.list.Filter != "" {
		info = append(info, "Search: "+s.list.Filter)
	}
	return info
}

// ParityRefs reports what the screen offers: the actions of its card.
func (s *logsScreen) ParityRefs() []string {
	return parityRefs("logs", s.list, s.buildCard())
}

func (s *logsScreen) View(w, h int) string {
	s.width, s.height = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	if s.dialog != nil {
		return s.dialog.View(w, h)
	}
	return s.workspace.View(w, h)
}
