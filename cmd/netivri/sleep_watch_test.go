package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
)

func TestSleepWatchNoticesALongGapOfTheWallClock(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	w := &sleepWatch{now: func() time.Time { return t0 }, every: 5 * time.Second, gap: 30 * time.Second, grace: 2 * time.Minute}
	if _, ok := w.note(t0, t0.Add(5*time.Second)); ok {
		t.Fatal("a normal check period counted as sleep")
	}
	if _, ok := w.note(t0, t0.Add(35*time.Second)); ok {
		t.Fatal("a busy machine (35 s between checks) counted as sleep")
	}
	if w.quiet() {
		t.Fatal("quiet without a sleep")
	}
	slept, ok := w.note(t0, t0.Add(26*time.Hour+22*time.Minute+400*time.Millisecond))
	if !ok || slept != 26*time.Hour+22*time.Minute {
		t.Fatalf("note = %v, %v; want 26h22m, true", slept, ok)
	}
	wake := t0.Add(26*time.Hour + 22*time.Minute)
	t0 = wake.Add(time.Minute)
	if !w.quiet() {
		t.Error("not quiet a minute after waking up")
	}
	t0 = wake.Add(3 * time.Minute)
	if w.quiet() {
		t.Error("still quiet 3 minutes after waking up")
	}
	// The clock going backwards is not a sleep.
	if _, ok := w.note(t0, t0.Add(-time.Hour)); ok {
		t.Error("a clock set back counted as sleep")
	}
}

func TestSleepWatchIsNeverQuietWhenNil(t *testing.T) {
	var w *sleepWatch
	if w.quiet() {
		t.Fatal("a nil watch is quiet")
	}
}

// run reports the jump of the wall clock once and keeps watching after it.
func TestSleepWatchRunReportsOneWake(t *testing.T) {
	var mu sync.Mutex
	at := time.Unix(1_000_000, 0)
	calls := 0
	every := 5 * time.Millisecond
	w := &sleepWatch{every: every, gap: 30 * time.Second, grace: time.Minute, now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		at = at.Add(every)
		if calls == 4 { // the third tick: the computer slept for an hour
			at = at.Add(time.Hour)
		}
		return at
	}}
	ctx, cancel := context.WithCancel(context.Background())
	woke := make(chan time.Duration, 4)
	done := make(chan struct{})
	go func() { w.run(ctx, func(d time.Duration) { woke <- d }); close(done) }()
	select {
	case d := <-woke:
		if d < time.Hour || d > time.Hour+time.Minute {
			t.Errorf("slept %v, want about an hour", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sleep was not reported")
	}
	for i := 0; i < 20; i++ { // more ticks, no more wakes
		time.Sleep(every)
	}
	cancel()
	<-done
	if len(woke) != 0 {
		t.Errorf("%d more wakes reported", len(woke))
	}
}

func TestWokeLogsTheSleepAndProbesTheNodesAgain(t *testing.T) {
	var lines []string
	l := newLiveAngel(upOptions{}, func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }, func() {})
	f := &fakeCore{}
	l.inst = f
	l.woke(26*time.Hour + 22*time.Minute)
	if len(lines) != 1 || !strings.Contains(lines[0], "the computer was asleep for 26h22m0s") || !strings.Contains(lines[0], "in the next 2 minutes") {
		t.Fatalf("lines = %q", lines)
	}
	if f.probed != 1 {
		t.Errorf("ProbeNow called %d times, want 1", f.probed)
	}
	l.inst = nil
	l.woke(time.Hour) // no core running: nothing to probe, nothing to fail
}

func TestAfterWakeTheConnectionLogDoesNotWarn(t *testing.T) {
	var infos, warns []string
	l := newLiveAngel(upOptions{}, func(format string, args ...any) { infos = append(infos, fmt.Sprintf(format, args...)) }, func() {})
	l.warnf = func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }
	feed := func() {
		for i := 0; i < 5; i++ {
			l.connLog().event(core.ConnEvent{Kind: core.ConnDialFailed, Login: "alice", Label: "main/us", Why: "timeout"})
		}
		l.connLog().flush()
	}
	feed()
	if len(warns) != 1 || len(infos) != 0 {
		t.Fatalf("awake: warns %q infos %q, want one warning", warns, infos)
	}
	warns = nil
	now := time.Now()
	l.sleep().note(now.Add(-time.Hour), now) // woke up just now
	feed()
	if len(warns) != 0 || len(infos) != 1 || !strings.Contains(infos[0], "5 dials failed") {
		t.Fatalf("after wake: warns %q infos %q, want one INFO line", warns, infos)
	}
}

