package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type fakeHeader struct {
	calls int
	fail  bool
}

func (h *fakeHeader) Header() (Header, error) {
	h.calls++
	if h.fail {
		return Header{}, errors.New("stopped")
	}
	return Header{Running: true, Listen: "127.0.0.1:1", Version: "test"}, nil
}

type frameScreen struct {
	name     string
	inits    int
	capture  bool
	messages []tea.Msg
	env      Env
}

func (s *frameScreen) Title() string { return s.name }
func (s *frameScreen) Init() tea.Cmd { s.inits++; return nil }
func (s *frameScreen) Update(m tea.Msg) (Screen, tea.Cmd) {
	s.messages = append(s.messages, m)
	return s, nil
}
func (s *frameScreen) View(w, h int) string { return strings.Repeat("x", w+10) + "\n" + s.name }
func (s *frameScreen) KeyHints() string     { return "keys" }
func (s *frameScreen) CapturingInput() bool { return s.capture }
func frameKey(s string) tea.KeyPressMsg     { return tuiTestKey(s) }
func TestFrameRetainsScreensCapturesKeysAndHandlesEmpty(t *testing.T) {
	var made []*frameScreen
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(e Env) Screen { s := &frameScreen{name: "a", env: e}; made = append(made, s); return s }}, {Title: "b", New: func(e Env) Screen { s := &frameScreen{name: "b", env: e}; made = append(made, s); return s }}}})
	f.Init()
	f.Update(frameKey("right"))
	f.Update(frameKey("left"))
	if len(made) != 2 || made[0].inits != 1 || made[1].inits != 1 {
		t.Fatal("screen lifecycle wrong")
	}
	made[0].capture = true
	before := len(made[0].messages)
	f.Update(frameKey("q"))
	if len(made[0].messages) != before+1 {
		t.Fatal("capture routing")
	}
	empty := NewFrame(Options{})
	empty.Update(frameKey("tab"))
	if !strings.Contains(empty.View().Content, "No screens") {
		t.Fatal("empty frame")
	}
}
func TestHeaderPollingManuallyWithoutWaiting(t *testing.T) {
	h := &fakeHeader{}
	f := NewFrame(Options{Header: h})
	initial := f.pollHeader()()
	if _, ok := initial.(headerResult); !ok {
		t.Fatalf("initial message %T", initial)
	}
	_, scheduled := f.Update(initial)
	if scheduled == nil {
		t.Fatal("header timer not scheduled")
	}
	if h.calls != 1 {
		t.Fatal("initial poll")
	}
	_, poll := f.Update(headerTick{})
	if h.calls != 1 {
		t.Fatal("tick performed poll inline")
	}
	_, _ = f.Update(poll())
	if h.calls != 2 {
		t.Fatal("second poll missing")
	}
	h.fail = true
	_, poll = f.Update(headerTick{})
	_, _ = f.Update(poll())
	if h.calls != 3 || !strings.Contains(f.View().Content, "unavailable") {
		t.Fatal("failure handling")
	}
}
func TestHeaderStoppedStatusRendersSeparatelyFromError(t *testing.T) {
	f := NewFrame(Options{})
	_, _ = f.Update(headerResult{h: Header{Running: false, Listen: "127.0.0.1:9000", Version: "v1"}})
	view := f.View().Content
	if !strings.Contains(view, "stopped") || !strings.Contains(view, "127.0.0.1:9000") || !strings.Contains(view, "v1") {
		t.Fatalf("stopped header: %q", view)
	}
	_, _ = f.Update(headerResult{err: errors.New("source stopped")})
	if !strings.Contains(f.View().Content, "Service status unavailable") {
		t.Fatal("source error not shown")
	}
}
func countFrameMessages(messages []tea.Msg) int {
	count := 0
	for _, message := range messages {
		if _, ok := message.(BackgroundMsg); ok {
			count++
		}
	}
	return count
}

func TestBackgroundCommandsTrackSeparateIDsAndRouteToCreator(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(e Env) Screen { return &frameScreen{name: "a", env: e} }}, {Title: "b", New: func(e Env) Screen { return &frameScreen{name: "b", env: e} }}}})
	f.Init()
	a := f.screens[0].(*frameScreen)
	first := a.env.Background("job", func() tea.Msg { return ToastMsg("1") })()
	second := a.env.Background("job", func() tea.Msg { return ToastMsg("2") })()
	_, c1 := f.Update(first)
	_, c2 := f.Update(second)
	if len(f.background) != 2 {
		t.Fatal("same labels collapsed")
	}
	run := func(cmd tea.Cmd) tea.Msg {
		batch := cmd().(tea.BatchMsg)
		for _, c := range batch {
			if c != nil {
				if m := c(); m != nil {
					if _, ok := m.(backgroundResult); ok {
						return m
					}
				}
			}
		}
		return nil
	}
	r1, r2 := run(c1), run(c2)
	f.Update(frameKey("right"))
	f.Update(r1)
	f.Update(r2)
	if len(f.background) != 0 || countFrameMessages(a.messages) != 2 || countFrameMessages(f.screens[1].(*frameScreen).messages) != 0 {
		t.Fatal("background routing")
	}
}

type testModal struct{}

func (testModal) Update(tea.Msg) (Modal, tea.Cmd) { return testModal{}, nil }
func (testModal) View(int, int) string            { return "" }
func (testModal) CapturingInput() bool            { return true }

