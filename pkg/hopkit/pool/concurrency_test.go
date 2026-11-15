package pool

import (
	"sync"
	"testing"
	"time"
)

func TestConcurrentDueProbeCallsLeaseOneClaimPerHop(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "a", "b", "c")
	for _, key := range []string{"a", "b", "c"} {
		for range 3 {
			p.RecordHard(key)
		}
	}
	now := testEpoch.Add(30 * time.Second)
	clock.Set(now)

	const callers = 32
	start := make(chan struct{})
	claims := make(chan ProbeClaim, callers*3)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, claim := range p.DueProbes(now) {
				claims <- claim
			}
		}()
	}
	close(start)
	wg.Wait()
	close(claims)

	counts := make(map[string]int)
	for claim := range claims {
		counts[claim.Key()]++
	}
	if len(counts) != 3 {
		t.Fatalf("leased keys = %#v, want all three", counts)
	}
	for _, key := range []string{"a", "b", "c"} {
		if counts[key] != 1 {
			t.Errorf("concurrent DueProbes leased %q %d times, want once", key, counts[key])
		}
	}
}

func TestConcurrentPoolOperationsPreserveMembershipAndSnapshotInvariants(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "a", "b", "c")
	for range 3 {
		p.RecordHard("a")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	const workers = 8
	const iterations = 150
	outcomes := [...]ProbeOutcome{ProbeSuccess, ProbeHardFailure, ProbeInconclusive}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range iterations {
				key := []string{"a", "b", "c"}[(worker+i)%3]
				now := clock.Now()
				applyConcurrentOp(t, p, (worker+i)%9, key, now, outcomes[(worker+i)/9%3])
				checkConcurrentSnapshot(t, p)
			}
		}()
	}
	close(start)
	wg.Wait()
}

// applyConcurrentOp runs one of the concurrently exercised pool operations, selected by op.
func applyConcurrentOp(t *testing.T, p *Pool, op int, key string, now time.Time, outcome ProbeOutcome) {
	t.Helper()
	switch op {
	case 0:
		checkConcurrentOrder(t, p.Order(now))
	case 1:
		p.RecordHard(key)
	case 2:
		p.RecordSoft(key)
	case 3:
		p.RecordSuccess(key)
	case 4:
		for _, claim := range p.DueProbes(now) {
			p.ReportProbe(claim, outcome)
		}
	case 5:
		_ = p.Snapshot()
	case 6:
		_ = p.Export()
	case 7:
		p.Restore([]Entry{{Key: "a", Trips: 2, NextProbe: now.Add(time.Hour)}}, now)
	default:
		p.Reconcile([]string{"a", "b", "c", "a"})
	}
}

// checkConcurrentOrder checks that an order lists each known key at most once.
func checkConcurrentOrder(t *testing.T, order []string) {
	t.Helper()
	if len(order) == 0 || len(order) > 3 {
		t.Errorf("concurrent order length = %d: %v", len(order), order)
	}
	seenOrder := make(map[string]bool, len(order))
	for _, orderedKey := range order {
		if orderedKey != "a" && orderedKey != "b" && orderedKey != "c" {
			t.Errorf("order returned unknown key %q", orderedKey)
		}
		if seenOrder[orderedKey] {
			t.Errorf("order duplicated key %q: %v", orderedKey, order)
		}
		seenOrder[orderedKey] = true
	}
}

// checkConcurrentSnapshot checks that the snapshot holds each key once with valid counters.
func checkConcurrentSnapshot(t *testing.T, p *Pool) {
	t.Helper()
	states := p.Snapshot()
	if len(states) != 3 {
		t.Errorf("concurrent snapshot length = %d, want 3: %#v", len(states), states)
		return
	}
	seenKeys := make(map[string]bool, len(states))
	for _, state := range states {
		if state.Key != "a" && state.Key != "b" && state.Key != "c" {
			t.Errorf("concurrent snapshot returned unknown key %q", state.Key)
		}
		if seenKeys[state.Key] {
			t.Errorf("concurrent snapshot duplicated key %q", state.Key)
		}
		seenKeys[state.Key] = true
		if state.Sand < 0 || state.Sand > 8 || state.Weight < 0.05 || state.Weight > 1 {
			t.Errorf("invalid concurrent sand/weight for %q: %#v", state.Key, state)
		}
		if state.HardFailures < 0 || state.HardFailures > 3 || state.Trips < 0 {
			t.Errorf("invalid concurrent counters for %q: %#v", state.Key, state)
		}
		if state.State != Healthy && state.State != Quarantined && state.State != Probing {
			t.Errorf("invalid concurrent state for %q: %#v", state.Key, state)
		}
	}
}
