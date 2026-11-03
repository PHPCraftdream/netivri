package tui

import (
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const headerRows = 1
const tabsRows = 1
const toastRows = 1
const hintRows = 1
const bodyReservedRows = headerRows + tabsRows + toastRows + hintRows
const activityWidth = 16

// Header contains the service state shown in the frame banner.
type Header struct {
	Running bool
	Listen  string
	Uptime  time.Duration
	Version string
}
type HeaderSource interface{ Header() (Header, error) }
type Screen interface {
	Title() string
	Init() tea.Cmd
	Update(tea.Msg) (Screen, tea.Cmd)
	View(width, height int) string
	KeyHints() string
	CapturingInput() bool
}
type BreadcrumbScreen interface{ Breadcrumb() string }
type ScreenBindings interface{ Bindings() ActionHints }
type PaneFocusScreen interface {
	// FocusPane advances focus within the screen. Return false at the boundary
	// so Frame can move focus to the tab strip.
	FocusPane(reverse bool) bool
}
type SetPaneFocusScreen interface {
	// SetPaneFocus selects the initial pane when focus enters from the tab strip.
	SetPaneFocus(pane int)
}
type RegionFocusMsg struct {
	Tabs    bool
	Reverse bool
}
type SwitchTabMsg int
type ThemeMsg struct{ Theme Theme }
type switchTabMsg int
type ToastMsg string
type toastExpire struct{ generation uint64 }
type BackgroundMsg struct {
	Label  string
	Screen int
	Value  tea.Msg
}
type ModalResultMsg struct {
	Value    any
	Err      error
	Canceled bool
}
type Modal interface {
	Update(tea.Msg) (Modal, tea.Cmd)
	View(width, height int) string
	CapturingInput() bool
}
type ToastFunc func(string) tea.Cmd
type Env struct {
	Home       string
	Toast      ToastFunc
	OpenModal  func(Modal) tea.Cmd
	SwitchTab  func(int) tea.Cmd
	Background func(string, func() tea.Msg) tea.Cmd
	Now        func() time.Time
	Theme      Theme
}
type Tab struct {
	Title string
	New   func(Env) Screen
}
type Options struct {
	Tabs   []Tab
	Start  int
	Home   string
	Env    Env
	Header HeaderSource
	Now    func() time.Time
	Input  io.Reader
	Output io.Writer
}

type Frame struct {
	options              Options
	screens              []Screen
	initialized          []bool
	index, width, height int
	help                 bool
	modal                Modal
	modalScreen          int
	toast                string
	toastGeneration      uint64
	header               Header
	headerErr            error
	background           map[uint64]backgroundItem
	nextJob              uint64
	spinner              spinner.Model
	theme                Theme
	tabFocus             bool
}
type backgroundItem struct {
	label  string
	screen int
}
type headerTick struct{}
type headerResult struct {
	h   Header
	err error
}
type modalOpen struct {
	modal  Modal
	screen int
}
type backgroundStart struct {
	id     uint64
	label  string
	screen int
	work   func() tea.Msg
	tick   tea.Msg
}
type backgroundResult struct {
	id     uint64
	label  string
	screen int
	value  tea.Msg
}

func NewFrame(o Options) *Frame {
	if o.Now == nil {
		o.Now = time.Now
	}
	o.Env.Home = o.Home
	if o.Env.Now == nil {
		o.Env.Now = o.Now
	}
	theme := o.Env.Theme
	if theme.Accent == nil {
		theme = NewTheme()
	}
	f := &Frame{options: o, width: 80, height: 24, screens: make([]Screen, len(o.Tabs)), initialized: make([]bool, len(o.Tabs)), background: map[uint64]backgroundItem{}, spinner: spinner.New(spinner.WithSpinner(spinner.Line)), theme: theme}
	if len(o.Tabs) > 0 {
		f.index = o.Start
		if f.index < 0 || f.index >= len(o.Tabs) {
			f.index = 0
		}
		f.ensureScreen()
	}
	return f
}
func (f *Frame) envFor(index int) Env {
	e := f.options.Env
	e.Theme = f.theme
	e.Toast = func(s string) tea.Cmd { return func() tea.Msg { return ToastMsg(s) } }
	e.OpenModal = func(m Modal) tea.Cmd { return func() tea.Msg { return modalOpen{m, index} } }
	e.SwitchTab = func(tab int) tea.Cmd { return func() tea.Msg { return switchTabMsg(tab) } }
	e.Background = func(label string, work func() tea.Msg) tea.Cmd {
		f.nextJob++
		id := f.nextJob
		tick := f.spinner.Tick()
		return func() tea.Msg { return backgroundStart{id, label, index, work, tick} }
	}
	return e
}
func (f *Frame) ensureScreen() {
	if f.index >= 0 && f.index < len(f.screens) && f.screens[f.index] == nil && f.options.Tabs[f.index].New != nil {
		f.screens[f.index] = f.options.Tabs[f.index].New(f.envFor(f.index))
		if f.width > 0 && f.height > 0 {
			_, _ = f.screens[f.index].Update(tea.WindowSizeMsg{Width: f.width, Height: max(0, f.height-bodyReservedRows)})
			_, _ = f.screens[f.index].Update(ThemeMsg{Theme: f.theme})
		}
	}
}
func (f *Frame) initCurrent() tea.Cmd {
	if len(f.screens) == 0 {
		return nil
	}
	f.ensureScreen()
	if f.screens[f.index] != nil && !f.initialized[f.index] {
		f.initialized[f.index] = true
		return f.screens[f.index].Init()
	}
	return nil
}
func (f *Frame) Init() tea.Cmd { return tea.Batch(f.initCurrent(), f.pollHeader()) }
func (f *Frame) pollHeader() tea.Cmd {
	if f.options.Header == nil {
		return nil
	}
	return func() tea.Msg { h, e := f.options.Header.Header(); return headerResult{h, e} }
}
func (f *Frame) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		var cmds []tea.Cmd
		for _, cmd := range batch {
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return f, tea.Batch(cmds...)
	}
	if m, ok := msg.(backgroundResult); ok {
		delete(f.background, m.id)
		if m.value == nil {
			return f, nil
		}
		return f, f.routeScreen(m.screen, BackgroundMsg{m.label, m.screen, m.value})
	}
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		f.width = max(1, m.Width)
		f.height = max(1, m.Height)
		return f, forwardAll(f.screens, tea.WindowSizeMsg{Width: f.width, Height: max(0, f.height-bodyReservedRows)})
	case switchTabMsg:
		return f, f.switchToTab(int(m))
	case ThemeMsg:
		f.theme = m.Theme
		f.options.Env.Theme = f.theme
		if themed, ok := f.modal.(interface{ SetTheme(Theme) }); ok {
			themed.SetTheme(f.theme)
		}
		return f, forwardAll(f.screens, m)
	case tea.BackgroundColorMsg:
		f.theme = NewThemeFor(m.IsDark())
		f.options.Env.Theme = f.theme
		return f, forwardAll(f.screens, ThemeMsg{Theme: f.theme})
	case headerTick:
		return f, f.pollHeader()
	case headerResult:
		f.header, f.headerErr = m.h, m.err
		return f, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return headerTick{} })
	case ToastMsg:
		f.toast = string(m)
		f.toastGeneration++
		generation := f.toastGeneration
		return f, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return toastExpire{generation} })
	case toastExpire:
		if m.generation == f.toastGeneration {
			f.toast = ""
		}
		return f, nil
	case tea.MouseWheelMsg:
		if f.help {
			return f, nil
		}
		if m.Y < 2 || m.Y >= f.height-2 {
			return f, nil
		}
		translated := tea.MouseWheelMsg{X: m.X, Y: m.Y - 2, Button: m.Button, Mod: m.Mod}
		if f.modal != nil {
			var c tea.Cmd
			f.modal, c = f.modal.Update(translated)
			return f, c
		}
		return f, f.routeScreen(f.index, translated)
	case tea.MouseClickMsg:
		mouse := tea.Mouse(m)
		if mouse.Button != tea.MouseLeft {
			return f, nil
		}
		if mouse.Y == 1 {
			current := f.screenAt(f.index)
			if f.modal != nil || f.help || current != nil && current.CapturingInput() {
				return f, nil
			}
			column := 0
			for i, tab := range f.options.Tabs {
				width := lipgloss.Width(tab.Title) + 2
				if mouse.X >= column && mouse.X < column+width {
					f.index = i
					f.tabFocus = true
					return f, f.initCurrent()
				}
				column += width + 1
			}
			return f, nil
		}
		if mouse.Y >= 2 && mouse.Y < f.height-2 {
			if f.help {
				return f, nil
			}
			f.tabFocus = false
			translated := tea.MouseClickMsg{X: mouse.X, Y: mouse.Y - 2, Button: mouse.Button, Mod: mouse.Mod}
			if f.modal != nil {
				var c tea.Cmd
				f.modal, c = f.modal.Update(translated)
				return f, c
			}
			return f, f.routeScreen(f.index, translated)
		}
	case modalOpen:
		f.modal = m.modal
		f.modalScreen = m.screen
		return f, nil
	case backgroundStart:
		f.background[m.id] = backgroundItem{m.label, m.screen}
		return f, tea.Batch(backgroundWork(m.id, m.label, m.screen, m.work), func() tea.Msg { return m.tick })
	case spinner.TickMsg:
		var c tea.Cmd
		f.spinner, c = f.spinner.Update(m)
		if len(f.background) > 0 {
			return f, c
		}
		return f, nil
	case ModalResultMsg, ConfirmResultMsg, PromptResultMsg, WizardResultMsg:
		if f.modal != nil {
			i := f.modalScreen
			f.modal = nil
			return f, f.routeScreen(i, msg)
		}
	}
	if len(f.screens) == 0 {
		return f, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		key := k.String()
		if key == "ctrl+c" {
			return f, tea.Quit
		}
		if f.modal != nil {
			var c tea.Cmd
			f.modal, c = f.modal.Update(msg)
			return f, c
		}
		if f.help {
			if key == "esc" || bubbleskey.Matches(k, globalHelpBindings[1]) {
				f.help = false
			}
			if bubbleskey.Matches(k, globalHelpBindings[0]) {
				return f, tea.Quit
			}
			return f, nil
		}
		if f.width <= 60 || f.height <= 16 {
			return f, nil
		}
		if f.modal != nil {
			return f, nil
		}
		if f.help {
			return f, nil
		}
		if f.screens[f.index] != nil && f.screens[f.index].CapturingInput() {
			return f, f.routeScreen(f.index, msg)
		}
		if key == "up" && f.tabFocus {
			return f, nil
		}
		if key == "enter" && f.tabFocus {
			f.tabFocus = false
			if s, ok := f.screenAt(f.index).(SetPaneFocusScreen); ok {
				s.SetPaneFocus(0)
			}
			return f, f.routeScreen(f.index, RegionFocusMsg{Tabs: false})
		}
		if bubbleskey.Matches(k, globalHelpBindings[0]) {
			return f, tea.Quit
		}
		if bubbleskey.Matches(k, globalHelpBindings[1]) {
			f.help = true
			return f, nil
		}
		switch key {
		case "q":
			return f, tea.Quit
		case "?":
			f.help = true
			return f, nil
		case "right":
			return f, f.switchTab(1)
		case "left":
			return f, f.switchTab(-1)
		case "home":
			if f.tabFocus {
				return f, f.switchToTab(0)
			}
		case "end":
			if f.tabFocus {
				return f, f.switchToTab(len(f.screens) - 1)
			}
		case "tab":
			if f.tabFocus {
				f.tabFocus = false
				if s, ok := f.screenAt(f.index).(SetPaneFocusScreen); ok {
					s.SetPaneFocus(0)
				}
				return f, f.routeScreen(f.index, RegionFocusMsg{Reverse: false})
			}
			if s, ok := f.screenAt(f.index).(PaneFocusScreen); ok && s.FocusPane(false) {
				return f, nil
			}
			f.tabFocus = true
			return f, nil
		case "shift+tab":
			if f.tabFocus {
				f.tabFocus = false
				pane := PaneList
				if f.width >= 100 || f.width < 100 && f.screenAt(f.index) != nil && f.screenAt(f.index).CapturingInput() {
					pane = PaneCard
				}
				if s, ok := f.screenAt(f.index).(SetPaneFocusScreen); ok {
					s.SetPaneFocus(pane)
				}
				return f, f.routeScreen(f.index, RegionFocusMsg{Tabs: false, Reverse: true})
			}
			if s, ok := f.screenAt(f.index).(PaneFocusScreen); ok && s.FocusPane(true) {
				return f, nil
			}
			f.tabFocus = true
			return f, nil
		case "enter", "down":
			if f.tabFocus {
				f.tabFocus = false
				if s, ok := f.screenAt(f.index).(SetPaneFocusScreen); ok {
					s.SetPaneFocus(0)
				}
				return f, f.routeScreen(f.index, RegionFocusMsg{Tabs: false})
			}
		case "1", "2", "3":
			i := int(key[0] - '1')
			if i < len(f.screens) {
				f.index = i
				return f, f.initCurrent()
			}
		}
	}
	if f.modal != nil {
		var c tea.Cmd
		f.modal, c = f.modal.Update(msg)
		return f, c
	}
	return f, f.routeScreen(f.index, msg)
}
func forwardAll(screens []Screen, m tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for i, s := range screens {
		if s != nil {
			n, c := s.Update(m)
			screens[i] = n
			if c != nil {
				cmds = append(cmds, c)
			}
		}
	}
	return tea.Batch(cmds...)
}
func (f *Frame) switchTab(delta int) tea.Cmd {
	if len(f.screens) == 0 {
		return nil
	}
	return f.switchToTab((f.index + len(f.screens) + delta) % len(f.screens))
}
func (f *Frame) switchToTab(index int) tea.Cmd {
	if index < 0 || index >= len(f.screens) {
		return nil
	}
	f.index = index
	f.tabFocus = true
	return f.initCurrent()
}
func backgroundWork(id uint64, label string, screen int, work func() tea.Msg) tea.Cmd {
	return func() tea.Msg {
		var value tea.Msg
		if work != nil {
			value = work()
		}
		return backgroundResult{id, label, screen, value}
	}
}
func (f *Frame) routeScreen(i int, msg tea.Msg) tea.Cmd {
	if i < 0 || i >= len(f.screens) || f.screens[i] == nil {
		return nil
	}
	s, c := f.screens[i].Update(msg)
	f.screens[i] = s
	return c
}
func (f *Frame) View() tea.View {
	w, h := f.width, f.height
	if w <= 60 || h <= 16 {
		view := boundedView(strings.Join(centeredMessage(w, h, "Window too small - enlarge the window"), "\n"), w, h)
		view.AltScreen = true
		view.MouseMode = tea.MouseModeCellMotion
		return view
	}
	lines := make([]string, 0, h)
	header := f.headerLine(w)
	lines = append(lines, header)
	lines = append(lines, clipCells(f.tabLine(), w))
	bodyH := max(0, h-bodyReservedRows)
	var body string
	if f.help {
		body = f.helpBody(bodyH)
	} else if f.modal != nil {
		body = f.modal.View(w, bodyH)
	} else if s := f.screenAt(f.index); s != nil {
		body = s.View(w, bodyH)
	} else {
		body = "No screens"
	}
	bodyLines := strings.Split(body, "\n")
	for len(bodyLines) < bodyH {
		bodyLines = append(bodyLines, "")
	}
	if len(bodyLines) > bodyH {
		bodyLines = bodyLines[:bodyH]
	}
	for _, line := range bodyLines {
		lines = append(lines, clipCells(line, w))
	}
	activity := f.activitySlot()
	leftW := max(0, w-activityWidth)
	toast := clipCells(f.toast, leftW)
	toast = padCells(toast, leftW)
	lines = append(lines, toast+activity)
	lines = append(lines, clipCells(f.hintLine(), w))
	view := boundedView(strings.Join(lines, "\n"), w, h)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}
