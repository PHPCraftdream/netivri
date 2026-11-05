package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

var logsNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type logsFixture struct {
	s        *logsScreen
	reads    []angel.LogQuery
	polls    int
	follower *angel.LogFollower
	readErr  error
}

func newLogsFixture(t *testing.T, seed string) *logsFixture {
	t.Helper()
	home := t.TempDir()
	if seed != "" {
		seedLog(t, home, seed)
	}
	env := tui.Env{
		Home:       home,
		Now:        func() time.Time { return logsNow },
		Background: func(_ string, w func() tea.Msg) tea.Cmd { return w },
		Toast:      func(string) tea.Cmd { return nil },
	}
	f := &logsFixture{}
	f.s = newLogsScreen(env.Home, env).(*logsScreen)
	f.s.wait = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	f.follower = angel.NewLogFollower(home)
	f.s.poll = func(_ string, from int64, q angel.LogQuery) ([]angel.LogEntry, int64, error) {
		f.polls++
		return f.follower.Poll(from, q)
	}
	base := f.s.read
	f.s.read = func(home string, q angel.LogQuery) ([]angel.LogEntry, int64, error) {
		f.reads = append(f.reads, q)
		if f.readErr != nil {
			return nil, 0, f.readErr
		}
		return base(home, q)
	}
	f.s = parityDrain(f.s, f.s.Init()).(*logsScreen)
	return f
}

func (f *logsFixture) key(k tea.KeyPressMsg) {
	sc, c := f.s.Update(k)
	f.s = sc.(*logsScreen)
	f.s = parityDrain(f.s, c).(*logsScreen)
}

func (f *logsFixture) tick() {
	sc, c := f.s.Update(logsTick{})
	f.s = sc.(*logsScreen)
	f.s = parityDrain(f.s, c).(*logsScreen)
}

func (f *logsFixture) wheelUp() {
	sc, c := f.s.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	f.s = sc.(*logsScreen)
	f.s = parityDrain(f.s, c).(*logsScreen)
}

func logsTypeText(f *logsFixture, text string) {
	for _, r := range text {
		f.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

const logsFiveLines = "2026-11-05T11:00:00Z INFO first thing happened\n" +
	"2026-11-05T11:01:00Z WARN second looks odd\n" +
	"2026-11-05T11:02:00Z INFO third moved along\n" +
	"2026-11-05T11:03:00Z WARN fourth is odd too\n" +
	"2026-11-05T11:04:00Z INFO fifth settled\n"

const logsFourWithMain = "2026-11-05T11:00:00Z INFO main started\n" +
	"2026-11-05T11:01:00Z INFO side tracked\n" +
	"2026-11-05T11:02:00Z WARN main is slow\n" +
	"2026-11-05T11:03:00Z INFO other ended\n"

func TestLogsScreenShowsTailNewestAtBottom(t *testing.T) {
	f := newLogsFixture(t, logsFiveLines)
	s := f.s
	if len(s.list.Rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(s.list.Rows))
	}
	if got := s.list.Rows[4].Cells[2]; got != "fifth settled" {
		t.Fatalf("last row = %q", got)
	}
	if got := s.list.Rows[0].Cells[2]; got != "first thing happened" {
		t.Fatalf("first row = %q", got)
	}
	if got := s.list.Rows[1].Cells[1]; got != "WARN" {
		t.Fatalf("level cell = %q", got)
	}
	for _, want := range []string{"info and above", "follow on"} {
		if !strings.Contains(s.list.Title, want) {
			t.Fatalf("title %q misses %q", s.list.Title, want)
		}
	}
	if l, _ := cardLine(s.workspace.Card, "follow"); l.Value != "on" {
		t.Fatalf("follow = %q", l.Value)
	}
	if l, _ := cardLine(s.workspace.Card, "level"); l.Value != "info" {
		t.Fatalf("level = %q", l.Value)
	}
	if l, _ := cardLine(s.workspace.Card, "tail"); l.Value != "200" {
		t.Fatalf("tail = %q", l.Value)
	}
	if l, _ := cardLine(s.workspace.Card, "since"); l.Value != "—" {
		t.Fatalf("since = %q", l.Value)
	}
	if l, _ := cardLine(s.workspace.Card, "search"); l.Value != "—" {
		t.Fatalf("search = %q", l.Value)
	}
	if len(f.reads) != 1 || f.reads[0].MinLevel != angel.LevelInfo || f.reads[0].Tail != 200 {
		t.Fatalf("reads = %#v", f.reads)
	}
}

func TestLogsScreenMissingLogExplainsItself(t *testing.T) {
	f := newLogsFixture(t, "")
	s := f.s
	if s.list.EmptyText != "The angel has not written a log yet." {
		t.Fatalf("empty text = %q", s.list.EmptyText)
	}
	v := ansi.Strip(s.View(80, 24))
	if len(s.list.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(s.list.Rows))
	}
	for _, c := range s.workspace.Card.Info {
		if strings.Contains(c, "The angel has not written a log yet.") {
			goto found
		}
	}
	t.Fatalf("card info %v misses the explanation", s.workspace.Card.Info)
found:
	if !strings.Contains(v, "The angel has not written a log yet.") {
		t.Fatalf("view misses the explanation:\n%s", v)
	}
	f.tick() // a poll without a log keeps the explanation
	if !s.missing || f.polls < 1 {
		t.Fatalf("poll on a missing log: missing %v polls %d", s.missing, f.polls)
	}
	appendLogFile(t, f.s.home, "2026-11-05T12:01:00Z INFO appended one\n2026-11-05T12:02:00Z INFO appended two\n")
	f.tick()
	if len(s.list.Rows) != 2 || s.missing || s.list.Selected != 1 || f.polls < 1 {
		t.Fatalf("after tick: rows %d missing %v selected %d polls %d",
			len(s.list.Rows), s.missing, s.list.Selected, f.polls)
	}
}

