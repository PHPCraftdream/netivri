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
func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "q", "?", "1", "2", "3":
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Text: s}
}
func TestFrameRetainsScreensCapturesKeysAndHandlesEmpty(t *testing.T) {
	var made []*frameScreen
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(e Env) Screen { s := &frameScreen{name: "a", env: e}; made = append(made, s); return s }}, {Title: "b", New: func(e Env) Screen { s := &frameScreen{name: "b", env: e}; made = append(made, s); return s }}}})
	f.Init()
	f.Update(key("tab"))
	f.Update(key("shift+tab"))
	if len(made) != 2 || made[0].inits != 1 || made[1].inits != 1 {
		t.Fatal("screen lifecycle wrong")
	}
	made[0].capture = true
	f.Update(key("q"))
	if len(made[0].messages) != 1 {
		t.Fatal("capture routing")
	}
	empty := NewFrame(Options{})
	empty.Update(key("tab"))
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
	if !strings.Contains(view, "Running: false") || !strings.Contains(view, "127.0.0.1:9000") || !strings.Contains(view, "v1") {
		t.Fatalf("stopped header: %q", view)
	}
	_, _ = f.Update(headerResult{err: errors.New("source stopped")})
	if !strings.Contains(f.View().Content, "Service status unavailable") {
		t.Fatal("source error not shown")
	}
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
	f.Update(key("tab"))
	f.Update(r1)
	f.Update(r2)
	if len(f.background) != 0 || len(a.messages) != 2 || len(f.screens[1].(*frameScreen).messages) != 0 {
		t.Fatal("background routing")
	}
}
func TestModalResultClosesAndRoutesToOrigin(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(e Env) Screen { return &frameScreen{name: "a", env: e} }}, {Title: "b", New: func(e Env) Screen { return &frameScreen{name: "b", env: e} }}}})
	f.Init()
	a := f.screens[0].(*frameScreen)
	f.Update(a.env.OpenModal((&Form{}).AsModal())())
	f.Update(key("tab"))
	f.Update(FormResultMsg{Canceled: true})
	if f.modal != nil || len(a.messages) != 1 || f.index != 0 || f.screens[1] != nil {
		t.Fatal("modal routing/close")
	}
}
func TestFrameFooterAndBounds(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "a", New: func(Env) Screen { return &frameScreen{name: "a"} }}}})
	f.Update(key("?"))
	f.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
	lines := strings.Split(f.View().Content, "\n")
	if len(lines) != 4 || !strings.Contains(lines[3], "q quit") {
		t.Fatalf("footer: %q", f.View().Content)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 80 {
			t.Fatal("width exceeded")
		}
	}
	f.Update(tea.WindowSizeMsg{Width: 12, Height: 4})
	for _, line := range strings.Split(f.View().Content, "\n") {
		if lipgloss.Width(line) > 12 {
			t.Fatal("narrow width exceeded")
		}
	}
}
func TestFrameQuitAndCapturingCtrlC(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "capture", New: func(Env) Screen { return &frameScreen{capture: true} }}}})
	_, cmd := f.Update(key("q"))
	if cmd != nil {
		t.Fatal("capturing q should not quit")
	}
	_, cmd = f.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("Ctrl-C must quit during capture")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C did not return QuitMsg")
	}
}

func TestFrameHelpOverlayKeys(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "screen", New: func(Env) Screen { return &frameScreen{} }}}})
	f.Update(key("?"))
	if !f.help {
		t.Fatal("? must open help")
	}
	_, cmd := f.Update(key("q"))
	if cmd == nil {
		t.Fatal("q in help must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q in help did not return QuitMsg")
	}
	_, cmd = f.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("Ctrl-C in help must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C in help did not return QuitMsg")
	}
	f.Update(key("?"))
	if f.help {
		t.Fatal("? must close help")
	}
	f.Update(key("?"))
	f.Update(key("esc"))
	if f.help {
		t.Fatal("Esc must close help")
	}
}