func TestModalResultClosesAndRoutesToOrigin(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(e Env) Screen { return &frameScreen{name: "a", env: e} }}, {Title: "b", New: func(e Env) Screen { return &frameScreen{name: "b", env: e} }}}})

	a := f.screens[0].(*frameScreen)
	initialMessages := len(a.messages)
	f.Update(a.env.OpenModal(testModal{})())
	f.Update(frameKey("tab"))
	f.Update(ModalResultMsg{Canceled: true})
	if f.modal != nil || len(a.messages) != initialMessages+1 || f.index != 0 || f.screens[1] != nil {
		t.Fatal("modal routing/close")
	}
}

type framePaneScreen struct {
	frameScreen
	focus int
}

func (s *framePaneScreen) SetPaneFocus(pane int) { s.focus = pane }
func (s *framePaneScreen) FocusPane(reverse bool) bool {
	if reverse && s.focus == PaneCard {
		s.focus = PaneList
		return true
	}
	if !reverse && s.focus == PaneList {
		s.focus = PaneCard
		return true
	}
	return false
}

func TestFrameTabAndShiftTabMovePaneFocus(t *testing.T) {
	var screen *framePaneScreen
	f := NewFrame(Options{Tabs: []Tab{{Title: "servers", New: func(Env) Screen {
		screen = &framePaneScreen{}
		return screen
	}}}})
	f.Init()
	f.Update(frameKey("tab"))
	if screen.focus != PaneCard || f.tabFocus || f.index != 0 {
		t.Fatalf("Tab focus=%d tabFocus=%t index=%d", screen.focus, f.tabFocus, f.index)
	}
	f.Update(frameKey("shift+tab"))
	if screen.focus != PaneList || f.tabFocus || f.index != 0 {
		t.Fatalf("Shift+Tab focus=%d tabFocus=%t index=%d", screen.focus, f.tabFocus, f.index)
	}
}

func TestFrameTabFocusNavigation(t *testing.T) {
	f := NewFrame(Options{Start: 1, Tabs: []Tab{{Title: "a", New: func(Env) Screen { return &frameScreen{name: "a"} }}, {Title: "b", New: func(Env) Screen { return &frameScreen{name: "b"} }}}})
	f.Update(frameKey("tab"))
	if !f.tabFocus {
		t.Fatal("Tab should move focus to tabs when the screen has no pane focus")
	}
	f.Update(frameKey("right"))
	if f.index != 0 {
		t.Fatal("right navigation should change tabs while tabs are focused")
	}
	f.Update(frameKey("enter"))
	if f.tabFocus {
		t.Fatal("Enter should return focus to the active screen")
	}
	f.Update(frameKey("tab"))
	f.Update(frameKey("shift+tab"))
	if f.tabFocus || f.index != 0 {
		t.Fatal("Shift-Tab from tabs should return focus to screen")
	}
}

func TestFrameFooterAndBounds(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(Env) Screen { return &frameScreen{name: "a"} }}}})
	f.Update(frameKey("?"))
	for _, size := range [][2]int{{80, 4}, {60, 16}} {
		f.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		content := f.View().Content
		if !strings.Contains(content, "Window too small") || strings.Contains(content, "keys") || strings.Contains(content, "screen") {
			t.Fatalf("size %dx%d should show only warning: %q", size[0], size[1], content)
		}
		lines := strings.Split(content, "\n")
		if len(lines) != size[1] {
			t.Fatalf("got %d lines, want %d", len(lines), size[1])
		}
		for _, line := range lines {
			if lipgloss.Width(line) > size[0] {
				t.Fatal("width exceeded")
			}
		}
	}
	f.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	f.Update(frameKey("esc"))
	lines := strings.Split(f.View().Content, "\n")
	if len(lines) != 24 || !strings.Contains(f.View().Content, "keys") {
		t.Fatal("normal frame should retain exact height and footer")
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 80 {
			t.Fatal("width exceeded")
		}
	}
}
func TestFrameQuitAndCapturingCtrlC(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "capture", New: func(Env) Screen { return &frameScreen{capture: true} }}}})
	_, cmd := f.Update(frameKey("q"))
	if cmd != nil {
		t.Fatal("capturing q should not quit")
	}
	_, cmd = f.Update(frameKey("ctrl+c"))
	if cmd == nil {
		t.Fatal("Ctrl-C must quit during capture")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C did not return QuitMsg")
	}
}

func TestFrameHelpOverlayKeys(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "screen", New: func(Env) Screen { return &frameScreen{} }}}})
	f.Update(frameKey("?"))
	if !f.help {
		t.Fatal("? must open help")
	}
	_, cmd := f.Update(frameKey("q"))
	if cmd == nil {
		t.Fatal("q in help must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q in help did not return QuitMsg")
	}
	_, cmd = f.Update(frameKey("ctrl+c"))
	if cmd == nil {
		t.Fatal("Ctrl-C in help must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C in help did not return QuitMsg")
	}
	f.Update(frameKey("?"))
	if f.help {
		t.Fatal("? must close help")
	}
	f.Update(frameKey("?"))
	if !f.help || !strings.Contains(f.helpBody(10), "q") || !strings.Contains(f.helpBody(10), "quit") {
		t.Fatal("help should show the quit binding")
	}
	f.Update(frameKey("esc"))
	if f.help {
		t.Fatal("Esc must close help")
	}
}