func TestLogsScreenLevelCyclesAndRereads(t *testing.T) {
	f := newLogsFixture(t, logsSample)
	levels := []string{angel.LevelWarn, angel.LevelError, angel.LevelDebug, angel.LevelInfo}
	titles := []string{"warn and above", "error and above", "debug and above", "info and above"}
	for i := range levels {
		f.key(tea.KeyPressMsg{Code: 'l', Text: "l"})
		s := f.s
		if !strings.Contains(s.list.Title, titles[i]) {
			t.Fatalf("title %q, want %q", s.list.Title, titles[i])
		}
		if l, _ := cardLine(s.workspace.Card, "level"); l.Value != strings.ToLower(levels[i]) {
			t.Fatalf("card level = %q, want %q", l.Value, strings.ToLower(levels[i]))
		}
	}
	if len(f.reads) != 1+len(levels) {
		t.Fatalf("reads = %d, want %d", len(f.reads), 1+len(levels))
	}
	for i, want := range levels {
		if f.reads[1+i].MinLevel != want {
			t.Fatalf("read %d level = %q, want %q", i, f.reads[1+i].MinLevel, want)
		}
	}
}

func TestLogsScreenLevelFiltersRows(t *testing.T) {
	seed := "2026-11-05T11:00:00Z DEBUG quiet probe\n2026-11-05T11:01:00Z INFO loud start\n"
	f := newLogsFixture(t, seed)
	for i := 0; i < 3; i++ {
		f.key(tea.KeyPressMsg{Code: 'l', Text: "l"})
	}
	if len(f.s.list.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(f.s.list.Rows))
	}
	if got := f.s.list.Rows[0].Cells[2]; got != "quiet probe" {
		t.Fatalf("first row = %q", got)
	}
	f2 := newLogsFixture(t, seed)
	f2.key(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if len(f2.s.list.Rows) != 0 || f2.s.list.EmptyText != "No entries yet." {
		t.Fatalf("warn hides everything: rows %d empty %q", len(f2.s.list.Rows), f2.s.list.EmptyText)
	}
}

