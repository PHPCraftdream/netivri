package main

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Service screen: the angel (state, start, stop, restart), the checks of
// `netivri doctor` and the live activity of the accounts, i.e. what the
// service commands (up, down, status, doctor) do, through the same operations.

type serviceStatusMsg struct {
	status angel.Status
	err    error
}
type serviceChecksMsg struct{ checks []doctorCheck }
type serviceActionMsg struct {
	kind string
	err  error
}
type serviceTick struct{}

type serviceScreen struct {
	home          string
	env           tui.Env
	theme         tui.Theme
	workspace     *tui.Workspace
	list          *tui.List
	width, height int

	status  angel.Status
	running bool
	probed  bool
	checks  []doctorCheck
	ran     bool

	selected string
	busy     string
	mode     string
	dialog   tui.Modal
	confirm  *tui.Confirm

	probe  func(string) (angel.Status, error)
	start  func(string) error
	stop   func(string) error
	doctor func(string) []doctorCheck
	wait   func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
}

func newServiceScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	l := tui.NewList([]tui.ListColumn{{Title: "Name", Width: 12, Flex: 1}, {Title: "State", Width: 14}}, nil)
	l.Title = "Service"
	l.AddLabel = ""
	theme := env.Theme
	if theme.Accent == nil {
		theme = tui.NewTheme()
	}
	s := &serviceScreen{home: home, env: env, theme: theme, list: l, selected: "angel",
		probe: ServiceProbe, start: ServiceStart, stop: ServiceStop, doctor: ServiceChecks}
	s.workspace = tui.NewWorkspace(l, s.buildCard("angel"))
	s.workspace.SetTheme(theme)
	s.syncRows()
	return s
}

func (s *serviceScreen) Title() string        { return "Service" }
func (s *serviceScreen) CapturingInput() bool { return s.dialog != nil }
func (s *serviceScreen) Breadcrumb() string {
	if s.width < 100 && s.workspace.Drilled {
		return "Service / " + s.selected
	}
	return ""
}
func (s *serviceScreen) FocusPane(reverse bool) bool {
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
func (s *serviceScreen) SetPaneFocus(p int) {
	if s.width >= 100 {
		s.workspace.SetFocus(p)
	}
}
func (s *serviceScreen) Bindings() tui.ActionHints {
	b := append(tui.ActionHints{}, s.list.Bindings()...)
	b = append(b, s.workspace.Card.Bindings()...)
	return append(b,
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "restart")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "run checks")))
}
func (s *serviceScreen) KeyHints() string {
	if s.dialog != nil {
		return "Dialog: confirm or cancel"
	}
	return "↑/↓ select  Enter open  s start  x stop  t restart  c run checks"
}

func (s *serviceScreen) bg(label string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, w)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: w()} }
}

func (s *serviceScreen) probeCmd() tea.Cmd {
	return s.bg("Reading the service state", func() tea.Msg {
		st, err := s.probe(s.home)
		return serviceStatusMsg{status: st, err: err}
	})
}
func (s *serviceScreen) tickCmd() tea.Cmd {
	wait := s.wait
	if wait == nil {
		wait = tea.Tick
	}
	timer := wait(3*time.Second, func(time.Time) tea.Msg { return serviceTick{} })
	if timer == nil {
		return nil
	}
	return s.bg("Service refresh timer", func() tea.Msg { return timer() })
}
func (s *serviceScreen) Init() tea.Cmd { return tea.Batch(s.probeCmd(), s.tickCmd()) }

func (s *serviceScreen) toast(text string) tea.Cmd {
	if text == "" || s.env.Toast == nil {
		return nil
	}
	return s.env.Toast(text)
}

func serviceErrorText(err error) string {
	if ce, ok := err.(*cli.Error); ok {
		if ce.Hint != "" {
			return ce.Message + " (" + ce.Hint + ")"
		}
		return ce.Message
	}
	return "service operation failed"
}

func (s *serviceScreen) openConnections() int {
	n := 0
	for _, a := range s.status.Activity {
		n += a.Connections
	}
	return n
}

