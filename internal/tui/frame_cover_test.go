package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// crumbScreen adds a breadcrumb to frameScreen for header rendering coverage.
type crumbScreen struct{ frameScreen }

func (s *crumbScreen) Breadcrumb() string { return "root / detail" }

func teaMsgType(m tea.Msg) string { return fmt.Sprintf("%T", m) }

func makeCoverTabs(n int, made *[]*frameScreen) []Tab {
	tabs := make([]Tab, n)
	for i := range tabs {
		name := string(rune('a' + i))
		tabs[i] = Tab{Title: name, New: func(e Env) Screen {
			s := &frameScreen{name: name, env: e}
			*made = append(*made, s)
			return s
		}}
	}
	return tabs
}

func TestFrameCoverNewFrameDefaults(t *testing.T) {
	// frame.go 146-165: Now default, theme fallback, Start clamping.
	var made []*frameScreen
	f := NewFrame(Options{Start: 99, Tabs: makeCoverTabs(3, &made)})
	if f.index != 0 {
		t.Fatalf("Start=99 clamped index = %d, want 0", f.index)
	}
	if f.theme.Accent == nil {
		t.Fatal("theme fallback: Accent is nil, want NewTheme defaults")
	}
	if len(made) != 1 || made[0].name != "a" {
		t.Fatalf("constructed screens = %d (name %q), want 1 (a)", len(made), made[0].name)
	}
	var nowSeen bool
	tabs := []Tab{{Title: "t", New: func(e Env) Screen {
		nowSeen = e.Now != nil
		return &frameScreen{name: "t", env: e}
	}}}
	g := NewFrame(Options{Tabs: tabs})
	if !nowSeen {
		t.Fatal("Env.Now was nil, want default time.Now wiring")
	}
	if g.width != 80 || g.height != 24 {
		t.Fatalf("default size %dx%d, want 80x24", g.width, g.height)
	}
	var madeNeg []*frameScreen
	neg := NewFrame(Options{Start: -2, Tabs: makeCoverTabs(2, &madeNeg)})
	if neg.index != 0 {
		t.Fatalf("Start=-2 clamped index = %d, want 0", neg.index)
	}
}

func TestFrameCoverInitBatchesHeaderPollAndDoesNotReinit(t *testing.T) {
	// frame.go 182-215: ensureScreen/initCurrent, Init batch, no double init, switchToTab clamp.
	h := &fakeHeader{}
	var made []*frameScreen
	f := NewFrame(Options{Header: h, Tabs: makeCoverTabs(2, &made)})
	cmd := f.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd, want batch with header poll")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init cmd produced %T, want tea.BatchMsg", msg)
	}
	sawHeader := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if m := c(); m != nil {
			if _, isHeader := m.(headerResult); isHeader {
				sawHeader = true
			}
		}
	}
	if !sawHeader {
		t.Fatal("Init batch did not include header poll result")
	}
	if h.calls != 1 {
		t.Fatalf("header calls = %d, want 1", h.calls)
	}
	if made[0].inits != 1 {
		t.Fatalf("first screen inits = %d, want 1", made[0].inits)
	}
	_ = f.Init() // second Init must not re-init the current screen
	if made[0].inits != 1 {
		t.Fatalf("second Init re-ran Init: inits = %d, want 1", made[0].inits)
	}
	f.Update(switchTabMsg(9))
	if f.index != 0 {
		t.Fatalf("switchTabMsg(9) left index = %d, want 0 (clamped)", f.index)
	}
	f.Update(switchTabMsg(-1))
	if f.index != 0 {
		t.Fatalf("switchTabMsg(-1) left index = %d, want 0 (clamped)", f.index)
	}
}

func TestFrameCoverToastLifecycle(t *testing.T) {
	// frame.go 249-258: ToastMsg sets toast + schedules expiry; mismatched generation keeps toast.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	_, cmd := f.Update(ToastMsg("saved"))
	if f.toast != "saved" {
		t.Fatalf("toast = %q, want %q", f.toast, "saved")
	}
	if cmd == nil {
		t.Fatal("ToastMsg returned nil cmd, want expiry tick (not executed)")
	}
	f.Update(toastExpire{generation: 999})
	if f.toast != "saved" {
		t.Fatalf("mismatched expiry cleared toast: %q, want %q", f.toast, "saved")
	}
	f.Update(toastExpire{generation: f.toastGeneration})
	if f.toast != "" {
		t.Fatalf("matching expiry kept toast: %q, want empty", f.toast)
	}
}

