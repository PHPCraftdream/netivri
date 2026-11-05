package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Stats screen: the table of `netivri stats` (per node or per account)
// with its filters, read-only, through the same StatsRead operation the
// command runs (docs/design/parity.md).

type statsReadMsg struct {
	res StatsResult
	err error
}

type statsScreen struct {
	home          string
	env           tui.Env
	theme         tui.Theme
	workspace     *tui.Workspace
	list          *tui.List
	width, height int

	by                               string // "node" or "account"
	since                            time.Duration
	sub, country, nodeLabel, account string // filters; empty = no filter

	items      []map[string]any
	selected   string // key of the selected row
	loaded     bool
	busy       string
	errText    string
	dialog     tui.Modal
	prompt     *tui.Prompt
	promptMode string // "since", "sub", "country", "node" or "account"

	read func(string, StatsOptions) (StatsResult, error)
}

func newStatsScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	l := tui.NewList(statsColumns("node"), nil)
	l.Title = "Stats"
	l.AddLabel = ""
	l.EmptyText = "No statistics yet"
	theme := env.Theme
	if theme.Accent == nil {
		theme = tui.NewTheme()
	}
	s := &statsScreen{home: home, env: env, theme: theme, list: l,
		by: "node", since: defaultStatsWindow, read: StatsRead}
	s.workspace = tui.NewWorkspace(l, s.buildCard(""))
	s.workspace.SetTheme(theme)
	return s
}

func (s *statsScreen) Title() string        { return "Stats" }
func (s *statsScreen) CapturingInput() bool { return s.dialog != nil }
func (s *statsScreen) Breadcrumb() string {
	if s.width < 100 && s.workspace.Drilled {
		return "Stats / " + s.selectedName()
	}
	return ""
}
func (s *statsScreen) FocusPane(reverse bool) bool {
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
func (s *statsScreen) SetPaneFocus(p int) {
	if s.width >= 100 {
		s.workspace.SetFocus(p)
	}
}
func (s *statsScreen) Bindings() tui.ActionHints {
	b := append(tui.ActionHints{}, s.list.Bindings()...)
	b = append(b, s.workspace.Card.Bindings()...)
	return append(b,
		key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "group by")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")))
}
func (s *statsScreen) KeyHints() string {
	if s.dialog != nil {
		return "Prompt: enter saves · esc cancels"
	}
	return "↑/↓ select  Enter open  b group by  r reload"
}

func (s *statsScreen) bg(label string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, w)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: w()} }
}

func (s *statsScreen) toast(text string) tea.Cmd {
	if text == "" || s.env.Toast == nil {
		return nil
	}
	return s.env.Toast(text)
}

func (s *statsScreen) Init() tea.Cmd { return s.reload() }

func (s *statsScreen) options() StatsOptions {
	return StatsOptions{Window: s.since, By: s.by, Sub: s.sub,
		Country: s.country, Node: s.nodeLabel, Account: s.account}
}

// reload re-reads the statistics in the background.
func (s *statsScreen) reload() tea.Cmd {
	s.busy = "Loading statistics"
	opts, read := s.options(), s.read
	return s.bg(s.busy, func() tea.Msg {
		res, err := read(s.home, opts)
		return statsReadMsg{res: res, err: err}
	})
}

// toggleBy switches the grouping between node and account and re-reads.
func (s *statsScreen) toggleBy() tea.Cmd {
	if s.by == "node" {
		s.by = "account"
	} else {
		s.by = "node"
	}
	s.selected = ""
	s.items = nil
	s.refresh()
	return s.reload()
}

// statsErrorText shows errors by message and hint only; a foreign error is
// never echoed (it can name addresses or nodes).
func statsErrorText(err error) string {
	if ce, ok := err.(*cli.Error); ok {
		if ce.Hint != "" {
			return ce.Message + " (" + ce.Hint + ")"
		}
		return ce.Message
	}
	return "statistics operation failed"
}

func (s *statsScreen) applyRead(x statsReadMsg) (tui.Screen, tea.Cmd) {
	s.busy = ""
	s.loaded = true
	s.errText = ""
	s.items = nil
	if x.err != nil {
		if ce, ok := x.err.(*cli.Error); !ok || ce.Code != "STATS_UNAVAILABLE" {
			// Without a database the empty state explains everything;
			// anything else is shown by message and hint only.
			s.errText = statsErrorText(x.err)
		}
	} else {
		s.items = x.res.Items
	}
	s.refresh()
	if s.errText != "" {
		return s, s.toast(s.errText)
	}
	return s, nil
}

