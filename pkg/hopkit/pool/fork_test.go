package pool

import (
	"reflect"
	"testing"
	"time"
)

func TestForkCopiesHealthIntoIndependentGeneration(t *testing.T) {
	clock := newTestClock()
	pool := newTestPoolWithSeed(clock, 11, 22, "sand", "hard", "quarantined", "leased", "removed")

	pool.RecordSoft("sand")
	pool.RecordHard("hard")
	pool.RecordHard("hard")
	for range 3 {
		pool.RecordHard("quarantined")
	}
	pool.RecordHard("leased")
	pool.RecordHard("leased")

	clock.Set(testEpoch.Add(30 * time.Second))
	pool.RecordHard("leased")
	quarantinedClaim := requireSingleClaim(t, pool.DueProbes(clock.Now()), "quarantined")
	if !pool.ReportProbe(quarantinedClaim, ProbeHardFailure) {
		t.Fatal("second quarantine trip was rejected")
	}

	clock.Set(testEpoch.Add(60 * time.Second))
	leasedClaim := requireSingleClaim(t, pool.DueProbes(clock.Now()), "leased")
	sourceBeforeFork := pool.Snapshot()

	fork := pool.Fork([]string{"hard", "sand", "quarantined", "leased", "added", "hard"})

	if got, want := snapshotKeys(fork.Snapshot()), []string{"hard", "sand", "quarantined", "leased", "added"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fork order = %v, want %v", got, want)
	}
	if got, want := snapshotKeys(pool.Snapshot()), []string{"sand", "hard", "quarantined", "leased", "removed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source membership/order changed: got %v, want %v", got, want)
	}

	if pool.members["hard"] == fork.members["hard"] {
		t.Fatal("fork shares hop allocation with source")
	}
	if pool.rng == fork.rng {
		t.Fatal("fork shares random source with source")
	}

	if got := pool.Snapshot(); !reflect.DeepEqual(got, sourceBeforeFork) {
		t.Fatalf("source state changed while constructing fork: got %#v, want %#v", got, sourceBeforeFork)
	}

	if got := requireSnapshot(t, fork, "hard").HardFailures; got != 2 {
		t.Fatalf("fork hard failures = %d, want 2", got)
	}
	fork.RecordHard("hard")
	if got := requireSnapshot(t, fork, "hard"); got.State != Quarantined || got.Trips != 1 {
		t.Fatalf("fork third hard failure state = %#v, want quarantined with one trip", got)
	}
	if got := requireSnapshot(t, pool, "hard"); got.State != Healthy || got.HardFailures != 2 || got.Trips != 0 {
		t.Fatalf("source changed after recording in fork: %#v", got)
	}

	for _, key := range []string{"sand", "quarantined", "leased", "added"} {
		_ = requireSnapshot(t, fork, key)
	}
	wantSand := effectiveSand(pool.members["sand"], clock.Now())
	requireClose(t, requireSnapshot(t, fork, "sand").Sand, wantSand, 1e-12)
	if got := requireSnapshot(t, fork, "leased").State; got != Quarantined {
		t.Fatalf("fork copied active lease state: got %q, want %q", got, Quarantined)
	}
	if fork.ReportProbe(leasedClaim, ProbeSuccess) {
		t.Fatal("fork accepted a probe claim from the source generation")
	}
	if got := requireSnapshot(t, pool, "leased").State; got != Probing {
		t.Fatalf("source lease was changed by fork: got %q, want %q", got, Probing)
	}
	sourceQuarantine := requireSnapshot(t, pool, "quarantined")
	forkQuarantine := requireSnapshot(t, fork, "quarantined")
	if sourceQuarantine.Trips != 2 || forkQuarantine.Trips != sourceQuarantine.Trips || forkQuarantine.NextProbe != sourceQuarantine.NextProbe {
		t.Fatalf("quarantine state not carried over: source=%#v fork=%#v", sourceQuarantine, forkQuarantine)
	}

	clock.Set(testEpoch.Add(90 * time.Second))
	requireClose(t, requireSnapshot(t, fork, "sand").Sand, 0.125, 1e-12)
}

func snapshotKeys(snapshots []HopSnapshot) []string {
	keys := make([]string, len(snapshots))
	for i, snapshot := range snapshots {
		keys[i] = snapshot.Key
	}
	return keys
}