func TestFrameCoverSwitchTabMsgAndThemeForward(t *testing.T) {
	// frame.go 231-239: switchTabMsg routes to tab; ThemeMsg updates theme and forwards.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(2, &made)})
	f.Init()
	f.Update(switchTabMsg(1))
	if f.index != 1 {
		t.Fatalf("index = %d, want 1", f.index)
	}
	if !f.tabFocus {
		t.Fatal("switchToTab did not set tabFocus, want true")
	}
	if made[1].inits != 1 {
		t.Fatalf("tab 1 inits = %d, want 1", made[1].inits)
	}
	light := NewThemeFor(false)
	f.Update(ThemeMsg{Theme: light})
	if f.theme.Dark != false {
		t.Fatalf("f.theme.Dark = %v, want false", f.theme.Dark)
	}
	forwarded := 0
	for _, m := range made[0].messages {
		if tm, ok := m.(ThemeMsg); ok && !tm.Theme.Dark {
			forwarded++
		}
	}
	if forwarded != 1 {
		t.Fatalf("light ThemeMsg forwards to screen = %d, want 1", forwarded)
	}
}

func TestFrameCoverBackgroundColorSwitch(t *testing.T) {
	// frame.go 240-243: BackgroundColorMsg switches theme via NewThemeFor and forwards.
	var made []*frameScreen
	f := NewFrame(Options{Env: Env{Theme: NewThemeFor(false)}, Tabs: makeCoverTabs(1, &made)})
	if f.theme.Dark {
		t.Fatalf("initial theme Dark = %v, want false", f.theme.Dark)
	}
	f.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	if !f.theme.Dark {
		t.Fatalf("after dark background f.theme.Dark = %v, want true", f.theme.Dark)
	}
	forwarded := 0
	for _, m := range made[0].messages {
		if tm, ok := m.(ThemeMsg); ok && tm.Theme.Dark {
			forwarded++
		}
	}
	if forwarded != 1 {
		t.Fatalf("dark ThemeMsg forwards to screen = %d, want 1", forwarded)
	}
}

func TestFrameCoverQuitHelpToggle(t *testing.T) {
	// frame.go 332-385: q quits, ? toggles help, esc closes help.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	_, cmd := f.Update(frameKey("q"))
	if cmd == nil {
		t.Fatal("q returned nil cmd, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q cmd produced %T, want tea.QuitMsg", cmd())
	}
	f.Update(frameKey("?"))
	if !f.help {
		t.Fatal("? did not open help: f.help = false")
	}
	f.Update(frameKey("?"))
	if f.help {
		t.Fatal("? did not close help: f.help = true")
	}
	f.Update(frameKey("?"))
	f.Update(frameKey("esc"))
	if f.help {
		t.Fatal("esc did not close help: f.help = true")
	}
}

func TestFrameCoverLeftRightAndDigitTabs(t *testing.T) {
	// frame.go 386-441: left/right switch tabs; 1/2/3 select tabs; out-of-range digit ignored.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(3, &made)})
	f.Init()
	f.Update(frameKey("right"))
	if f.index != 1 {
		t.Fatalf("after right index = %d, want 1", f.index)
	}
	if made[1].inits != 1 {
		t.Fatalf("tab 1 inits = %d, want 1", made[1].inits)
	}
	f.Update(frameKey("left"))
	if f.index != 0 {
		t.Fatalf("after left index = %d, want 0", f.index)
	}
	if made[0].inits != 1 {
		t.Fatalf("returning to tab 0 re-ran Init: inits = %d, want 1 (initCurrent is guarded by initialized)", made[0].inits)
	}
	f.Update(frameKey("3"))
	if f.index != 2 {
		t.Fatalf("after key 3 index = %d, want 2", f.index)
	}
	if made[2].inits != 1 {
		t.Fatalf("tab 2 inits = %d, want 1", made[2].inits)
	}
	f.Update(frameKey("5"))
	if f.index != 2 {
		t.Fatalf("key 5 changed index to %d, want 2 (unchanged)", f.index)
	}
	f.Update(frameKey("1"))
	if f.index != 0 {
		t.Fatalf("after key 1 index = %d, want 0", f.index)
	}
}

