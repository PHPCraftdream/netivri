package tui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Header contains the service state shown in the frame banner.
type Header struct {
	Running bool
	Listen  string
	Uptime  time.Duration
	Version string
}

// HeaderSource supplies current service status; called in a Bubble Tea command.
type HeaderSource interface{ Header() (Header, error) }

// Screen is a persistent page hosted by Frame. Commands must report changes as messages.
type Screen interface {
	Title() string
	Init() tea.Cmd
	Update(tea.Msg) (Screen, tea.Cmd)
	View(width, height int) string
	KeyHints() string
	CapturingInput() bool
}

// ToastMsg is sent to Frame to replace its transient footer notice.
type ToastMsg string

// BackgroundMsg is delivered to the screen that requested the matching job.
type BackgroundMsg struct {
	Label  string
	Screen int
	Value  tea.Msg
}

// ModalResultMsg is the common explicit result format for custom modals.
type ModalResultMsg struct {
	Value    any
	Err      error
	Canceled bool
}

// Modal is a transient dialog; result messages are routed back to its opener.
type Modal interface {
	Update(tea.Msg) (Modal, tea.Cmd)
	View(width, height int) string
	CapturingInput() bool
}
type ToastFunc func(string) tea.Cmd

// Env contains frame services bound to the screen that receives it.
type Env struct {
	Home      string
	Toast     ToastFunc
	OpenModal func(Modal) tea.Cmd
	// Background must be requested from a screen's Init or Update; work runs as a command.
	Background func(string, func() tea.Msg) tea.Cmd
	Now        func() time.Time
}

// Tab defines a named, lazily created persistent screen.
type Tab struct {
	Title string
	New   func(Env) Screen
}

// Options configures a reusable shell frame and its terminal streams.
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
	header               Header
	headerErr            error
	background           map[uint64]backgroundItem
	nextJob              uint64
	spinner              spinner.Model
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

// NewFrame constructs a frame without starting a terminal program.
func NewFrame(o Options) *Frame {
	if o.Now == nil {
		o.Now = time.Now
	}
	o.Env.Home = o.Home
	if o.Env.Now == nil {
		o.Env.Now = o.Now
	}
	f := &Frame{options: o, width: 80, height: 24, screens: make([]Screen, len(o.Tabs)), initialized: make([]bool, len(o.Tabs)), background: map[uint64]backgroundItem{}, spinner: spinner.New(spinner.WithSpinner(spinner.Line))}
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
	e.Toast = func(s string) tea.Cmd { return func() tea.Msg { return ToastMsg(s) } }
	e.OpenModal = func(m Modal) tea.Cmd { return func() tea.Msg { return modalOpen{m, index} } }
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
		return f, nil
	case headerTick:
		return f, f.pollHeader()
	case headerResult:
		f.header, f.headerErr = m.h, m.err
		return f, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return headerTick{} })
	case ToastMsg:
		f.toast = string(m)
		return f, nil
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
	case ModalResultMsg:
		if f.modal != nil {
			i := f.modalScreen
			f.modal = nil
			return f, f.routeScreen(i, m)
		}
	case ConfirmResultMsg:
		if f.modal != nil {
			i := f.modalScreen
			f.modal = nil
			return f, f.routeScreen(i, m)
		}
	case FormResultMsg:
		if f.modal != nil {
			i := f.modalScreen
			f.modal = nil
			return f, f.routeScreen(i, m)
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
			if key == "esc" || key == "?" {
				f.help = false
			} else if key == "q" {
				return f, tea.Quit
			}
			return f, nil
		}
		if f.screens[f.index] != nil && f.screens[f.index].CapturingInput() {
			return f, f.routeScreen(f.index, msg)
		}
		switch key {
		case "q":
			return f, tea.Quit
		case "?":
			f.help = true
			return f, nil
		case "tab", "right":
			f.index = (f.index + 1) % len(f.screens)
			return f, f.initCurrent()
		case "shift+tab", "left":
			f.index = (f.index + len(f.screens) - 1) % len(f.screens)
			return f, f.initCurrent()
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
	var lines []string
	if f.headerErr != nil {
		lines = append(lines, "Service status unavailable")
	} else if !f.header.Running && f.header.Version == "" && f.header.Listen == "" {
		lines = append(lines, "Service stopped")
	} else {
		lines = append(lines, fmt.Sprintf("Running: %t  Listen: %s  Uptime: %s  Version: %s", f.header.Running, f.header.Listen, f.header.Uptime.Round(time.Second), f.header.Version))
	}
	if len(f.screens) == 0 {
		lines = append(lines, "No screens available")
	} else {
		var tabs strings.Builder
		for i, t := range f.options.Tabs {
			if i == f.index {
				fmt.Fprintf(&tabs, "[%d %s] ", i+1, t.Title)
			} else {
				fmt.Fprintf(&tabs, " %d %s  ", i+1, t.Title)
			}
		}
		lines = append(lines, tabs.String())
	}
	footer := "Tab switch  ? help  q quit"
	if s := f.screenAt(f.index); s != nil && s.KeyHints() != "" {
		footer = s.KeyHints() + "  " + footer
	}
	if f.toast != "" {
		footer = f.toast + "  " + footer
	}
	if len(f.background) > 0 {
		ids := make([]uint64, 0, len(f.background))
		for id := range f.background {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		var running []string
		for _, id := range ids {
			running = append(running, f.spinner.View()+" "+f.background[id].label)
		}
		footer = strings.Join(running, "  ") + "  " + footer
	}
	var content []string
	if f.help {
		content = []string{"Tab/Shift-Tab or Left/Right: switch tabs", "1-3: select tab", "?: help / close help", "q: quit", "Ctrl-C: quit", "Esc: close overlay", "Table: Up/Down, PgUp/PgDn, Home/End, j/k", "Form: Tab/Shift-Tab fields, Enter submit, Esc cancel", "Modal: y/n/Enter or Esc (Confirm)"}
	} else if f.modal != nil {
		content = strings.Split(f.modal.View(f.width, max(0, f.height-3)), "\n")
	} else if s := f.screenAt(f.index); s != nil {
		content = strings.Split(s.View(f.width, max(0, f.height-3)), "\n")
	}
	available := max(0, f.height-len(lines)-1)
	if len(content) > available {
		content = content[:available]
	}
	lines = append(lines, content...)
	lines = append(lines, footer)
	return boundedView(strings.Join(lines, "\n"), f.width, f.height)
}
func (f *Frame) screenAt(i int) Screen {
	if i < 0 || i >= len(f.screens) {
		return nil
	}
	return f.screens[i]
}
func boundedView(s string, w, h int) tea.View {
	lines := strings.Split(s, "\n")
	if h >= 0 && len(lines) > h {
		lines = lines[:h]
	}
	for i, line := range lines {
		if w >= 0 && lipgloss.Width(line) > w {
			lines[i] = clipCells(line, w)
		}
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
func clipCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		cw := lipgloss.Width(string(r))
		if used+cw > width {
			break
		}
		b.WriteRune(r)
		used += cw
	}
	return b.String()
}

// Start runs the shell against the configured terminal streams.
func Start(o Options) error {
	opts := []tea.ProgramOption{tea.WithInput(o.Input), tea.WithOutput(o.Output)}
	if os.Getenv("NO_COLOR") != "" {
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	_, err := tea.NewProgram(NewFrame(o), opts...).Run()
	return err
}

// IO supplies the process standard streams for a set of tabs.
func IO(tabs []Tab, env Env, header HeaderSource) Options {
	return Options{Tabs: tabs, Env: env, Header: header, Input: os.Stdin, Output: os.Stdout}
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