func TestLogsScreenSearchWithRealKeys(t *testing.T) {
	f := newLogsFixture(t, logsFourWithMain)
	f.s.View(100, 30)
	f.key(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !f.s.list.Filtering {
		t.Fatalf("slash does not start the filter")
	}
	logsTypeText(f, "main")
	v := ansi.Strip(f.s.View(100, 30))
	if !strings.Contains(v, "main st") && !strings.Contains(v, "main is") {
		t.Fatalf("view misses the matches:\n%s", v)
	}
	if strings.Contains(v, "side tra") || strings.Contains(v, "other e") {
		t.Fatalf("view shows non-matching rows:\n%s", v)
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.list.Filtering || f.s.list.Filter != "main" {
		t.Fatalf("enter does not keep the filter: filtering %v filter %q", f.s.list.Filtering, f.s.list.Filter)
	}
	if l, _ := cardLine(f.s.workspace.Card, "search"); l.Value != "main" {
		t.Fatalf("search = %q", l.Value)
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if f.s.list.Filter != "" {
		t.Fatalf("esc does not clear the filter: %q", f.s.list.Filter)
	}
	if l, _ := cardLine(f.s.workspace.Card, "search"); l.Value != "—" {
		t.Fatalf("search = %q", l.Value)
	}
}

func TestLogsScreenKeysWhileFilteringStayInFilter(t *testing.T) {
	f := newLogsFixture(t, logsFourWithMain)
	f.key(tea.KeyPressMsg{Code: '/', Text: "/"})
	f.key(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !f.s.follow {
		t.Fatalf("f toggled follow while filtering")
	}
	if !strings.Contains(f.s.list.Filter, "f") {
		t.Fatalf("filter = %q, want the f in it", f.s.list.Filter)
	}
}

// promptCancelAfterTyping closes a prompt with unsaved input the way prompt.go
// really behaves: esc opens the Continue/Discard question, tab moves to
// Discard, enter cancels.
func promptCancelAfterTyping(f *logsFixture) {
	f.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	f.key(tea.KeyPressMsg{Code: tea.KeyTab})
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestLogsScreenSincePromptRealKeys(t *testing.T) {
	f := newLogsFixture(t, logsSample)
	f.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	if f.s.dialog == nil || !f.s.CapturingInput() {
		t.Fatalf("s does not open the since prompt")
	}
	v := ansi.Strip(f.s.View(80, 24))
	if !strings.Contains(v, "Logs — since") || !strings.Contains(v, "30m") {
		t.Fatalf("prompt view:\n%s", v)
	}
	logsTypeText(f, "30m")
	reads := len(f.reads)
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog != nil || f.s.since != "30m" {
		t.Fatalf("enter does not apply: dialog %v since %q", f.s.dialog != nil, f.s.since)
	}
	if len(f.reads) != reads+1 {
		t.Fatalf("no re-read happened")
	}
	if l, _ := cardLine(f.s.workspace.Card, "since"); l.Value != "30m" {
		t.Fatalf("since = %q", l.Value)
	}
	if !strings.Contains(strings.Join(f.s.workspace.Card.Info, "\n"), "Showing the last 30m.") {
		t.Fatalf("card info %v misses the since note", f.s.workspace.Card.Info)
	}
	f.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	f.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if f.s.dialog != nil || f.s.since != "30m" {
		t.Fatalf("esc on an unchanged prompt does not cancel: dialog %v since %q", f.s.dialog != nil, f.s.since)
	}
	f.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	for i := 0; i < 3; i++ {
		f.key(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.since != "" {
		t.Fatalf("since = %q, want empty", f.s.since)
	}
	if l, _ := cardLine(f.s.workspace.Card, "since"); l.Value != "—" {
		t.Fatalf("since = %q", l.Value)
	}
	if !f.reads[len(f.reads)-1].Since.IsZero() {
		t.Fatalf("re-read kept a since window: %v", f.reads[len(f.reads)-1].Since)
	}
	f.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	logsTypeText(f, "soon")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt.Error == nil {
		t.Fatalf("invalid since is not reported")
	}
	verr := f.s.prompt.Error.Error()
	if !strings.Contains(verr, "duration") || strings.Contains(verr, "logs:") {
		t.Fatalf("error text = %q", verr)
	}
	promptCancelAfterTyping(f)
	if f.s.dialog != nil || f.s.since != "" {
		t.Fatalf("cancel leaves dialog %v since %q", f.s.dialog != nil, f.s.since)
	}
}

func TestLogsScreenTailPromptRealKeys(t *testing.T) {
	f := newLogsFixture(t, logsFiveLines)
	clearTailInput := func() {
		for i := 0; i < 3; i++ {
			f.key(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
	}
	f.key(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if f.s.dialog == nil {
		t.Fatalf("n does not open the tail prompt")
	}
	if v := ansi.Strip(f.s.View(80, 24)); !strings.Contains(v, "Logs — tail") {
		t.Fatalf("prompt view:\n%s", v)
	}
	clearTailInput()
	logsTypeText(f, "50")
	reads := len(f.reads)
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog != nil || f.s.tail != 50 {
		t.Fatalf("enter does not apply: dialog %v tail %d", f.s.dialog != nil, f.s.tail)
	}
	if len(f.reads) != reads+1 || f.reads[len(f.reads)-1].Tail != 50 {
		t.Fatalf("reads = %#v", f.reads)
	}
	if l, _ := cardLine(f.s.workspace.Card, "tail"); l.Value != "50" {
		t.Fatalf("tail = %q", l.Value)
	}
	f.key(tea.KeyPressMsg{Code: 'n', Text: "n"})
	clearTailInput()
	logsTypeText(f, "many")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt.Error == nil {
		t.Fatalf("a word is accepted as a tail")
	}
	if !strings.Contains(f.s.prompt.Error.Error(), "whole number") {
		t.Fatalf("error = %q", f.s.prompt.Error.Error())
	}
	promptCancelAfterTyping(f)
	if f.s.dialog != nil || f.s.tail != 50 {
		t.Fatalf("cancel leaves dialog %v tail %d", f.s.dialog != nil, f.s.tail)
	}
	f.key(tea.KeyPressMsg{Code: 'n', Text: "n"})
	clearTailInput()
	logsTypeText(f, "-3")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt.Error == nil {
		t.Fatalf("a negative tail is accepted")
	}
	if !strings.Contains(f.s.prompt.Error.Error(), "at least 0") {
		t.Fatalf("error = %q", f.s.prompt.Error.Error())
	}
	promptCancelAfterTyping(f)
	f.key(tea.KeyPressMsg{Code: 'n', Text: "n"})
	clearTailInput()
	logsTypeText(f, "0")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.tail != 0 {
		t.Fatalf("tail = %d, want 0", f.s.tail)
	}
	f.s.entries = []angel.LogEntry{
		{Time: logsNow.Add(-3 * time.Minute), Level: angel.LevelInfo, Message: "one"},
		{Time: logsNow.Add(-2 * time.Minute), Level: angel.LevelInfo, Message: "two"},
		{Time: logsNow.Add(-1 * time.Minute), Level: angel.LevelInfo, Message: "three"},
	}
	f.s.refresh()
	f.tick()
	if len(f.s.entries) != 3 {
		t.Fatalf("tail 0 trimmed to %d", len(f.s.entries))
	}
}

func TestLogsScreenScrollTurnsFollowOffAndOn(t *testing.T) {
	f := newLogsFixture(t, logsFiveLines)
	f.key(tea.KeyPressMsg{Code: tea.KeyUp})
	if f.s.follow {
		t.Fatalf("up keeps follow on")
	}
	if l, _ := cardLine(f.s.workspace.Card, "follow"); l.Value != "off" {
		t.Fatalf("follow = %q", l.Value)
	}
	if !strings.Contains(f.s.list.Title, "follow off") {
		t.Fatalf("title %q", f.s.list.Title)
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if f.s.follow {
		t.Fatalf("pgup turns follow back on")
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !f.s.follow || f.s.list.Selected != len(f.s.list.Rows)-1 {
		t.Fatalf("end does not return to the live tail: follow %v selected %d", f.s.follow, f.s.list.Selected)
	}
	f.key(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if f.s.follow {
		t.Fatalf("f does not toggle follow off")
	}
	f.key(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !f.s.follow {
		t.Fatalf("f does not toggle follow on")
	}
	f.wheelUp()
	if f.s.follow {
		t.Fatalf("the wheel does not turn follow off")
	}
}

func TestLogsScreenFollowPollAppendsAndPins(t *testing.T) {
	seed := "2026-11-05T11:00:00Z INFO one\n2026-11-05T11:01:00Z INFO two\n2026-11-05T11:02:00Z INFO three\n"
	f := newLogsFixture(t, seed)
	appendLogFile(t, f.s.home, "2026-11-05T12:01:00Z INFO four\n2026-11-05T12:02:00Z INFO five\n")
	f.tick()
	if len(f.s.list.Rows) != 5 || f.s.list.Selected != 4 || f.s.offset == 0 {
		t.Fatalf("after poll: rows %d selected %d offset %d",
			len(f.s.list.Rows), f.s.list.Selected, f.s.offset)
	}
	size := f.s.offset
	appendLogFile(t, f.s.home, "2026-11-05T12:03:00Z INFO six\n")
	f.tick()
	if len(f.s.list.Rows) != 6 || f.s.list.Selected != 5 || f.s.offset <= size {
		t.Fatalf("after second poll: rows %d selected %d offset %d",
			len(f.s.list.Rows), f.s.list.Selected, f.s.offset)
	}
}

func TestLogsScreenTailTrimsOldestOnPoll(t *testing.T) {
	seed := ""
	for i := 1; i <= 6; i++ {
		seed += "2026-11-05T11:0" + string(rune('0'+i)) + ":00Z INFO line " + string(rune('0'+i)) + "\n"
	}
	f := newLogsFixture(t, seed)
	f.s.tail = 3
	appendLogFile(t, f.s.home, "2026-11-05T11:07:00Z INFO line 7\n")
	f.tick()
	if len(f.s.list.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(f.s.list.Rows))
	}
	if got := f.s.list.Rows[0].Cells[2]; got != "line 5" {
		t.Fatalf("oldest kept = %q, want line 5", got)
	}
}

func TestLogsScreenErrorsShownByMessageOnly(t *testing.T) {
	f := newLogsFixture(t, logsSample)
	f.readErr = errors.New("read tcp 10.0.0.1:1: secret-token")
	f.key(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if f.s.notice != "Reading the log failed." {
		t.Fatalf("notice = %q", f.s.notice)
	}
	f.readErr = &cli.Error{Code: "INTERNAL", Message: "read log: boom", Hint: "check the directory", Exit: cli.ExitInternal}
	f.key(tea.KeyPressMsg{Code: 'l', Text: "l"})
	info := strings.Join(f.s.workspace.Card.Info, "\n")
	if !strings.Contains(info, "read log: boom (check the directory)") {
		t.Fatalf("card info %q", info)
	}
	sc, _ := f.s.Update(logsPollMsg{err: errors.New("disk on fire")})
	f.s = sc.(*logsScreen)
	if f.s.notice != "Reading the log failed." {
		t.Fatalf("poll notice = %q", f.s.notice)
	}
	sc, _ = f.s.Update(logsPollMsg{err: os.ErrNotExist})
	f.s = sc.(*logsScreen)
	if !f.s.missing {
		t.Fatalf("a missing-log poll must set missing")
	}
}

func TestLogsScreenFrameContract(t *testing.T) {
	logsFrameCheck := func(f *logsFixture, drilled bool) {
		t.Helper()
		f.s.workspace.Drilled = drilled
		for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
			v := ansi.Strip(f.s.View(size[0], size[1]))
			lines := strings.Split(v, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%dx%d drilled=%v: %d lines", size[0], size[1], drilled, len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > size[0] {
					t.Fatalf("%dx%d drilled=%v: line wider than the screen: %q", size[0], size[1], drilled, l)
				}
			}
		}
	}
	f := newLogsFixture(t, strings.Repeat("2026-11-05T11:00:00Z INFO the angel keeps writing lines\n", 30))
	logsFrameCheck(f, false)
	logsFrameCheck(f, true)
	f.key(tea.KeyPressMsg{Code: tea.KeyUp})
	logsFrameCheck(f, false)
	logsFrameCheck(f, true)
	missing := newLogsFixture(t, "")
	logsFrameCheck(missing, false)
	logsFrameCheck(missing, true)
	f.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(f.s.View(size[0], size[1]))
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("since prompt %dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatalf("since prompt %dx%d: line wider than the screen: %q", size[0], size[1], l)
			}
		}
	}
	promptCancelAfterTyping(f)
	f.key(tea.KeyPressMsg{Code: 'n', Text: "n"})
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(f.s.View(size[0], size[1]))
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("tail prompt %dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatalf("tail prompt %dx%d: line wider than the screen: %q", size[0], size[1], l)
			}
		}
	}
}

func TestLogsScreenParityRefs(t *testing.T) {
	f := newLogsFixture(t, logsSample)
	got := f.s.ParityRefs()
	want := []string{"logs/list", "logs/follow", "logs/level", "logs/since", "logs/tail", "logs/search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
}

func TestLogsScreenSmallCoverage(t *testing.T) {
	f := newLogsFixture(t, logsSample)
	if f.s.View(0, 0) != "" {
		t.Fatalf("empty view is not empty")
	}
	f.s.View(90, 24)
	f.s.workspace.Drilled = true
	if got := f.s.Breadcrumb(); got != "Logs / Log view — info and above" {
		t.Fatalf("breadcrumb = %q", got)
	}
	f.s.workspace.Drilled = false
	if f.s.FocusPane(false) {
		t.Fatalf("FocusPane works below 100 columns")
	}
	f.s.View(120, 40)
	f.s.SetPaneFocus(1)
	if f.s.workspace.Focus() != tui.PaneCard {
		t.Fatalf("SetPaneFocus(1) ignored at 120 columns")
	}
	if got := logsUsageText(&cli.Error{Message: "logs: X"}); got != "X" {
		t.Fatalf("logsUsageText = %q", got)
	}
	if got := logsUsageText(errors.New("raw")); got != "That value is not valid." {
		t.Fatalf("logsUsageText = %q", got)
	}
	if !f.s.FocusPane(true) || f.s.workspace.Focus() != tui.PaneList {
		t.Fatalf("FocusPane reverse at 120 columns")
	}
	if f.s.FocusPane(true) {
		t.Fatalf("FocusPane reverse from the list must hit the boundary")
	}
	sc, _ := f.s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	f.s = sc.(*logsScreen)
	sc, _ = f.s.Update(tui.ThemeMsg{Theme: tui.NewTheme()})
	f.s = sc.(*logsScreen)
	if nextLogLevel("") != angel.LevelInfo || nextLogLevel("nope") != angel.LevelWarn {
		t.Fatalf("nextLogLevel fallbacks")
	}
	if len(f.s.Bindings()) == 0 || !strings.Contains(f.s.KeyHints(), "f follow") {
		t.Fatalf("bindings or key hints are empty")
	}
	entries, _, err := f.s.pollStep(f.s.home, 0, angel.LogQuery{})
	if err != nil || len(entries) == 0 {
		t.Fatalf("pollStep: %d entries, err %v", len(entries), err)
	}
	selected := f.s.list.Selected
	f.s.list.Filter = "x"
	f.s.pinBottom()
	if f.s.list.Selected != selected {
		t.Fatalf("pinBottom moved a filtered selection")
	}
	f.s.list.Filter = ""
	f.s.lastFilter = ""
	card := f.s.workspace.Card
	for i, l := range card.Lines {
		if l.ID == "follow" {
			card.Selected = i
		}
	}
	f.s.activateCard()
	if l, _ := cardLine(f.s.workspace.Card, "follow"); l.Value != "off" {
		t.Fatalf("activate follow = %q", l.Value)
	}
	for i, l := range f.s.workspace.Card.Lines {
		if l.ID == "level" {
			f.s.workspace.Card.Selected = i
		}
	}
	f.s.activateCard()
	if l, _ := cardLine(f.s.workspace.Card, "level"); l.Value != "warn" {
		t.Fatalf("activate level = %q", l.Value)
	}
	for i, l := range f.s.workspace.Card.Lines {
		if l.ID == "since" {
			f.s.workspace.Card.Selected = i
		}
	}
	f.s.activateCard()
	if f.s.dialog == nil {
		t.Fatalf("activate since does not open the prompt")
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	for i, l := range f.s.workspace.Card.Lines {
		if l.ID == "tail" {
			f.s.workspace.Card.Selected = i
		}
	}
	f.s.activateCard()
	if f.s.dialog == nil {
		t.Fatalf("activate tail does not open the prompt")
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	for i, l := range f.s.workspace.Card.Lines {
		if l.ID == "search" {
			f.s.workspace.Card.Selected = i
		}
	}
	f.s.activateCard()
	if !f.s.list.Filtering {
		t.Fatalf("activate search does not start the filter")
	}
	f.s.list.EndFilter(false)
}