func (s *statsScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
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
	case statsReadMsg:
		return s.applyRead(x)
	}
	if x, ok := m.(tui.PromptResultMsg); ok {
		return s.applyPrompt(x)
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		return s, c
	}
	if k, ok := m.(tea.KeyPressMsg); ok && !s.list.Filtering {
		switch k.String() {
		case "b":
			return s, s.toggleBy()
		case "r":
			return s, s.reload()
		}
	}
	activated := s.workspace.Update(m)
	if row, ok := s.list.SelectedRow(); ok {
		if name, _ := row.Data.(string); name != "" && name != s.selected {
			s.selected = name
			s.refresh()
		}
	}
	if activated && s.workspace.Focus() == tui.PaneCard {
		return s.activateCard()
	}
	return s, nil
}

func (s *statsScreen) activateCard() (tui.Screen, tea.Cmd) {
	card := s.workspace.Card
	if card == nil || card.Selected < 0 || card.Selected >= len(card.Lines) {
		return s, nil
	}
	switch id := card.Lines[card.Selected].ID; id {
	case "by":
		return s, s.toggleBy()
	case "reload":
		return s, s.reload()
	case "since", "sub", "country", "node", "account":
		s.openPrompt(id)
	}
	return s, nil
}

// openPrompt opens the one-line prompt of a filter or of the window; the
// current value is pre-filled and an empty answer clears the filter.
func (s *statsScreen) openPrompt(field string) {
	title := map[string]string{"since": "Stats — window", "sub": "Stats — subscription",
		"country": "Stats — country", "node": "Stats — node", "account": "Stats — account"}[field]
	label := map[string]string{"since": "Since", "sub": "Subscription", "country": "Country",
		"node": "Node", "account": "Account"}[field]
	hint := map[string]string{
		"since":   "A duration like 90m, 24h or 7d; empty resets it to 24h",
		"sub":     "Only the nodes of this subscription; empty shows all",
		"country": "Two letters, as in DE; empty shows all",
		"node":    "A label as in 'netivri node list' (sub/id); empty shows all",
		"account": "Only this login; empty shows all",
	}[field]
	value := map[string]string{"since": statsDurationInput(s.since), "sub": s.sub,
		"country": s.country, "node": s.nodeLabel, "account": s.account}[field]
	s.prompt = tui.NewPrompt(tui.TextPrompt, title, label, hint, value)
	s.prompt.SetTheme(s.theme)
	s.dialog = s.prompt
	s.promptMode = field
}

func (s *statsScreen) closePrompt() {
	s.dialog, s.prompt, s.promptMode = nil, nil, ""
}

// applyPrompt applies a finished prompt; invalid input keeps the prompt open
// with the error under the input.
func (s *statsScreen) applyPrompt(x tui.PromptResultMsg) (tui.Screen, tea.Cmd) {
	if x.Canceled {
		s.closePrompt()
		return s, nil
	}
	switch s.promptMode {
	case "since":
		v := strings.TrimSpace(x.Value)
		if v == "" {
			s.since = defaultStatsWindow
			break
		}
		d, err := parseWindow(v)
		if err != nil {
			s.prompt.SetError(errors.New("use a duration like 90m, 24h or 7d, at most 365d"))
			return s, nil
		}
		s.since = d
	case "sub":
		s.sub = strings.TrimSpace(x.Value)
	case "country":
		v := strings.TrimSpace(x.Value)
		if v == "" {
			s.country = ""
			break
		}
		cc, ok := config.NormalizeCountry(v)
		if !ok {
			s.prompt.SetError(errors.New("use two letters, as in DE or NL"))
			return s, nil
		}
		s.country = cc
	case "node":
		s.nodeLabel = strings.TrimSpace(x.Value)
	case "account":
		s.account = strings.TrimSpace(x.Value)
	}
	s.closePrompt()
	return s, s.reload()
}

