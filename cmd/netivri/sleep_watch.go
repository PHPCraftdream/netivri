package main

import (
	"context"
	"sync"
	"time"
)

const (
	// sleepCheckEvery is how often the wall clock is compared with the ticker.
	sleepCheckEvery = 5 * time.Second
	// sleepGap is how much longer than a check period the wall clock must have
	// moved to count as the computer having slept.
	sleepGap = 30 * time.Second
	// wakeGrace is how long after waking up the failures of nodes and
	// connections are expected (the network is not back at once) and are not
	// warnings.
	wakeGrace = 2 * time.Minute
	// sleepSpansKept is how many sleeps are remembered, for the lines that
	// tell how long something lasted (see asleep).
	sleepSpansKept = 32
)

// sleepWatch notices that the computer slept (or its clock jumped forward): a
// ticker that fires every sleepCheckEvery of running time sees the wall clock
// move by much more. The angel is frozen while the computer sleeps, so nothing
// in its log tells the hours of sleep from hours of silence.
type sleepWatch struct {
	now   func() time.Time
	every time.Duration
	gap   time.Duration
	grace time.Duration

	mu    sync.Mutex
	until time.Time // failures are expected until then
	spans []sleepSpan
}

// sleepSpan is one noticed sleep: the two readings of the wall clock around it.
type sleepSpan struct{ from, to time.Time }

func newSleepWatch() *sleepWatch {
	return &sleepWatch{now: time.Now, every: sleepCheckEvery, gap: sleepGap, grace: wakeGrace}
}

// run calls onWake with the length of the sleep each time one is noticed, until
// ctx ends.
func (w *sleepWatch) run(ctx context.Context, onWake func(slept time.Duration)) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	last := w.now().Round(0) // Round(0) drops the monotonic reading: the wall clock is what is compared
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur := w.now().Round(0)
		if slept, ok := w.note(last, cur); ok {
			onWake(slept)
		}
		last = cur
	}
}

// note compares two readings of the wall clock one check period apart and, when
// they are further apart than that by more than the gap, starts the grace
// period and returns how long the computer was gone.
func (w *sleepWatch) note(last, cur time.Time) (time.Duration, bool) {
	gone := cur.Sub(last)
	if gone-w.every <= w.gap {
		return 0, false
	}
	w.mu.Lock()
	w.until = cur.Add(w.grace)
	w.spans = append(w.spans, sleepSpan{last, cur})
	if len(w.spans) > sleepSpansKept {
		w.spans = w.spans[len(w.spans)-sleepSpansKept:]
	}
	w.mu.Unlock()
	return gone.Round(time.Second), true
}

// asleep is how much of the time from..to the computer spent asleep, by the
// sleeps the watch noticed. A nil watch noticed none.
func (w *sleepWatch) asleep(from, to time.Time) time.Duration {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var total time.Duration
	for _, s := range w.spans {
		start, end := s.from, s.to
		if from.After(start) {
			start = from
		}
		if to.Before(end) {
			end = to
		}
		if end.After(start) {
			total += end.Sub(start)
		}
	}
	return total
}

// quiet reports whether the angel woke up a moment ago and failures are
// expected. A nil watch is never quiet.
func (w *sleepWatch) quiet() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now().Before(w.until)
}
