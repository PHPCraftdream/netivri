package hoppool

import (
	"math"
	"testing"
	"time"
)

func TestSandCapAndEndpointWeights(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")

	initial := requireSnapshot(t, p, "hop")
	requireClose(t, initial.Sand, 0, 0)
	requireClose(t, initial.Weight, 1, 1e-12)

	for i := 0; i < 20; i++ {
		p.RecordSoft("hop")
	}
	capped := requireSnapshot(t, p, "hop")
	requireClose(t, capped.Sand, 8, 1e-12)
	requireClose(t, capped.Weight, 0.05, 1e-12)
}

func TestSandLazyHalfLifeAt15_30_60Seconds(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 8; i++ {
		p.RecordSoft("hop")
	}

	for _, tc := range []struct {
		elapsed time.Duration
		want    float64
	}{
		{15 * time.Second, 8 * math.Sqrt(0.5)},
		{30 * time.Second, 4},
		{60 * time.Second, 2},
	} {
		clock.Set(testEpoch.Add(tc.elapsed))
		snapshot := requireSnapshot(t, p, "hop")
		requireClose(t, snapshot.Sand, tc.want, 1e-12)
		requireClose(t, snapshot.Weight, math.Pow(20, -tc.want/8), 1e-12)
	}
}

func TestSuccessHalvesEffectiveSand(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 4; i++ {
		p.RecordSoft("hop")
	}
	clock.Set(testEpoch.Add(30 * time.Second))

	p.RecordSuccess("hop")
	snapshot := requireSnapshot(t, p, "hop")
	requireClose(t, snapshot.Sand, 1, 1e-12) // 4 decays to 2, then success halves it.
}

func TestSuccessiveSoftFailureAddsAfterElapsedDecay(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 4; i++ {
		p.RecordSoft("hop")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	p.RecordSoft("hop")

	requireClose(t, requireSnapshot(t, p, "hop").Sand, 3, 1e-12)
}

func TestBackwardClockDoesNotInflateSand(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 4; i++ {
		p.RecordSoft("hop")
	}

	clock.Set(testEpoch.Add(30 * time.Second))
	p.RecordSoft("hop") // The stored score becomes 2, then this event makes it 3.
	requireClose(t, requireSnapshot(t, p, "hop").Sand, 3, 1e-12)

	clock.Set(testEpoch.Add(15 * time.Second))
	p.RecordSoft("hop") // A backwards event must not decay from the later timestamp.
	requireClose(t, requireSnapshot(t, p, "hop").Sand, 4, 1e-12)

	clock.Set(testEpoch.Add(30 * time.Second))
	requireClose(t, requireSnapshot(t, p, "hop").Sand, 4, 1e-12)
	clock.Set(testEpoch.Add(60 * time.Second))
	requireClose(t, requireSnapshot(t, p, "hop").Sand, 2, 1e-12)
}

func TestInconclusiveInvalidStrengthAndUnknownKeyAreNoOps(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "known")
	for i := 0; i < 3; i++ {
		p.RecordSoft("known")
	}
	p.RecordHard("known")
	p.RecordHard("known")

	p.Record("known", Inconclusive)
	p.Record("known", Strength(255))
	p.Record("missing", Soft)
	p.Record("missing", Hard)
	p.Record("missing", Inconclusive)
	p.RecordSuccess("missing")

	p.RecordHard("known")
	snapshot := requireSnapshot(t, p, "known")
	if snapshot.State != Quarantined || snapshot.Trips != 1 || snapshot.HardFailures != 3 {
		t.Fatalf("no-op events changed the hard-failure streak: %#v", snapshot)
	}
	requireClose(t, snapshot.Sand, 3, 1e-12)
	if got := len(p.Snapshot()); got != 1 {
		t.Fatalf("unknown key changed membership; snapshot length = %d", got)
	}
}