func (f *Frame) headerLine(w int) string {
	var s string
	if f.headerErr != nil {
		s = "Service status unavailable"
	} else if !f.header.Running && f.header.Version == "" && f.header.Listen == "" {
		s = "Service stopped"
	} else {
		s = "netivri  " + padCells(statusWord(f.header.Running), 7) + " " + padCells(sanitizeLine(f.header.Listen), 24) + "  up " + padCells(sanitizeLine(f.header.Uptime.Round(time.Second).String()), 12) + "  " + padCells(sanitizeLine(f.header.Version), 16)
	}
	if b, ok := f.screenAt(f.index).(BreadcrumbScreen); ok && b.Breadcrumb() != "" {
		s += "  " + b.Breadcrumb()
	}
	return clipCells(s, w)
}
func statusWord(r bool) string {
	if r {
		return "running"
	}
	return "stopped"
}
func (f *Frame) tabLine() string {
	var b strings.Builder
	for i, t := range f.options.Tabs {
		label := " " + sanitizeLine(t.Title) + " "
		if i == f.index {
			label = "[" + sanitizeLine(t.Title) + "]"
		}
		b.WriteString(label)
		if i < len(f.options.Tabs)-1 {
			b.WriteByte(' ')
		}
	}
	return b.String()
}
func (f *Frame) activitySlot() string {
	if len(f.background) == 0 {
		return padCells("", activityWidth)
	}
	n := len(f.background)
	label := " tasks"
	if n == 1 {
		label = " task"
	}
	return padCells(f.spinner.View()+" "+itoa(n)+label, activityWidth)
}
func (f *Frame) hintLine() string {
	bindings := append([]bubbleskey.Binding{}, globalShortBindings...)
	if s := f.screenAt(f.index); s != nil {
		if b, ok := s.(ScreenBindings); ok {
			bindings = append(bindings, b.Bindings()...)
			return themedHintLine(bindings, f.width, f.theme)
		}
		return themedHintLine(legacyHintBindings(s.KeyHints()), f.width, f.theme)
	}
	return themedHintLine(bindings, f.width, f.theme)
}
func (f *Frame) helpBody(height int) string {
	sections := [][]bubbleskey.Binding{append([]bubbleskey.Binding{}, globalHelpBindings...)}
	screen := f.screenAt(f.index)
	if bindings, ok := screen.(ScreenBindings); ok {
		sections = append(sections, bindings.Bindings().FullHelp()...)
	} else if screen != nil {
		sections = append(sections, ActionHints(legacyHintBindings(screen.KeyHints())).FullHelp()...)
	}
	model := help.New()
	model.SetWidth(f.width)
	model.Styles = themedHelpStyles(f.theme)
	text := model.FullHelpView(sections)
	lines := strings.Split(text, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
func (f *Frame) screenAt(i int) Screen {
	if i < 0 || i >= len(f.screens) {
		return nil
	}
	return f.screens[i]
}
func boundedView(s string, w, h int) tea.View {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > w {
			lines[i] = clipCells(line, w)
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
func clipCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, NewTheme().Ellipsis())
}

func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
func padCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = clipCells(s, width)
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}
func centeredMessage(w, h int, message string) []string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", max(0, (w-lipgloss.Width(message))/2))
	}
	if h > 0 {
		row := h / 2
		lines[row] = strings.Repeat(" ", max(0, (w-lipgloss.Width(message))/2)) + clipCells(message, w)
	}
	return lines
}
func Start(o Options) error {
	opts := []tea.ProgramOption{tea.WithInput(o.Input), tea.WithOutput(o.Output)}
	if os.Getenv("NO_COLOR") != "" {
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	_, err := tea.NewProgram(NewFrame(o), opts...).Run()
	return err
}
func IO(tabs []Tab, env Env, header HeaderSource) Options {
	return Options{Tabs: tabs, Env: env, Header: header, Input: os.Stdin, Output: os.Stdout}
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