func TestFrameCoverRegionFocusEnterResetsPane(t *testing.T) {
	// frame.go 363-372 + 398-410: tab twice reaches tab strip; enter resets and calls SetPaneFocus(0).
	var screen *framePaneScreen
	f := NewFrame(Options{Tabs: []Tab{{Title: "servers", New: func(Env) Screen {
		screen = &framePaneScreen{}
		return screen
	}}}})
	f.Init()
	f.Update(frameKey("tab")) // PaneList -> PaneCard inside the screen
	if screen.focus != PaneCard || f.tabFocus {
		t.Fatalf("first tab: focus=%d tabFocus=%t, want focus=%d tabFocus=false", screen.focus, f.tabFocus, PaneCard)
	}
	f.Update(frameKey("tab")) // PaneCard boundary -> tab strip
	if !f.tabFocus {
		t.Fatal("second tab did not move focus to tab strip: f.tabFocus = false")
	}
	f.Update(frameKey("enter"))
	if f.tabFocus {
		t.Fatal("enter did not leave tab strip: f.tabFocus = true")
	}
	if screen.focus != 0 {
		t.Fatalf("SetPaneFocus got %d, want 0", screen.focus)
	}
	found := false
	for _, m := range screen.messages {
		if rf, ok := m.(RegionFocusMsg); ok && !rf.Tabs && !rf.Reverse {
			found = true
		}
	}
	if !found {
		t.Fatal("enter did not route RegionFocusMsg{Tabs:false} to the screen")
	}
}

func TestFrameCoverTabClickSelects(t *testing.T) {
	// frame.go 273-293: MouseClickMsg Y==1 on tab labels selects tabs.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(2, &made)})
	f.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	if f.index != 0 {
		t.Fatalf("click X=1 on first tab index = %d, want 0", f.index)
	}
	if !f.tabFocus {
		t.Fatal("tab click did not set tabFocus, want true")
	}
	f.Update(tea.MouseClickMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	if f.index != 1 {
		t.Fatalf("click X=5 on second tab index = %d, want 1", f.index)
	}
	if made[1].inits != 1 {
		t.Fatalf("tab 1 inits = %d, want 1", made[1].inits)
	}
	f.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseRight})
	if f.index != 1 {
		t.Fatalf("non-left click changed index to %d, want 1", f.index)
	}
	f.help = true
	f.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	if f.index != 1 {
		t.Fatalf("click while help open changed index to %d, want 1", f.index)
	}
}

func TestFrameCoverBodyClickTranslatedAndRouted(t *testing.T) {
	// frame.go 295-306: body clicks translate Y-2 and route to the current screen.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	f.Update(tea.MouseClickMsg{X: 7, Y: 5, Button: tea.MouseLeft})
	var got *tea.MouseClickMsg
	for _, m := range made[0].messages {
		if mc, ok := m.(tea.MouseClickMsg); ok {
			c := mc
			got = &c
		}
	}
	if got == nil {
		t.Fatal("body click was not routed to the screen")
	}
	if got.X != 7 || got.Y != 3 {
		t.Fatalf("translated click = (X=%d,Y=%d), want (X=7,Y=3)", got.X, got.Y)
	}
	if f.tabFocus {
		t.Fatal("body click did not clear tabFocus, want false")
	}
}

func TestFrameCoverModalClicksWheelsAndResults(t *testing.T) {
	// frame.go 267-271, 301-306, 322-327: modal swallows body clicks/wheels; result msgs close and route.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	f.Update(modalOpen{modal: testModal{}, screen: 0})
	if f.modal == nil {
		t.Fatal("modalOpen did not set f.modal")
	}
	before := len(made[0].messages)
	f.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if f.modal == nil {
		t.Fatal("body click closed the modal, want it to stay open")
	}
	if len(made[0].messages) != before {
		t.Fatalf("screen messages = %d, want %d (click swallowed by modal)", len(made[0].messages), before)
	}
	f.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if f.modal == nil || len(made[0].messages) != before {
		t.Fatalf("wheel with modal: modal=%v messages=%d, want modal open and %d messages", f.modal != nil, len(made[0].messages), before)
	}
	results := []tea.Msg{ModalResultMsg{}, ConfirmResultMsg{}, PromptResultMsg{}, WizardResultMsg{}}
	for _, res := range results {
		f.Update(modalOpen{modal: testModal{}, screen: 0})
		count := len(made[0].messages)
		f.Update(res)
		if f.modal != nil {
			t.Fatalf("%T did not close f.modal", res)
		}
		if len(made[0].messages) != count+1 {
			t.Fatalf("%T routed %d messages, want %d", res, len(made[0].messages)-count, 1)
		}
		if got := made[0].messages[len(made[0].messages)-1]; teaMsgType(got) != teaMsgType(res) {
			t.Fatalf("routed message %T, want %T", got, res)
		}
	}
}

