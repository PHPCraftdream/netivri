package main

import (
	"strings"
	"testing"
	"time"
)

func TestSleepWatchAsleepCountsTheOverlapWithEachSleep(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	w := &sleepWatch{now: func() time.Time { return t0 }, every: 5 * time.Second, gap: 30 * time.Second, grace: 2 * time.Minute}
	if got := w.asleep(t0, t0.Add(time.Hour)); got != 0 {
		t.Fatalf("asleep before any sleep = %v", got)
	}
	// one sleep: the wall clock read t0 and then t0+25h
	if _, ok := w.note(t0, t0.Add(25*time.Hour)); !ok {
		t.Fatal("the sleep was not noticed")
	}
	// a second one an hour after waking up: 2h long
	second := t0.Add(26 * time.Hour)
	if _, ok := w.note(second, second.Add(2*time.Hour)); !ok {
		t.Fatal("the second sleep was not noticed")
	}
	cases := []struct {
		name     string
		from, to time.Time
		want     time.Duration
	}{
		{"around both", t0.Add(-time.Hour), t0.Add(40 * time.Hour), 27 * time.Hour},
		{"only the first", t0.Add(-time.Hour), t0.Add(25*time.Hour + 30*time.Minute), 25 * time.Hour},
		{"the tail of the first", t0.Add(10 * time.Hour), t0.Add(26 * time.Hour), 15 * time.Hour},
		{"between them", t0.Add(25*time.Hour + time.Minute), t0.Add(25*time.Hour + 59*time.Minute), 0},
		{"inside the second", second.Add(30 * time.Minute), second.Add(time.Hour), 30 * time.Minute},
		{"after both", t0.Add(30 * time.Hour), t0.Add(31 * time.Hour), 0},
		{"empty span", t0.Add(time.Hour), t0.Add(time.Hour), 0},
	}
	for _, c := range cases {
		if got := w.asleep(c.from, c.to); got != c.want {
			t.Errorf("%s: asleep = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSleepWatchAsleepNilWatchSleptNothing(t *testing.T) {
	var w *sleepWatch
	if got := w.asleep(time.Unix(0, 0), time.Unix(1000, 0)); got != 0 {
		t.Fatalf("asleep = %v", got)
	}
}

func TestSleepWatchKeepsTheNewestSleepsOnly(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	w := &sleepWatch{now: func() time.Time { return t0 }, every: 5 * time.Second, gap: 30 * time.Second, grace: 2 * time.Minute}
	at := t0
	for i := 0; i < sleepSpansKept+8; i++ {
		if _, ok := w.note(at, at.Add(time.Hour)); !ok {
			t.Fatal("a sleep was not noticed")
		}
		at = at.Add(2 * time.Hour)
	}
	if len(w.spans) != sleepSpansKept {
		t.Fatalf("kept %d sleeps, want %d", len(w.spans), sleepSpansKept)
	}
	if got := w.asleep(t0, t0.Add(time.Hour)); got != 0 {
		t.Errorf("the oldest sleep is still counted: %v", got)
	}
	last := t0.Add(time.Duration(sleepSpansKept+7) * 2 * time.Hour)
	if got := w.asleep(last, last.Add(time.Hour)); got != time.Hour {
		t.Errorf("the newest sleep = %v, want 1h", got)
	}
}

// sleeping returns an asleep function that reports slept for any span and
// records the span it was asked about.
func sleeping(slept time.Duration, asked *[2]time.Time) func(from, to time.Time) time.Duration {
	return func(from, to time.Time) time.Duration {
		*asked = [2]time.Time{from, to}
		return slept
	}
}

func TestNodeLogBackDoesNotCountTheSleepOfTheDowntime(t *testing.T) {
	f := newBurstFixture(t, 2)
	var asked [2]time.Time
	f.log.asleep = sleeping(25*time.Hour+46*time.Minute+52*time.Second, &asked)
	f.down(0, "2 probe failures (timeout)")
	f.now = f.now.Add(31*time.Hour + 57*time.Minute + 37*time.Second)
	f.back(0, 31*time.Hour+57*time.Minute+37*time.Second)

	want := "INFO node p/n0 (provider p, country DE) is back after 6h10m45s down, not counting 25h46m52s asleep"
	if got := f.lines[len(f.lines)-1]; got != want {
		t.Fatalf("back line = %q, want %q", got, want)
	}
	if !asked[1].Equal(f.now) || asked[1].Sub(asked[0]) != 31*time.Hour+57*time.Minute+37*time.Second {
		t.Fatalf("asked about %v..%v, want the whole downtime ending now", asked[0], asked[1])
	}
}

func TestNodeLogBackWithoutSleepKeepsTheLine(t *testing.T) {
	f := newBurstFixture(t, 2)
	f.log.asleep = func(time.Time, time.Time) time.Duration { return 0 }
	f.down(0, "2 probe failures (timeout)")
	f.back(0, 41*time.Second)
	if got, want := f.lines[len(f.lines)-1], "INFO node p/n0 (provider p, country DE) is back after 41s down"; got != want {
		t.Fatalf("back line = %q, want %q", got, want)
	}
}

func TestNodeLogBackNeverShowsANegativeAwakeTime(t *testing.T) {
	f := newBurstFixture(t, 2)
	var asked [2]time.Time
	f.log.asleep = sleeping(2*time.Hour, &asked) // a sleep span wider than the downtime
	f.down(0, "2 probe failures (timeout)")
	f.back(0, time.Hour)
	want := "INFO node p/n0 (provider p, country DE) is back after 0s down, not counting 2h0m0s asleep"
	if got := f.lines[len(f.lines)-1]; got != want {
		t.Fatalf("back line = %q, want %q", got, want)
	}
}

func TestNodeLogBurstOverDoesNotCountTheSleep(t *testing.T) {
	f := newBurstFixture(t, 6)
	var asked [2]time.Time
	f.log.asleep = sleeping(25*time.Hour, &asked)
	for i := 0; i < 4; i++ {
		f.down(i, "2 probe failures (timeout)")
	}
	f.now = f.now.Add(30 * time.Hour)
	for i := 0; i < 4; i++ {
		f.back(i, 30*time.Hour)
	}
	want := "INFO node burst over: 4 nodes were down, all back after 5h0m0s, not counting 25h0m0s asleep"
	if got := f.lines[len(f.lines)-1]; got != want {
		t.Fatalf("last line = %q, want %q", got, want)
	}
}

func TestNodeLogOutageOverDoesNotCountTheSleep(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	var asked [2]time.Time
	f.log.asleep = sleeping(3*time.Hour, &asked)
	f.slowly(0, 9) // an outage of nine of twelve servers
	for i := 0; i < 6; i++ {
		f.back(i, time.Hour)
	}
	f.now = f.now.Add(10 * time.Hour)
	f.back(6, time.Hour) // the seventh back leaves two of twelve down: the outage ends
	last := f.lines[len(f.lines)-1]
	if !strings.HasPrefix(last, "INFO node outage over: ") {
		t.Fatalf("no outage end: %q", f.lines)
	}
	if !strings.HasSuffix(last, ", not counting 3h0m0s asleep") {
		t.Fatalf("last line = %q, want it to end with the sleep note", last)
	}
}
