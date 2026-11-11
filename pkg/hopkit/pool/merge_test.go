package pool

import (
	"reflect"
	"testing"
	"time"
)

func TestForkFromMergesIndependentHealthAndKeepsSources(t *testing.T) {
	clock := newTestClock()
	first := newTestPoolWithSeed(clock, 11, 22, "A")
	second := newTestPoolWithSeed(clock, 33, 44, "B", "A")
	first.RecordHard("A")
	first.RecordHard("A")
	second.RecordSoft("B")
	for range 3 {
		second.RecordHard("B")
		second.RecordHard("A")
	}

	clock.Set(testEpoch.Add(30 * time.Second))
	claims := second.DueProbes(clock.Now())
	if len(claims) != 2 {
		t.Fatalf("source probe claims = %d, want 2", len(claims))
	}
	firstBefore := first.Snapshot()
	secondBefore := second.Snapshot()

	merged := ForkFrom([]string{"A", "B", "C", "A"}, first, second)
	if got, want := snapshotKeys(merged.Snapshot()), []string{"A", "B", "C"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged order = %v, want %v", got, want)
	}
	if got := requireSnapshot(t, merged, "A"); got.State != Healthy || got.HardFailures != 2 {
		t.Fatalf("first source did not win conflict for A: %#v", got)
	}
	if got := requireSnapshot(t, merged, "B"); got.State != Quarantined || got.Trips != 1 || got.HardFailures != 3 || got.Sand <= 0 {
		t.Fatalf("merged B did not retain quarantine and soft health without lease: %#v", got)
	}
	if got, want := requireSnapshot(t, merged, "B").NextProbe, requireSnapshot(t, second, "B").NextProbe; !got.Equal(want) {
		t.Fatalf("merged next probe = %v, want %v", got, want)
	}
	if got := requireSnapshot(t, second, "B"); got.State != Probing {
		t.Fatalf("merge changed source probe lease: %#v", got)
	}
	if !reflect.DeepEqual(first.Snapshot(), firstBefore) || !reflect.DeepEqual(second.Snapshot(), secondBefore) {
		t.Fatal("merge changed source health")
	}
	if got, want := snapshotKeys(first.Snapshot()), []string{"A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first source membership = %v, want %v", got, want)
	}
	if got, want := snapshotKeys(second.Snapshot()), []string{"B", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second source membership = %v, want %v", got, want)
	}
	for _, claim := range claims {
		if merged.ReportProbe(claim, ProbeSuccess) {
			t.Fatalf("merged pool accepted source lease for %q", claim.Key())
		}
	}
	if got := requireSnapshot(t, merged, "B").State; got != Quarantined {
		t.Fatalf("merged pool acquired a source lease: state %q", got)
	}
}

func TestForkFromSkipsNilSourcesAndUsesFirstAvailable(t *testing.T) {
	clock := newTestClock()
	source := newTestPoolWithSeed(clock, 11, 22, "key")
	source.RecordHard("key")

	fork := ForkFrom([]string{"key", "new"}, nil, source, nil)
	if got := requireSnapshot(t, fork, "key").HardFailures; got != 1 {
		t.Fatalf("copied hard failures = %d, want 1", got)
	}
	if got := requireSnapshot(t, fork, "new"); got.State != Healthy || got.Trips != 0 {
		t.Fatalf("new key has unexpected health: %#v", got)
	}
}
