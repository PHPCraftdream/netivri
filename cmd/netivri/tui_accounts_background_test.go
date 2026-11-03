package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func TestAccountsBackgroundPollBehindModal(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	calls, scheduled := 0, 0
	var screen *accountsScreen
	frame := tui.NewFrame(tui.Options{
		Now: func() time.Time { return now },
		Tabs: []tui.Tab{{Title: "Accounts", New: func(env tui.Env) tui.Screen {
			screen = newAccountsScreen(env.Home, env).(*accountsScreen)
			screen.status = func(string) (angel.Status, error) {
				calls++
				return angel.Status{}, nil
			}
			screen.pollWait = func(_ time.Duration, callback func(time.Time) tea.Msg) tea.Cmd {
				scheduled++
				return func() tea.Msg { return callback(now) }
			}
			return screen
		}}},
	})
	frame.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	frame.Update(tui.ThemeMsg{Theme: tui.NewTheme()})

	// Put a real frame-managed modal above the accounts screen.
	frame.Update(screen.env.OpenModal(tui.NewPrompt(tui.TextPrompt, "Blocking dialog", "Value", "", ""))())
	if !strings.Contains(frame.View().Content, "Blocking dialog") {
		t.Fatal("modal was not displayed")
	}

	executeBackground := func(cmd tea.Cmd) tea.Cmd {
		t.Helper()
		if cmd == nil {
			t.Fatal("expected background command")
		}
		msg := cmd()
		_, work := frame.Update(msg)
		batch, ok := work().(tea.BatchMsg)
		if !ok || len(batch) == 0 {
			t.Fatalf("background work batch = %T", work())
		}
		result := batch[0]()
		_, next := frame.Update(result)
		return next
	}
	next := executeBackground(screen.rateTick())
	if next == nil {
		t.Fatal("timer did not start status poll")
	}
	next = executeBackground(next)
	if next == nil {
		t.Fatal("status result did not schedule another timer")
	}
	if calls != 1 || scheduled != 2 {
		t.Fatalf("first poll calls=%d scheduled timers=%d; want 1 and 2", calls, scheduled)
	}
	now = now.Add(time.Second)
	next = executeBackground(next)
	if next == nil {
		t.Fatal("timer did not start second status poll")
	}
	next = executeBackground(next)
	if next == nil {
		t.Fatal("second status result did not schedule another timer")
	}
	if calls != 2 || scheduled != 3 {
		t.Fatalf("second poll calls=%d scheduled timers=%d; want 2 and 3", calls, scheduled)
	}
	if !strings.Contains(frame.View().Content, "Blocking dialog") {
		t.Fatal("background poll closed or obscured the modal")
	}
}