func (s *serviceScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
	switch x := m.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = x.Width, x.Height
		s.workspace.SetSize(x.Width, x.Height)
		return s, nil
	case tui.ThemeMsg:
		s.theme = x.Theme
		s.workspace.SetTheme(x.Theme)
		if s.confirm != nil {
			s.confirm.SetTheme(x.Theme)
		}
		return s, nil
	case tui.BackgroundMsg:
		return s.Update(x.Value)
	case serviceTick:
		return s, tea.Batch(s.probeCmd(), s.tickCmd())
	case serviceStatusMsg:
		s.status, s.running, s.probed = x.status, x.err == nil, true
		if x.err != nil {
			s.status = angel.Status{}
		}
		s.refresh()
		return s, nil
	case serviceChecksMsg:
		s.busy = ""
		s.checks, s.ran = x.checks, true
		s.refresh()
		return s, s.toast(serviceChecksSummary(x.checks))
	case serviceActionMsg:
		s.busy = ""
		cmds := []tea.Cmd{s.probeCmd()}
		if x.err != nil {
			cmds = append(cmds, s.toast(serviceErrorText(x.err)))
		} else {
			cmds = append(cmds, s.toast(map[string]string{"start": "Angel started", "stop": "Angel stopped", "restart": "Angel restarted"}[x.kind]))
		}
		return s, tea.Batch(cmds...)
	case tui.ConfirmResultMsg:
		mode := s.mode
		s.dialog, s.confirm, s.mode = nil, nil, ""
		if !x.Confirmed || x.Canceled {
			return s, nil
		}
		switch mode {
		case "confirm-stop":
			return s, s.runStop(false)
		case "confirm-restart":
			return s, s.runStop(true)
		}
		return s, nil
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		return s, c
	}
	if k, ok := m.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "s":
			return s, s.requestStart()
		case "x":
			return s, s.requestStop(false)
		case "t":
			return s, s.requestStop(true)
		case "c":
			return s, s.runChecks()
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

func (s *serviceScreen) activateCard() (tui.Screen, tea.Cmd) {
	card := s.workspace.Card
	if card == nil || card.Selected < 0 || card.Selected >= len(card.Lines) {
		return s, nil
	}
	switch card.Lines[card.Selected].ID {
	case "start":
		return s, s.requestStart()
	case "stop":
		return s, s.requestStop(false)
	case "restart":
		return s, s.requestStop(true)
	case "checks":
		return s, s.runChecks()
	}
	return s, nil
}

func (s *serviceScreen) requestStart() tea.Cmd {
	if s.running {
		return s.toast("The angel is already running")
	}
	s.busy = "Starting the angel"
	return s.bg(s.busy, func() tea.Msg { return serviceActionMsg{kind: "start", err: s.start(s.home)} })
}

func (s *serviceScreen) requestStop(restart bool) tea.Cmd {
	if !s.running {
		if restart {
			return s.requestStart()
		}
		return s.toast("The angel is not running")
	}
	verb, mode := "Stop", "confirm-stop"
	if restart {
		verb, mode = "Restart", "confirm-restart"
	}
	text := fmt.Sprintf("%s the angel?", verb)
	if n := s.openConnections(); n > 0 {
		text += fmt.Sprintf(" %d open connection%s will be dropped.", n, map[bool]string{true: "", false: "s"}[n == 1])
	}
	s.confirm = &tui.Confirm{Prompt: text, ConfirmLabel: verb, Theme: s.theme}
	s.dialog, s.mode = s.confirm, mode
	return nil
}

func (s *serviceScreen) runStop(restart bool) tea.Cmd {
	kind := "stop"
	if restart {
		kind = "restart"
	}
	s.busy = map[bool]string{true: "Restarting the angel", false: "Stopping the angel"}[restart]
	return s.bg(s.busy, func() tea.Msg {
		if err := s.stop(s.home); err != nil {
			return serviceActionMsg{kind: kind, err: err}
		}
		if restart {
			return serviceActionMsg{kind: kind, err: s.start(s.home)}
		}
		return serviceActionMsg{kind: kind}
	})
}

func (s *serviceScreen) runChecks() tea.Cmd {
	s.busy = "Running the checks"
	return s.bg(s.busy, func() tea.Msg { return serviceChecksMsg{checks: s.doctor(s.home)} })
}

func serviceChecksSummary(checks []doctorCheck) string {
	ok, warn, fail := serviceCounts(checks)
	return fmt.Sprintf("Checks: %d ok, %d warn, %d failed", ok, warn, fail)
}

func serviceCounts(checks []doctorCheck) (ok, warn, fail int) {
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			fail++
		case doctorWarn:
			warn++
		default:
			ok++
		}
	}
	return
}

