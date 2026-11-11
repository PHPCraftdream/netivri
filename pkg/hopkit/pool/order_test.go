package pool

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestOrderHidesQuarantinedAndProbingWhenHealthyExists(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "healthy-a", "quarantined", "soft-only", "healthy-b")
	p.RecordSoft("soft-only")
	for i := 0; i < 3; i++ {
		p.RecordHard("quarantined")
	}
	preDueOrder := p.Order(clock.Now())
	preDueSeen := requireUniqueStrings(t, preDueOrder)
	if len(preDueOrder) != 3 || !preDueSeen["healthy-a"] || !preDueSeen["healthy-b"] || !preDueSeen["soft-only"] || preDueSeen["quarantined"] {
		t.Fatalf("quarantined hop entered order before its probe was due: %v", preDueOrder)
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "quarantined")

	order := p.Order(clock.Now())
	seen := requireUniqueStrings(t, order)
	if len(order) != 3 || !seen["healthy-a"] || !seen["healthy-b"] || !seen["soft-only"] {
		t.Fatalf("healthy order should include healthy and soft-only hops, but omit quarantine/probing: %v", order)
	}
	if seen[claim.Key()] {
		t.Fatalf("probing hop %q leaked into healthy order: %v", claim.Key(), order)
	}
}

func TestExpiredProbationStaysOutOfCommonOrderUntilSuccess(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop", "other")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	clock.Set(testEpoch.Add(55 * time.Second))

	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Quarantined {
		t.Fatalf("expired lease state = %q, want quarantined", snapshot.State)
	}
	order := p.Order(clock.Now())
	if seen := requireUniqueStrings(t, order); len(order) != 1 || !seen["other"] || seen["hop"] {
		t.Fatalf("expired probation entered common order without success: %v", order)
	}
	if p.ReportProbe(claim, ProbeSuccess) {
		t.Fatal("expired claim was accepted")
	}
	p.RecordSuccess("hop")
	order = p.Order(clock.Now())
	seen := requireUniqueStrings(t, order)
	if len(order) != 2 || !seen["hop"] || !seen["other"] {
		t.Fatalf("successful hop did not return to common order: %v", order)
	}
}

func TestAllQuarantinedFallbackIncludesEveryKeyWithoutDuplicates(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "a", "b", "c", "d")
	for _, key := range []string{"a", "b", "c", "d"} {
		for i := 0; i < 3; i++ {
			p.RecordHard(key)
		}
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claims := p.DueProbes(clock.Now())
	if len(claims) != 4 {
		t.Fatalf("due claims = %d, want 4", len(claims))
	}
	fallback := p.Order(clock.Now())
	seen := requireUniqueStrings(t, fallback)
	if len(fallback) != 4 {
		t.Fatalf("all-quarantined fallback has %d hops, want 4: %v", len(fallback), fallback)
	}
	for _, key := range []string{"a", "b", "c", "d"} {
		if !seen[key] {
			t.Errorf("fallback omitted %q: %v", key, fallback)
		}
	}
}

func TestOrderIsReproducibleAndCopiesCallerPCG(t *testing.T) {
	clock := newTestClock()
	seed1, seed2 := uint64(721), uint64(991)
	caller := rand.NewPCG(seed1, seed2)
	wantCallerNext := rand.NewPCG(seed1, seed2).Uint64()
	first := New([]string{"a", "b", "c"}, Options{Clock: clock.Now, RNG: caller})
	second := newTestPoolWithSeed(clock, seed1, seed2, "a", "b", "c")
	if got := caller.Uint64(); got != wantCallerNext {
		t.Fatalf("New advanced the caller's PCG: got next %d, want %d", got, wantCallerNext)
	}
	for i := 0; i < 20; i++ {
		left, right := first.Order(clock.Now()), second.Order(clock.Now())
		leftSeen := requireUniqueStrings(t, left)
		rightSeen := requireUniqueStrings(t, right)
		if len(leftSeen) != 3 || len(rightSeen) != 3 || !leftSeen["a"] || !leftSeen["b"] || !leftSeen["c"] || !rightSeen["a"] || !rightSeen["b"] || !rightSeen["c"] {
			t.Fatalf("draw %d was not a permutation of all three members: %v vs %v", i, left, right)
		}
		if len(left) != len(right) {
			t.Fatalf("draw %d lengths differ: %v vs %v", i, left, right)
		}
		for j := range left {
			if left[j] != right[j] {
				t.Fatalf("draw %d differs: %v vs %v", i, left, right)
			}
		}
	}
}

func TestWeightedOrderFavorsLowSandButStillDrawsHighSandFirst(t *testing.T) {
	clock := newTestClock()
	const draws = 6000
	p := newTestPoolWithSeed(clock, 1001, 2002, "low", "high")
	for i := 0; i < 8; i++ {
		p.RecordSoft("high")
	}
	lowFirst, highFirst := 0, 0
	for i := 0; i < draws; i++ {
		order := p.Order(clock.Now())
		if len(order) != 2 || order[0] == order[1] {
			t.Fatalf("draw %d was not a two-key permutation: %v", i, order)
		}
		switch order[0] {
		case "low":
			lowFirst++
		case "high":
			highFirst++
		default:
			t.Fatalf("unexpected first key %q", order[0])
		}
	}
	if lowFirst < draws*90/100 || lowFirst > draws*99/100 {
		t.Fatalf("low-sand first count %d/%d outside deterministic 90-99%% bound", lowFirst, draws)
	}
	if highFirst == 0 {
		t.Fatal("higher-sand hop was never selected first; ordering may be deterministic")
	}
}

func TestSnapshotAndReturnedSlicesAreIndependent(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "a", "b")
	p.RecordSoft("a")

	snapshot := p.Snapshot()
	snapshot[0].Key = "corrupted"
	snapshot[0].Sand = 100
	snapshot[0].Weight = 100
	snapshot[0].State = Quarantined
	snapshot[0].HardFailures = 99
	snapshot[0].Trips = 99
	snapshot[0].NextProbe = testEpoch.Add(time.Hour)
	if got := requireSnapshot(t, p, "a"); got.Key != "a" || got.State != Healthy || got.Sand != 1 || got.Weight >= 1 || got.HardFailures != 0 || got.Trips != 0 || !got.NextProbe.IsZero() {
		t.Fatalf("mutating snapshot result changed pool state: %#v", got)
	}

	order := p.Order(clock.Now())
	order[0] = "corrupted"
	if seen := requireUniqueStrings(t, p.Order(clock.Now())); len(seen) != 2 || !seen["a"] || !seen["b"] {
		t.Fatalf("mutating order result changed pool membership: %v", seen)
	}
}

func TestEmptyPoolOrderAndSnapshotAreSafe(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock)
	if got := p.Order(clock.Now()); len(got) != 0 {
		t.Fatalf("empty pool order = %v", got)
	}
	if got := p.Snapshot(); len(got) != 0 {
		t.Fatalf("empty pool snapshot = %#v", got)
	}
	if got := p.DueProbes(clock.Now()); len(got) != 0 {
		t.Fatalf("empty pool due probes = %#v", got)
	}
	p.Reconcile(nil)
	p.Restore(nil, clock.Now())
}
