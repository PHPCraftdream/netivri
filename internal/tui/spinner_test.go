package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type spinTestScreen struct {
	env Env
}

func (spinTestScreen) Title() string                    { return "spin" }
func (spinTestScreen) Init() tea.Cmd                    { return nil }
func (spinTestScreen) Update(tea.Msg) (Screen, tea.Cmd) { return nil, nil }
func (spinTestScreen) View(int, int) string             { return "" }
func (spinTestScreen) KeyHints() string                 { return "" }
func (spinTestScreen) CapturingInput() bool             { return false }

func TestFrameBackgroundSpinnerIntegration(t *testing.T) {
	var env Env
	f := NewFrame(Options{Tabs: []Tab{{Title: "spin", New: func(e Env) Screen { env = e; return spinTestScreen{} }}}})
	f.Init()
	start := env.Background("sync", func() tea.Msg { return ToastMsg("done") })()
	_, startWork := f.Update(start)
	if startWork == nil {
		t.Fatal("background commands missing")
	}
	batch, ok := startWork().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("expected work and tick commands, got %T", startWork())
	}
	var workCmd, tickCmd tea.Cmd
	for _, cmd := range batch {
		msg := cmd()
		switch msg.(type) {
		case backgroundResult:
			result := msg
			workCmd = func() tea.Msg { return result }
		case spinner.TickMsg:
			tick := msg
			tickCmd = func() tea.Msg { return tick }
		}
	}
	if workCmd == nil || tickCmd == nil {
		t.Fatal("background work or spinner tick command missing")
	}
	view := f.View().Content
	if !strings.Contains(view, "| sync") {
		t.Fatalf("initial spinner label missing: %q", view)
	}
	_, nextTick := f.Update(tickCmd())
	if nextTick == nil || !strings.Contains(f.View().Content, "/ sync") {
		t.Fatalf("spinner did not advance: %q", f.View().Content)
	}
	f.Update(workCmd())
	if strings.Contains(f.View().Content, "sync") {
		t.Fatalf("completed background label remains: %q", f.View().Content)
	}
}

func TestFrameFooterFitsAndHasReservedRow(t *testing.T) {
	f := NewFrame(Options{Tabs: []Tab{{Title: "x", New: func(Env) Screen { return spinTestScreen{} }}}})
	f.Update(tea.KeyPressMsg{Text: "?", Code: '?'})
	f.Update(tea.WindowSizeMsg{Width: 14, Height: 4})
	view := f.View().Content
	lines := strings.Split(view, "\n")
	if len(lines) != 4 {
		t.Fatalf("line count %d: %q", len(lines), view)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 14 {
			t.Fatalf("width overflow %q", line)
		}
	}
}
