package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type asyncRegressionOtherScreen struct{}

func (*asyncRegressionOtherScreen) Title() string                          { return "Other" }
func (*asyncRegressionOtherScreen) Init() tea.Cmd                          { return nil }
func (s *asyncRegressionOtherScreen) Update(tea.Msg) (tui.Screen, tea.Cmd) { return s, nil }
func (*asyncRegressionOtherScreen) View(int, int) string                   { return "Other screen" }
func (*asyncRegressionOtherScreen) KeyHints() string                       { return "" }
func (*asyncRegressionOtherScreen) CapturingInput() bool                   { return false }

func asyncRegressionBackgroundWorkResult(t *testing.T, work tea.Cmd) tea.Msg {
	t.Helper()
	if work == nil {
		t.Fatal("expected accepted background work")
	}
	msg := work()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("background work batch = %T, want a non-empty tea.BatchMsg", msg)
	}
	return batch[0]()
}

func asyncRegressionBackgroundResult(t *testing.T, frame *tui.Frame, start tea.Cmd) tea.Msg {
	t.Helper()
	if start == nil {
		t.Fatal("expected a frame-managed background start command")
	}
	_, work := frame.Update(start())
	if work == nil {
		t.Fatal("background start was not accepted by the frame")
	}
	return asyncRegressionBackgroundWorkResult(t, work)
}

func asyncRegressionInitBackgrounds(t *testing.T, frame *tui.Frame, want int) []tea.Cmd {
	t.Helper()
	return asyncRegressionBatch(t, frame.Init(), want)
}
func asyncRegressionBatch(t *testing.T, cmd tea.Cmd, want int) tea.BatchMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a rescheduled command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) != want {
		t.Fatalf("scheduled commands = %T, want tea.BatchMsg with %d entries", msg, want)
	}
	return batch
}

func TestLogsFrameCapturesFilterKeysBeforeGlobalKeys(t *testing.T) {
	var screen *logsScreen
	frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Logs", New: func(env tui.Env) tui.Screen {
		screen = newLogsScreen(env.Home, env).(*logsScreen)
		return screen
	}}}})

	_, cmd := frame.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if cmd != nil || !screen.list.Filtering {
		t.Fatalf("slash: command=%v filtering=%v", cmd != nil, screen.list.Filtering)
	}
	for _, r := range []rune{'q', '?'} {
		_, cmd = frame.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatalf("key %q returned QuitMsg", r)
			}
		}
	}
	if screen.list.Filter != "q?" {
		t.Fatalf("filter = %q, want q?", screen.list.Filter)
	}
	if !strings.Contains(frame.View().Content, "/q?") {
		t.Fatalf("frame view does not show the captured filter: %q", frame.View().Content)
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if screen.list.Filtering || screen.list.Filter != "" {
		t.Fatalf("Esc did not reach the filter: filtering=%v filter=%q", screen.list.Filtering, screen.list.Filter)
	}
}

func TestServiceFrameTimerPollsBehindTabAndContinues(t *testing.T) {
	probes, timers := 0, 0
	var screen *serviceScreen
	frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{
		{Title: "Service", New: func(env tui.Env) tui.Screen {
			screen = newServiceScreen(env.Home, env).(*serviceScreen)
			screen.probe = func(string) (angel.Status, error) {
				probes++
				return angel.Status{Angel: "running", Listen: "127.0.0.1:7890"}, nil
			}
			screen.wait = func(_ time.Duration, callback func(time.Time) tea.Msg) tea.Cmd {
				timers++
				return func() tea.Msg { return callback(time.Time{}) }
			}
			return screen
		}},
		{Title: "Other", New: func(tui.Env) tui.Screen { return &asyncRegressionOtherScreen{} }},
	}})

	initial := asyncRegressionInitBackgrounds(t, frame, 2)
	probeResult := asyncRegressionBackgroundResult(t, frame, initial[0])
	frame.Update(probeResult)
	firstTimer := initial[1]
	frame.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !strings.Contains(frame.View().Content, "[Other]") {
		t.Fatal("did not leave the Service tab")
	}
	firstResult := asyncRegressionBackgroundResult(t, frame, firstTimer)
	_, cycle := frame.Update(firstResult)
	cycleBatch := asyncRegressionBatch(t, cycle, 2)

	probeResult = asyncRegressionBackgroundResult(t, frame, cycleBatch[0])
	frame.Update(probeResult)
	if probes != 2 || timers != 2 {
		t.Fatalf("first cycle probes=%d timers=%d, want 2 and 2", probes, timers)
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !strings.Contains(frame.View().Content, "[Service]") {
		t.Fatal("did not return to the Service tab")
	}
	nextTimerResult := asyncRegressionBackgroundResult(t, frame, cycleBatch[1])
	_, nextCycle := frame.Update(nextTimerResult)
	nextBatch := asyncRegressionBatch(t, nextCycle, 2)
	probeResult = asyncRegressionBackgroundResult(t, frame, nextBatch[0])
	frame.Update(probeResult)
	if probes != 3 || timers != 3 {
		t.Fatalf("second cycle probes=%d timers=%d, want 3 and 3", probes, timers)
	}
}

func TestLogsFrameTimerPollsBehindTabAndContinues(t *testing.T) {
	polls, timers := 0, 0
	var screen *logsScreen
	frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{
		{Title: "Logs", New: func(env tui.Env) tui.Screen {
			screen = newLogsScreen(env.Home, env).(*logsScreen)
			screen.read = func(string, angel.LogQuery) ([]angel.LogEntry, int64, error) {
				return nil, 0, nil
			}
			screen.poll = func(_ string, from int64, _ angel.LogQuery) ([]angel.LogEntry, int64, error) {
				polls++
				return nil, from, nil
			}
			screen.wait = func(_ time.Duration, callback func(time.Time) tea.Msg) tea.Cmd {
				timers++
				return func() tea.Msg { return callback(time.Time{}) }
			}
			return screen
		}},
		{Title: "Other", New: func(tui.Env) tui.Screen { return &asyncRegressionOtherScreen{} }},
	}})

	initial := asyncRegressionInitBackgrounds(t, frame, 2)
	probeResult := asyncRegressionBackgroundResult(t, frame, initial[0])
	frame.Update(probeResult)
	firstTimer := initial[1]
	frame.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !strings.Contains(frame.View().Content, "[Other]") {
		t.Fatal("did not leave the Logs tab")
	}
	firstResult := asyncRegressionBackgroundResult(t, frame, firstTimer)
	_, cycle := frame.Update(firstResult)
	cycleBatch := asyncRegressionBatch(t, cycle, 2)

	pollResult := asyncRegressionBackgroundResult(t, frame, cycleBatch[0])
	frame.Update(pollResult)
	if polls != 1 || timers != 2 {
		t.Fatalf("first cycle polls=%d timers=%d, want 1 and 2", polls, timers)
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !strings.Contains(frame.View().Content, "[Logs]") {
		t.Fatal("did not return to the Logs tab")
	}
	nextTimerResult := asyncRegressionBackgroundResult(t, frame, cycleBatch[1])
	_, nextCycle := frame.Update(nextTimerResult)
	nextBatch := asyncRegressionBatch(t, nextCycle, 2)
	pollResult = asyncRegressionBackgroundResult(t, frame, nextBatch[0])
	frame.Update(pollResult)
	if polls != 2 || timers != 3 {
		t.Fatalf("second cycle polls=%d timers=%d, want 2 and 3", polls, timers)
	}
}