// refresh rebuilds the rows and the card from the current data, keeping the
// selected line and the scroll position of the card. The cards share their
// control ids, so the selection follows the id even when the selected row
// (and the title) changes.
func (s *statsScreen) refresh() {
	s.syncRows()
	if s.selected == "" {
		if row, ok := s.list.SelectedRow(); ok {
			s.selected, _ = row.Data.(string)
		}
	}
	old := s.workspace.Card
	card := s.buildCard(s.selected)
	card.SetTheme(s.theme)
	if old != nil {
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

func (s *statsScreen) syncRows() {
	s.list.Columns = statsColumns(s.by)
	rows := make([]tui.Row, 0, len(s.items))
	for _, item := range s.items {
		rows = append(rows, tui.Row{Cells: statsCells(item, s.by), Data: statsRowKey(item, s.by)})
	}
	s.list.SetRows(rows)
	if s.selected != "" && !s.list.SelectData(s.selected) {
		s.selected = ""
	}
}

func (s *statsScreen) selectedName() string {
	if r, ok := s.list.SelectedRow(); ok {
		name, _ := r.Data.(string)
		return name
	}
	return ""
}

func (s *statsScreen) item(key string) map[string]any {
	for _, row := range s.items {
		if statsRowKey(row, s.by) == key {
			return row
		}
	}
	return nil
}

// buildCard renders the card of a row: its figures (read-only) and the
// controls. Without a key only the controls remain.
func (s *statsScreen) buildCard(key string) *tui.Card {
	var lines []tui.CardLine
	title := "Statistics"
	if row := s.item(key); row != nil {
		title = statsCardTitle(s.by, key)
		lines = append(lines, s.figureLines(row)...)
	}
	lines = append(lines, s.controlLines()...)
	c := tui.NewCard(title, lines)
	c.Info = s.cardInfo()
	if s.busy != "" {
		c.Info = append(c.Info, s.busy+"…")
	}
	return c
}

func statsCardTitle(by, key string) string {
	switch by {
	case "account":
		return "Account " + key
	case "country":
		return "Country " + key
	case "provider":
		return "Provider " + key
	}
	return "Node " + key
}

func (s *statsScreen) controlLines() []tui.CardLine {
	dash := func(v string) string {
		if v == "" {
			return "—"
		}
		return v
	}
	return []tui.CardLine{
		{ID: "since", Label: "Since", Value: statsDurationInput(s.since), Kind: tui.CardField},
		{ID: "sub", Label: "Subscription", Value: dash(s.sub), Kind: tui.CardField},
		{ID: "country", Label: "Country", Value: dash(s.country), Kind: tui.CardField},
		{ID: "node", Label: "Node", Value: dash(s.nodeLabel), Kind: tui.CardField},
		{ID: "account", Label: "Account", Value: dash(s.account), Kind: tui.CardField},
		{ID: "by", Label: "Group by", Value: s.by, Hotkey: "b", Kind: tui.CardAction, Action: true},
		{ID: "reload", Label: "Reload", Hotkey: "r", Kind: tui.CardAction, Action: true},
	}
}

func (s *statsScreen) cardInfo() []string {
	var info []string
	if s.errText != "" {
		info = append(info, s.errText)
	}
	if len(s.items) == 0 {
		info = append(info,
			"Statistics appear after the angel has run for a while.",
			"They are stored in "+statsPath(s.home)+".")
	} else {
		info = append(info, "Read-only, as in `netivri stats`.")
	}
	return info
}

// figureLines renders every figure of a row, formatted like the command.
func (s *statsScreen) figureLines(item map[string]any) []tui.CardLine {
	var lines []tui.CardLine
	add := func(id, label, value string) {
		lines = append(lines, tui.CardLine{ID: id, Label: label, Value: value, Kind: tui.CardReadOnly})
	}
	if s.by == "account" {
		add("login", "Login", statsString(item, "login"))
		add("dials", "Dials", statsCount(item, "dials"))
		add("dial-ok-pct", "Dial OK %", statsPct(item["dial_ok_pct"]))
		add("nodes", "Nodes", statsCount(item, "nodes"))
		add("bytes-up", "Bytes up", statsBytes(item, "bytes_up"))
		add("bytes-down", "Bytes down", statsBytes(item, "bytes_down"))
		add("countries", "Countries", statsString(item, "countries"))
		add("providers", "Providers", statsString(item, "providers"))
		return lines
	}
	add("label", "Node", statsString(item, "label"))
	if v := statsString(item, "name"); v != "" {
		add("name", "Name", v)
	}
	add("provider", "Provider", statsString(item, "provider"))
	add("country", "Country", statsString(item, "country"))
	add("probes", "Probes", statsCount(item, "probes"))
	add("uptime-pct", "Uptime %", statsPct(item["uptime_pct"]))
	if d, ok := item["delay_ms"].(map[string]any); ok {
		add("delay-avg", "Delay avg", statsMS(statsNumOr(d, "avg")))
		add("delay-p50", "Delay p50", statsMS(statsNumOr(d, "p50")))
		add("delay-p95", "Delay p95", statsMS(statsNumOr(d, "p95")))
	}
	add("dials", "Dials", statsCount(item, "dials"))
	add("dial-ok-pct", "Dial OK %", statsPct(item["dial_ok_pct"]))
	add("dial-avg", "Dial avg", statsMS(statsNumOr(item, "dial_ms_avg")))
	if tp, ok := item["throughput_kbps"].(map[string]any); ok {
		add("throughput-p50", "Throughput p50", statsKBps(statsNumOr(tp, "p50")))
		add("throughput-max", "Throughput max", statsKBps(statsNumOr(tp, "max")))
	}
	add("bytes", "Bytes", statsBytes(item, "bytes"))
	add("last-seen", "Last seen", statsString(item, "last_seen"))
	add("last-ok", "Last ok", statsString(item, "last_ok"))
	return lines
}

// ParityRefs reports what the screen offers: the list and the controls of its
// card (docs/design/parity.md).
func (s *statsScreen) ParityRefs() []string {
	return parityRefs("stats", s.list, s.workspace.Card)
}

func (s *statsScreen) View(w, h int) string {
	s.width, s.height = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	if s.dialog != nil {
		return s.dialog.View(w, h)
	}
	return s.workspace.View(w, h)
}

// statsColumns is the layout of a grouping, with the columns the command
// prints.
func statsColumns(by string) []tui.ListColumn {
	switch by {
	case "account":
		return []tui.ListColumn{
			{Title: "Login", Width: 12, Flex: 1},
			{Title: "Dials", Width: 6},
			{Title: "OK %", Width: 6},
			{Title: "Nodes", Width: 6},
			{Title: "Down", Width: 8},
			{Title: "Up", Width: 8},
		}
	}
	return []tui.ListColumn{
		{Title: "Node", Width: 12, Flex: 1},
		{Title: "Name", Width: 10, Flex: 1},
		{Title: "Probes", Width: 6},
		{Title: "OK %", Width: 6},
		{Title: "Delay", Width: 9},
		{Title: "Bytes", Width: 8},
	}
}

// statsRowKey is the identity of a row: the node label, the login or the
// group key.
func statsRowKey(item map[string]any, by string) string {
	if by == "account" {
		return statsString(item, "login")
	}
	if by == "node" {
		return statsString(item, "label")
	}
	return statsString(item, by)
}

// statsCells renders a list row with the columns of the command.
func statsCells(item map[string]any, by string) []string {
	if by == "account" {
		return []string{
			statsString(item, "login"),
			statsCount(item, "dials"),
			statsPct(item["dial_ok_pct"]),
			statsCount(item, "nodes"),
			statsBytes(item, "bytes_down"),
			statsBytes(item, "bytes_up"),
		}
	}
	name := ""
	if by == "node" {
		name = statsString(item, "name")
	}
	delay := "—"
	if item["uptime_pct"] != nil {
		if d, ok := item["delay_ms"].(map[string]any); ok {
			delay = statsWhole(statsNumOr(d, "p50")) + "/" + statsWhole(statsNumOr(d, "p95"))
		}
	}
	return []string{
		statsString(item, "label"),
		name,
		statsCount(item, "probes"),
		statsPct(item["uptime_pct"]),
		delay,
		statsBytes(item, "bytes"),
	}
}

// statsDurationInput renders the window the way `stats --since` reads it.
func statsDurationInput(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	}
	return d.String()
}

// The items of StatsRead are JSON-shaped maps; the accessors below read them
// whatever number type the value carries.

func statsNum(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func statsNumOr(item map[string]any, key string) float64 {
	v, _ := statsNum(item[key])
	return v
}

// statsWhole formats a number like the command's JSON: whole numbers without
// decimals, fractional ones with their decimals.
func statsWhole(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// statsPct formats a percentage like the command (nil = never measured).
func statsPct(v any) string {
	f, ok := statsNum(v)
	if !ok {
		return "—"
	}
	return statsWhole(f) + "%"
}

func statsMS(f float64) string   { return statsWhole(f) + " ms" }
func statsKBps(f float64) string { return statsWhole(f) + " KiB/s" }

func statsCount(item map[string]any, key string) string { return statsWhole(statsNumOr(item, key)) }

func statsBytes(item map[string]any, key string) string {
	return humanBytes(int64(statsNumOr(item, key)))
}

func statsString(item map[string]any, key string) string {
	if v, ok := item[key].(string); ok {
		return v
	}
	return ""
}
