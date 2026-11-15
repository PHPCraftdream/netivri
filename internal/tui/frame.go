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
		return f, batchCmds(batch)
	}
	if m, ok := msg.(backgroundResult); ok {
		return f, f.handleBackgroundResult(m)
	}
	if cmd, ok := f.updateFrameMsg(msg); ok {
		return f, cmd
	}
	if len(f.screens) == 0 {
		return f, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, ok := f.handleKey(k, msg); ok {
			return f, cmd
		}
	}
	return f, f.routeToModalOrScreen(msg)
}
func batchCmds(batch tea.BatchMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, cmd := range batch {
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}
func (f *Frame) handleBackgroundResult(m backgroundResult) tea.Cmd {
	delete(f.background, m.id)
	if m.value == nil {
		return nil
	}
	return f.routeScreen(m.screen, BackgroundMsg{m.label, m.screen, m.value})
}

// updateFrameMsg handles frame-level messages; ok=false lets the message fall through to key handling and screen routing.
func (f *Frame) updateFrameMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		return f.resize(m), true
	case switchTabMsg:
		return f.switchToTab(int(m)), true
	case ThemeMsg:
		return f.handleThemeMsg(m), true
	case tea.BackgroundColorMsg:
		return f.handleBackgroundColor(m), true
	case headerTick:
		return f.pollHeader(), true
	case headerResult:
		return f.handleHeaderResult(m), true
	case ToastMsg:
		return f.showToast(m), true
	case toastExpire:
		f.expireToast(m)
		return nil, true
	case tea.MouseWheelMsg:
		return f.handleMouseWheel(m), true
	case tea.MouseClickMsg:
		return f.handleMouseClick(m)
	case modalOpen:
		f.openModal(m)
		return nil, true
	case backgroundStart:
		return f.startBackground(m), true
	case spinner.TickMsg:
		return f.tickSpinner(m), true
	case ModalResultMsg, ConfirmResultMsg, PromptResultMsg, WizardResultMsg:
		return f.handleModalResult(msg)
	}
	return nil, false
}
func (f *Frame) resize(m tea.WindowSizeMsg) tea.Cmd {
	f.width = max(1, m.Width)
	f.height = max(1, m.Height)
	return forwardAll(f.screens, tea.WindowSizeMsg{Width: f.width, Height: max(0, f.height-bodyReservedRows)})
}
func (f *Frame) setTheme(th Theme) {
	f.theme = th
	f.options.Env.Theme = th
}
func (f *Frame) handleThemeMsg(m ThemeMsg) tea.Cmd {
	f.setTheme(m.Theme)
	if themed, ok := f.modal.(interface{ SetTheme(Theme) }); ok {
		themed.SetTheme(f.theme)
	}
	return forwardAll(f.screens, m)
}
func (f *Frame) handleBackgroundColor(m tea.BackgroundColorMsg) tea.Cmd {
	f.setTheme(NewThemeFor(m.IsDark()))
	return forwardAll(f.screens, ThemeMsg{Theme: f.theme})
}
func (f *Frame) handleHeaderResult(m headerResult) tea.Cmd {
	f.header, f.headerErr = m.h, m.err
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return headerTick{} })
}
func (f *Frame) showToast(m ToastMsg) tea.Cmd {
	f.toast = string(m)
	f.toastGeneration++
	generation := f.toastGeneration
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return toastExpire{generation} })
}
func (f *Frame) expireToast(m toastExpire) {
	if m.generation == f.toastGeneration {
		f.toast = ""
	}
}
func (f *Frame) openModal(m modalOpen) {
	f.modal = m.modal
	f.modalScreen = m.screen
}
func (f *Frame) startBackground(m backgroundStart) tea.Cmd {
	f.background[m.id] = backgroundItem{m.label, m.screen}
	return tea.Batch(backgroundWork(m.id, m.label, m.screen, m.work), func() tea.Msg { return m.tick })
}
func (f *Frame) tickSpinner(m spinner.TickMsg) tea.Cmd {
	var c tea.Cmd
	f.spinner, c = f.spinner.Update(m)
	if len(f.background) > 0 {
		return c
	}
	return nil
}
func (f *Frame) handleModalResult(msg tea.Msg) (tea.Cmd, bool) {
	if f.modal == nil {
		return nil, false
	}
	i := f.modalScreen
	f.modal = nil
	return f.routeScreen(i, msg), true
}
func (f *Frame) handleMouseWheel(m tea.MouseWheelMsg) tea.Cmd {
	if f.help {
		return nil
	}
	if m.Y < 2 || m.Y >= f.height-2 {
		return nil
	}
	return f.routeToModalOrScreen(tea.MouseWheelMsg{X: m.X, Y: m.Y - 2, Button: m.Button, Mod: m.Mod})
}
func (f *Frame) handleMouseClick(m tea.MouseClickMsg) (tea.Cmd, bool) {
	mouse := tea.Mouse(m)
	if mouse.Button != tea.MouseLeft {
		return nil, true
	}
	if mouse.Y == 1 {
		return f.handleTabStripClick(mouse), true
	}
	if mouse.Y >= 2 && mouse.Y < f.height-2 {
		return f.handleBodyClick(mouse), true
	}
	return nil, false
}
func (f *Frame) handleTabStripClick(mouse tea.Mouse) tea.Cmd {
	current := f.screenAt(f.index)
	if f.modal != nil || f.help || current != nil && current.CapturingInput() {
		return nil
	}
	column := 0
	for i, tab := range f.options.Tabs {
		width := lipgloss.Width(tab.Title) + 2
		if mouse.X >= column && mouse.X < column+width {
			f.index = i
			f.tabFocus = true
			return f.initCurrent()
		}
		column += width + 1
	}
	return nil
}
func (f *Frame) handleBodyClick(mouse tea.Mouse) tea.Cmd {
	if f.help {
		return nil
	}
	f.tabFocus = false
	return f.routeToModalOrScreen(tea.MouseClickMsg{X: mouse.X, Y: mouse.Y - 2, Button: mouse.Button, Mod: mouse.Mod})
}
func (f *Frame) handleKey(k tea.KeyPressMsg, msg tea.Msg) (tea.Cmd, bool) {
	key := k.String()
	if key == "ctrl+c" {
		return tea.Quit, true
	}
	if f.modal != nil {
		return f.updateModal(msg), true
	}
	if f.help {
		return f.handleHelpKey(k, key), true
	}
	if f.width <= 60 || f.height <= 16 {
		return nil, true
	}
	if f.screens[f.index] != nil && f.screens[f.index].CapturingInput() {
		return f.routeScreen(f.index, msg), true
	}
	if cmd, ok := f.handleTabStripKey(key); ok {
		return cmd, true
	}
	if bubbleskey.Matches(k, globalHelpBindings[0]) {
		return tea.Quit, true
	}
	if bubbleskey.Matches(k, globalHelpBindings[1]) {
		f.help = true
		return nil, true
	}
	return f.handleNavKey(key)
}
func (f *Frame) handleHelpKey(k tea.KeyPressMsg, key string) tea.Cmd {
	if key == "esc" || bubbleskey.Matches(k, globalHelpBindings[1]) {
		f.help = false
	}
	if bubbleskey.Matches(k, globalHelpBindings[0]) {
		return tea.Quit
	}
	return nil
}
func (f *Frame) handleTabStripKey(key string) (tea.Cmd, bool) {
	if !f.tabFocus {
		return nil, false
	}
	switch key {
	case "up":
		return nil, true
	case "enter":
		return f.focusPaneFromTabs(0, false), true
	}
	return nil, false
}
func (f *Frame) handleNavKey(key string) (tea.Cmd, bool) {
	switch key {
	case "q":
		return tea.Quit, true
	case "?":
		f.help = true
		return nil, true
	case "right":
		return f.switchTab(1), true
	case "left":
		return f.switchTab(-1), true
	case "home":
		if f.tabFocus {
			return f.switchToTab(0), true
		}
	case "end":
		if f.tabFocus {
			return f.switchToTab(len(f.screens) - 1), true
		}
	case "tab":
		return f.handleTabKey(), true
	case "shift+tab":
		return f.handleShiftTabKey(), true
	case "enter", "down":
		if f.tabFocus {
			return f.focusPaneFromTabs(0, false), true
		}
	case "1", "2", "3":
		return f.selectTabByKey(key)
	}
	return nil, false
}
func (f *Frame) selectTabByKey(key string) (tea.Cmd, bool) {
	i := int(key[0] - '1')
	if i >= len(f.screens) {
		return nil, false
	}
	f.index = i
	return f.initCurrent(), true
}
func (f *Frame) handleTabKey() tea.Cmd {
	if f.tabFocus {
		return f.focusPaneFromTabs(0, false)
	}
	if s, ok := f.screenAt(f.index).(PaneFocusScreen); ok && s.FocusPane(false) {
		return nil
	}
	f.tabFocus = true
	return nil
}
func (f *Frame) handleShiftTabKey() tea.Cmd {
	if f.tabFocus {
		return f.focusPaneFromTabs(f.shiftTabPane(), true)
	}
	if s, ok := f.screenAt(f.index).(PaneFocusScreen); ok && s.FocusPane(true) {
		return nil
	}
	f.tabFocus = true
	return nil
}
func (f *Frame) shiftTabPane() int {
	if f.width >= 100 || f.width < 100 && f.screenAt(f.index) != nil && f.screenAt(f.index).CapturingInput() {
		return PaneCard
	}
	return PaneList
}

// focusPaneFromTabs leaves the tab strip and focuses the given pane of the current screen.
func (f *Frame) focusPaneFromTabs(pane int, reverse bool) tea.Cmd {
	f.tabFocus = false
	if s, ok := f.screenAt(f.index).(SetPaneFocusScreen); ok {
		s.SetPaneFocus(pane)
	}
	return f.routeScreen(f.index, RegionFocusMsg{Tabs: false, Reverse: reverse})
}
func (f *Frame) routeToModalOrScreen(msg tea.Msg) tea.Cmd {
	if f.modal != nil {
		return f.updateModal(msg)
	}
	return f.routeScreen(f.index, msg)
}
func (f *Frame) updateModal(msg tea.Msg) tea.Cmd {
	var c tea.Cmd
	f.modal, c = f.modal.Update(msg)
	return c
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
	switch {
	case f.headerErr != nil:
		s = "Service status unavailable"
	case !f.header.Running && f.header.Version == "" && f.header.Listen == "":
		s = "Service stopped"
	default:
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