// Right after the computer woke up every node fails to answer: the downs are
// INFO lines, not a burst and not flapping, and the log is normal again after.
func TestNodeLogRightAfterWakingUpIsQuiet(t *testing.T) {
	f := newBurstFixture(t, 8)
	quiet := true
	f.log.quiet = func() bool { return quiet }
	for round := 0; round < 4; round++ { // the same nodes drop four times: no flapping either
		for i := 0; i < 8; i++ {
			f.down(i, "2 probe failures (timeout)")
		}
		f.now = f.now.Add(time.Second)
	}
	if got := f.count("WARN "); got != 0 {
		t.Fatalf("%d warnings while quiet: %q", got, f.lines)
	}
	if got := f.countContaining("right after the computer woke up"); got != 32 {
		t.Errorf("%d lines say why, want 32: %q", got, f.lines)
	}
	f.lines = nil
	quiet = false
	f.now = f.now.Add(time.Minute)
	f.down(0, "2 probe failures (timeout)")
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], "WARN node p/n0 ") || strings.Contains(f.lines[0], "woke") {
		t.Fatalf("after the quiet time lines = %q, want a plain warning (the quiet downs did not count toward flapping)", f.lines)
	}
}

func TestConnLogMovesBetweenManyPairsAreOneLine(t *testing.T) {
	f := newConnLogFixture()
	var detail []string
	f.log.debug = func(format string, args ...any) { detail = append(detail, fmt.Sprintf(format, args...)) }
	move := func(login, from, to, why string, n int) {
		for i := 0; i < n; i++ {
			f.log.event(core.ConnEvent{Kind: core.ConnSiteMoved, Login: login, From: from, Label: to, Why: why})
		}
	}
	move("alice", "main/us", "own/nl", core.MoveNodeDown, 7)
	move("alice", "main/us", "own/de", core.MoveNodeDown, 3)
	move("alice", "own/fr", "own/nl", core.MoveNodeDown, 1)
	move("alice", "own/de", "own/nl", core.MoveNodeDown, 1)
	move("alice", "own/nl", "main/us", core.MoveBetterTier, 2)
	move("bob", "own/de", "own/nl", core.MoveNodeDown, 1) // one pair of another account stays its own line
	f.log.flush()
	want := []string{
		"WARN account alice: 12 sites moved from 3 nodes to 2 nodes: node down",
		"INFO account alice: 2 sites moved from own/nl (country NL) to main/us (United States, country US): better tier",
		"WARN account bob: 1 site moved from own/de to own/nl (country NL): node down",
	}
	if strings.Join(f.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(f.lines, "\n"), strings.Join(want, "\n"))
	}
	if len(detail) != 4 || !strings.Contains(detail[0], "3 sites moved from main/us") {
		t.Errorf("debug detail = %q, want the four pairs of alice's node-down moves", detail)
	}
}

func TestConnLogWhenQuietKeepsWarningsAsInfo(t *testing.T) {
	f := newConnLogFixture()
	f.log.quiet = func() bool { return true }
	f.dialFail("alice", "main/us", "timeout", 11)
	f.ended("alice", "main/us", "timeout", 10, 50)
	for i := 0; i < 4; i++ {
		f.log.event(core.ConnEvent{Kind: core.ConnSiteMoved, Login: "alice", From: "main/us", Label: "own/nl", Why: core.MoveNodeDown})
	}
	f.log.flush()
	if len(f.lines) != 3 {
		t.Fatalf("lines = %q, want 3", f.lines)
	}
	for _, line := range f.lines {
		if !strings.HasPrefix(line, "INFO ") {
			t.Errorf("not INFO while quiet: %q", line)
		}
	}
	// A failed proxy login is a different matter: it is still a warning.
	f.lines = nil
	f.log.loginFailed("alice", true)
	f.log.flush()
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], "WARN ") {
		t.Errorf("lines = %q, want the failed login as WARN", f.lines)
	}
}

// The hook that the core is started with is the one that goes quiet after a
// wake-up: the watch of the angel and the node log are wired together.
func TestCoreOptionsHookIsQuietRightAfterWaking(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.l.mu.Lock()
	label := f.l.applied.pool.infos[0].Label
	f.l.mu.Unlock()
	hook := f.script.call(0).OnNodeHealth
	hook(core.NodeEvent{Label: label, Down: true, Reason: "2 probe failures (timeout)"})
	if strings.Contains(f.logText(), "woke up") {
		t.Fatalf("quiet without a sleep: %q", f.logText())
	}
	now := time.Now()
	f.l.sleep().note(now.Add(-time.Hour), now)
	hook(core.NodeEvent{Label: label, Down: false, Downtime: time.Second})
	hook(core.NodeEvent{Label: label, Down: true, Reason: "2 probe failures (timeout)"})
	if !strings.Contains(f.logText(), "right after the computer woke up") {
		t.Errorf("a down right after waking is not marked: %q", f.logText())
	}
}