// refresh rebuilds the rows and the card of the selected row from the current
// data, keeping the selected line and the scroll position of the card.
func (s *serviceScreen) refresh() {
	s.syncRows()
	old := s.workspace.Card
	card := s.buildCard(s.selected)
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

func (s *serviceScreen) syncRows() {
	angelState := "stopped"
	if !s.probed {
		angelState = "…"
	} else if s.running {
		angelState = "running"
	}
	checks := "not run"
	if s.ran {
		ok, warn, fail := serviceCounts(s.checks)
		checks = fmt.Sprintf("%d ok", ok)
		if warn > 0 {
			checks += fmt.Sprintf(", %d warn", warn)
		}
		if fail > 0 {
			checks += fmt.Sprintf(", %d failed", fail)
		}
	}
	accounts := "—"
	if s.running {
		active := 0
		for _, a := range s.status.Activity {
			if a.Active {
				active++
			}
		}
		accounts = fmt.Sprintf("%d active", active)
	}
	rows := []tui.Row{
		{Cells: []string{"Angel", angelState}, Data: "angel"},
		{Cells: []string{"Checks", checks}, Data: "checks"},
		{Cells: []string{"Accounts", accounts}, Data: "accounts"},
	}
	s.list.SetRows(rows)
	s.list.SelectData(s.selected)
}

func (s *serviceScreen) uptime() string {
	started, err := time.Parse(time.RFC3339, s.status.StartedAt)
	if err != nil {
		return "—"
	}
	now := time.Now()
	if s.env.Now != nil {
		now = s.env.Now()
	}
	d := now.Sub(started)
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

func (s *serviceScreen) buildCard(kind string) *tui.Card {
	switch kind {
	case "checks":
		return s.checksCard()
	case "accounts":
		return s.accountsCard()
	}
	return s.angelCard()
}

func (s *serviceScreen) angelCard() *tui.Card {
	state, listen, process, up, version := "stopped", "—", "—", "—", "—"
	if s.running {
		state, listen, process, up, version = "running", s.status.Listen, fmt.Sprint(s.status.PID), s.uptime(), s.status.Version
	}
	if !s.probed {
		state = "checking…"
	}
	c := tui.NewCard("Angel", []tui.CardLine{
		{ID: "state", Label: "State", Value: state, Kind: tui.CardReadOnly},
		{ID: "listen", Label: "Listen", Value: listen, Kind: tui.CardReadOnly},
		{ID: "process", Label: "Process", Value: process, Kind: tui.CardReadOnly},
		{ID: "uptime", Label: "Uptime", Value: up, Kind: tui.CardReadOnly},
		{ID: "version", Label: "Version", Value: version, Kind: tui.CardReadOnly},
		{ID: "start", Label: "Start", Hotkey: "s", Kind: tui.CardAction, Action: true, Disabled: s.running},
		{ID: "stop", Label: "Stop...", Hotkey: "x", Kind: tui.CardAction, Action: true, Disabled: !s.running},
		{ID: "restart", Label: "Restart...", Hotkey: "t", Kind: tui.CardAction, Action: true, Disabled: !s.running},
	})
	if s.busy != "" {
		c.Info = []string{s.busy + "…"}
	}
	return c
}

func (s *serviceScreen) checksCard() *tui.Card {
	lines := []tui.CardLine{{ID: "checks", Label: "Run checks", Hotkey: "c", Kind: tui.CardAction, Action: true}}
	for i, ch := range s.checks {
		lines = append(lines, tui.CardLine{ID: fmt.Sprintf("check-%d", i), Label: ch.Name, Value: ch.Status, Kind: tui.CardReadOnly})
		if ch.Detail != "" && ch.Status != doctorOK {
			lines = append(lines, tui.CardLine{ID: fmt.Sprintf("detail-%d", i), Label: "", Value: ch.Detail, Kind: tui.CardReadOnly})
		}
	}
	c := tui.NewCard("Checks", lines)
	switch {
	case s.busy == "Running the checks":
		c.Info = []string{"Running the checks…"}
	case !s.ran:
		c.Info = []string{"Not run yet. The same checks as `netivri doctor`."}
	default:
		c.Info = []string{serviceChecksSummary(s.checks)}
	}
	return c
}

func (s *serviceScreen) accountsCard() *tui.Card {
	var lines []tui.CardLine
	for _, a := range s.status.Activity {
		state := "idle"
		if a.Active {
			state = fmt.Sprintf("%d conn · ↓%s ↑%s", a.Connections, humanBytes(a.DownloadBytes), humanBytes(a.UploadBytes))
		}
		lines = append(lines, tui.CardLine{ID: "account-" + a.Login, Label: a.Login, Value: state, Kind: tui.CardReadOnly})
	}
	c := tui.NewCard("Accounts", lines)
	switch {
	case !s.running:
		c.Info = []string{"The angel is not running."}
	case len(lines) == 0:
		c.Info = []string{"No accounts yet."}
	default:
		c.Info = []string{"Live activity, as in `netivri status`."}
	}
	return c
}

// ParityRefs reports what the screen offers: the actions of every card.
func (s *serviceScreen) ParityRefs() []string {
	var refs []string
	seen := map[string]bool{}
	for _, kind := range []string{"angel", "checks", "accounts"} {
		for _, r := range parityRefs("service", s.list, s.buildCard(kind)) {
			if !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
	}
	return refs
}

func (s *serviceScreen) View(w, h int) string {
	s.width, s.height = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	if s.dialog != nil {
		return s.dialog.View(w, h)
	}
	return s.workspace.View(w, h)
}