func TestFrameCoverWheelHelpAndBody(t *testing.T) {
	// frame.go 259-272: wheel in help ignored; wheel in body translated and routed.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	f.Update(frameKey("?"))
	before := len(made[0].messages)
	f.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if len(made[0].messages) != before {
		t.Fatalf("wheel in help routed %d messages, want 0", len(made[0].messages)-before)
	}
	f.Update(frameKey("esc"))
	f.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	var got *tea.MouseWheelMsg
	for _, m := range made[0].messages {
		if mw, ok := m.(tea.MouseWheelMsg); ok {
			w := mw
			got = &w
		}
	}
	if got == nil {
		t.Fatal("wheel in body was not routed to the screen")
	}
	if got.Y != 3 {
		t.Fatalf("translated wheel Y = %d, want 3", got.Y)
	}
}

func TestFrameCoverRouteScreenAndForwardAllNilSafety(t *testing.T) {
	// frame.go 451-463 + 487-494: routeScreen/forwardAll skip nil screens and out-of-range indexes.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(2, &made)})
	if c := f.routeScreen(-1, ToastMsg("x")); c != nil {
		t.Fatalf("routeScreen(-1) returned %v, want nil", c)
	}
	if c := f.routeScreen(2, ToastMsg("x")); c != nil {
		t.Fatalf("routeScreen(2) returned %v, want nil", c)
	}
	if c := f.routeScreen(1, ToastMsg("x")); c != nil {
		t.Fatalf("routeScreen(1) with nil screen returned %v, want nil", c)
	}
	f.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	var got *tea.WindowSizeMsg
	for _, m := range made[0].messages {
		if ws, ok := m.(tea.WindowSizeMsg); ok {
			w := ws
			got = &w
		}
	}
	if got == nil {
		t.Fatal("WindowSizeMsg was not forwarded to the screen")
	}
	if got.Width != 80 || got.Height != 20 {
		t.Fatalf("forwarded size = %dx%d, want 80x20 (body height)", got.Width, got.Height)
	}
}

func TestFrameCoverViewTooSmall(t *testing.T) {
	// frame.go 495-502: too-small sizes render the centered notice with exact height.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	for _, size := range [][2]int{{55, 20}, {80, 16}} {
		f.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		content := f.View().Content
		if !strings.Contains(content, "Window too small - enlarge the window") {
			t.Fatalf("size %dx%d view missing notice: %q", size[0], size[1], content)
		}
		lines := strings.Split(content, "\n")
		if len(lines) != size[1] {
			t.Fatalf("size %dx%d produced %d lines, want %d", size[0], size[1], len(lines), size[1])
		}
	}
}

func TestFrameCoverHelpBodyClipping(t *testing.T) {
	// frame.go 595-611 + 509-510: help body is clipped to the body height.
	var made []*frameScreen
	f := NewFrame(Options{Tabs: makeCoverTabs(1, &made)})
	f.Update(frameKey("?"))
	tall := f.helpBody(20)
	short := f.helpBody(1)
	if got := len(strings.Split(short, "\n")); got != 1 {
		t.Fatalf("helpBody(1) lines = %d, want 1", got)
	}
	if len(short) >= len(tall) {
		t.Fatalf("helpBody(1) len %d not shorter than helpBody(20) len %d", len(short), len(tall))
	}
	f.Update(tea.WindowSizeMsg{Width: 80, Height: 17}) // body height 17-4
	lines := strings.Split(f.View().Content, "\n")
	if len(lines) != 17 {
		t.Fatalf("help view at 80x17 produced %d lines, want 17", len(lines))
	}
}

func TestFrameCoverHeaderErrorAndBreadcrumb(t *testing.T) {
	// frame.go 541-551: header error text; breadcrumb appended when screen implements BreadcrumbScreen.
	f := NewFrame(Options{Tabs: []Tab{{Title: "c", New: func(Env) Screen {
		return &crumbScreen{frameScreen{name: "c"}}
	}}}})
	header := strings.Split(f.View().Content, "\n")[0]
	if !strings.Contains(header, "root / detail") {
		t.Fatalf("header line %q missing breadcrumb %q", header, "root / detail")
	}
	f.Update(headerResult{err: errors.New("down")})
	header = strings.Split(f.View().Content, "\n")[0]
	if !strings.Contains(header, "Service status unavailable") {
		t.Fatalf("header line %q missing %q after error", header, "Service status unavailable")
	}
	if !strings.Contains(header, "root / detail") {
		t.Fatalf("header line %q lost breadcrumb after error", header)
	}
}
