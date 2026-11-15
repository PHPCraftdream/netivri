package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// sleepBeat is the start of the tally in the tests below.
var sleepBeat = time.Date(2026, 11, 13, 12, 50, 18, 0, time.UTC)

// beatAt makes the fixture's hourly tally begin at sleepBeat and the clock read
// sleepBeat plus after.
func (f *connLogFixture) beatAt(after time.Duration) {
	f.log.beatSince = sleepBeat
	f.log.now = func() time.Time { return sleepBeat.Add(after) }
}

func TestConnLogHourlySummaryNamesTheSleepItCovers(t *testing.T) {
	f := newConnLogFixture()
	f.beatAt(26*time.Hour + 46*time.Minute)
	var asked [2]time.Time
	f.log.asleep = sleeping(26*time.Hour+21*time.Minute+38*time.Second, &asked)
	f.endedUp("alice", 300, 900, 5)
	f.log.heartbeat()
	want := "INFO account alice: since 2026-11-13T12:50:18Z, not counting 26h21m38s asleep: 5 connections ended, 0 broken"
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], want) {
		t.Fatalf("lines = %q, want a line starting %q", f.lines, want)
	}
	if !asked[0].Equal(sleepBeat) || !asked[1].Equal(sleepBeat.Add(26*time.Hour+46*time.Minute)) {
		t.Errorf("the sleep was asked for %v, want the span of the tally", asked)
	}
	// The next tally begins where this one ended and, without a sleep, is an hour.
	f.lines = nil
	f.log.asleep = nil
	f.endedUp("alice", 300, 900, 2)
	f.log.heartbeat()
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], "INFO account alice: last hour: 2 connections ended") {
		t.Errorf("lines after the sleep = %q", f.lines)
	}
}

func TestConnLogHourlySummaryTellsASleepTheWatchHasNotSeenYet(t *testing.T) {
	f := newConnLogFixture()
	f.beatAt(26*time.Hour + 46*time.Minute) // the tick comes at the wake, before the watch notices it
	f.endedUp("alice", 300, 900, 3)
	f.log.heartbeat()
	want := "INFO account alice: since 2026-11-13T12:50:18Z, the computer slept in between: 3 connections ended"
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], want) {
		t.Fatalf("lines = %q, want a line starting %q", f.lines, want)
	}
}

func TestConnLogHourlySummaryOfAnOrdinaryHourIsLastHour(t *testing.T) {
	for name, tc := range map[string]struct {
		after  time.Duration
		asleep time.Duration
	}{
		"on time":                    {time.Hour, 0},
		"a little late":              {time.Hour + sleepGap, 0},
		"a short sleep is not named": {time.Hour, sleepGap},
	} {
		f := newConnLogFixture()
		f.beatAt(tc.after)
		var asked [2]time.Time
		f.log.asleep = sleeping(tc.asleep, &asked)
		f.endedUp("alice", 300, 900, 1)
		f.log.heartbeat()
		if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], "INFO account alice: last hour: 1 connection ended") {
			t.Errorf("%s: lines = %q", name, f.lines)
		}
	}
}

func TestConnLogRunTakesTheBeatFromItsArgument(t *testing.T) {
	f := newConnLogFixture()
	if f.log.beatEvery != connBeatEvery {
		t.Fatalf("beatEvery = %v, want %v by default", f.log.beatEvery, connBeatEvery)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.log.run(ctx, time.Hour, 3*time.Hour, nil)
	if f.log.beatEvery != 3*time.Hour {
		t.Errorf("beatEvery = %v, want 3h", f.log.beatEvery)
	}
	// A tally that is three hours long on a three-hour beat is no sleep.
	f.beatAt(3 * time.Hour)
	f.endedUp("alice", 300, 900, 1)
	f.log.heartbeat()
	if len(f.lines) != 1 || !strings.HasPrefix(f.lines[0], "INFO account alice: last hour: 1 connection ended") {
		t.Errorf("lines = %q", f.lines)
	}
}

func TestLiveConnLogKnowsTheSleepsOfTheWatch(t *testing.T) {
	l := &liveAngel{}
	c := l.connLog()
	if c.asleep == nil {
		t.Fatal("the connection log has no sleep source")
	}
	t0 := time.Now()
	w := l.sleep()
	w.spans = append(w.spans, sleepSpan{t0, t0.Add(time.Hour)})
	if got := c.asleep(t0, t0.Add(2*time.Hour)); got != time.Hour {
		t.Errorf("asleep = %v, want the 1h the watch noticed", got)
	}
}
