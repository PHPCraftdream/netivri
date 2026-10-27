package core

import (
	"sync"
	"testing"
	"time"
)

type healthEvent struct {
	label string
	down  bool
}

func notifyingTable(labels ...string) (*healthTable, func() []healthEvent) {
	var mu sync.Mutex
	var events []healthEvent
	table := newHealthTable()
	table.notify = func(ev NodeEvent) {
		mu.Lock()
		events = append(events, healthEvent{ev.Label, ev.Down})
		mu.Unlock()
	}
	table.register(labels)
	return table, func() []healthEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]healthEvent(nil), events...)
	}
}

func TestHealthNotifiesWhenAProbeFailureStreakTakesTheNodeDown(t *testing.T) {
	table, events := notifyingTable("p/a")
	table.recordProbe("p/a", false, 0)
	if got := events(); len(got) != 0 {
		t.Fatalf("one failure announced %v", got)
	}
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0) // already down: nothing new to say
	if got := events(); len(got) != 1 || got[0] != (healthEvent{"p/a", true}) {
		t.Fatalf("events after the streak = %v, want one down", got)
	}
	table.recordProbe("p/a", true, 10*time.Millisecond)
	table.recordProbe("p/a", true, 10*time.Millisecond)
	if got := events(); len(got) != 2 || got[1] != (healthEvent{"p/a", false}) {
		t.Fatalf("events after recovery = %v, want down then up", got)
	}
}

func TestHealthNotifiesOnDialFailuresAndRecovery(t *testing.T) {
	table, events := notifyingTable("p/a", "p/b")
	for i := 0; i < downDialFailures-1; i++ {
		table.recordDial("p/a", false, 0)
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("too few failures announced %v", got)
	}
	table.recordDial("p/a", false, 0)
	table.recordDial("p/a", true, time.Millisecond)
	got := events()
	if len(got) != 2 || got[0] != (healthEvent{"p/a", true}) || got[1] != (healthEvent{"p/a", false}) {
		t.Fatalf("events = %v", got)
	}
}

func TestHealthNotifiesPerNode(t *testing.T) {
	table, events := notifyingTable("p/a", "p/b")
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/b", false, 0)
	table.recordProbe("p/b", false, 0)
	got := events()
	if len(got) != 1 || got[0] != (healthEvent{"p/b", true}) {
		t.Fatalf("events = %v, want only p/b down", got)
	}
}

func TestHealthWithoutNotifyKeepsWorking(t *testing.T) {
	table := newHealthTable()
	table.register([]string{"p/a"})
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	if !table.down("p/a") {
		t.Fatal("the node is not down after two failed probes")
	}
	table.recordDial("p/a", true, time.Millisecond)
	if table.down("p/a") {
		t.Fatal("the node is still down after a successful dial")
	}
}

func TestHealthNotifyRunsWithoutTheNodeLock(t *testing.T) {
	table := newHealthTable()
	table.register([]string{"p/a"})
	h := table.health("p/a")
	done := make(chan struct{})
	table.entries["p/a"].notify = func(ev NodeEvent) {
		// A listener may read the node's health: the lock must be free.
		_ = h.view(ev.Label)
		close(done)
	}
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener deadlocked on the node lock")
	}
}
