package pool

import (
	"testing"
	"time"
)

func TestQuarantineRequiresThreeConsecutiveHardFailures(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 1; i <= 2; i++ {
		p.RecordHard("hop")
		snapshot := requireSnapshot(t, p, "hop")
		if snapshot.State != Healthy || snapshot.HardFailures != i || snapshot.Trips != 0 {
			t.Fatalf("after hard failure %d: %#v", i, snapshot)
		}
	}

	p.RecordHard("hop")
	snapshot := requireSnapshot(t, p, "hop")
	if snapshot.State != Quarantined || snapshot.HardFailures != 3 || snapshot.Trips != 1 {
		t.Fatalf("third consecutive hard failure did not trip quarantine: %#v", snapshot)
	}
	if want := testEpoch.Add(30 * time.Second); !snapshot.NextProbe.Equal(want) {
		t.Fatalf("first next probe = %s, want %s", snapshot.NextProbe, want)
	}
}

func TestSoftFailureInterruptsHardFailureStreak(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	p.RecordHard("hop")
	p.RecordHard("hop")
	p.RecordSoft("hop")
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Healthy || snapshot.HardFailures != 0 {
		t.Fatalf("soft failure did not interrupt hard streak: %#v", snapshot)
	}

	for i := 1; i <= 2; i++ {
		p.RecordHard("hop")
		if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Healthy || snapshot.HardFailures != i {
			t.Fatalf("after post-soft hard failure %d: %#v", i, snapshot)
		}
	}
	p.RecordHard("hop")
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Quarantined || snapshot.Trips != 1 {
		t.Fatalf("third post-soft hard failure did not trip: %#v", snapshot)
	}
}

func TestRepeatedHardFailureDuringQuarantineDoesNotChangeBackoff(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	before := requireSnapshot(t, p, "hop")
	for i := 0; i < 7; i++ {
		p.RecordHard("hop")
	}
	after := requireSnapshot(t, p, "hop")
	if after.State != before.State || after.HardFailures != before.HardFailures || after.Trips != before.Trips || !after.NextProbe.Equal(before.NextProbe) {
		t.Fatalf("hard failure while quarantined changed state: before=%#v after=%#v", before, after)
	}
}

func TestSuccessClearsCountsTripsAndRestoresInitialBackoff(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	clock.Set(testEpoch.Add(5 * time.Second))
	p.RecordSuccess("hop")
	snapshot := requireSnapshot(t, p, "hop")
	if snapshot.State != Healthy || snapshot.HardFailures != 0 || snapshot.Trips != 0 || !snapshot.NextProbe.IsZero() {
		t.Fatalf("success did not clear quarantine history: %#v", snapshot)
	}

	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	snapshot = requireSnapshot(t, p, "hop")
	if snapshot.Trips != 1 || !snapshot.NextProbe.Equal(clock.Now().Add(30*time.Second)) {
		t.Fatalf("success did not reset backoff to 30 seconds: %#v", snapshot)
	}
}

func TestProbeBackoffProgressionAndTripCounts(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}

	wantBackoffs := []time.Duration{
		30 * time.Second,
		60 * time.Second,
		120 * time.Second,
		240 * time.Second,
		480 * time.Second,
		600 * time.Second,
		600 * time.Second,
	}
	for i, wantBackoff := range wantBackoffs {
		snapshot := requireSnapshot(t, p, "hop")
		if snapshot.Trips != i+1 {
			t.Fatalf("trip %d has count %d", i+1, snapshot.Trips)
		}
		wantDue := clock.Now().Add(wantBackoff)
		if i == 0 {
			wantDue = testEpoch.Add(wantBackoff)
		}
		if !snapshot.NextProbe.Equal(wantDue) {
			t.Fatalf("trip %d next probe = %s, want %s", i+1, snapshot.NextProbe, wantDue)
		}
		if i == len(wantBackoffs)-1 {
			break
		}

		clock.Set(snapshot.NextProbe)
		claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
		if !p.ReportProbe(claim, ProbeHardFailure) {
			t.Fatalf("hard result for trip %d was rejected", i+2)
		}
	}
}

func TestMaxIntRestoredTripsRemainSaturatedWithCappedBackoff(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	maxInt := int(^uint(0) >> 1)
	p.Restore([]Entry{{Key: "hop", Trips: maxInt}}, clock.Now())
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if !p.ReportProbe(claim, ProbeHardFailure) {
		t.Fatal("hard probe result rejected")
	}

	snapshot := requireSnapshot(t, p, "hop")
	if snapshot.Trips != maxInt {
		t.Fatalf("trip count overflowed: got %d, want %d", snapshot.Trips, maxInt)
	}
	if want := clock.Now().Add(600 * time.Second); !snapshot.NextProbe.Equal(want) {
		t.Fatalf("saturated next probe = %s, want %s", snapshot.NextProbe, want)
	}
}

func TestProbeLeaseDueExpiryStaleReportsAndSuccess(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	firstDue := testEpoch.Add(30 * time.Second)

	clock.Set(firstDue.Add(-time.Nanosecond))
	if claims := p.DueProbes(clock.Now()); len(claims) != 0 {
		t.Fatalf("probe leased before due: %d claims", len(claims))
	}
	clock.Set(firstDue)
	oldClaim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if got := oldClaim.Key(); got != "hop" {
		t.Fatalf("claim key = %q, want hop", got)
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Probing {
		t.Fatalf("leased hop state = %q, want probing", snapshot.State)
	}
	clock.Set(firstDue.Add(24 * time.Second))
	if claims := p.DueProbes(clock.Now()); len(claims) != 0 {
		t.Fatalf("active lease issued a duplicate claim: %d", len(claims))
	}

	leaseExpiry := firstDue.Add(25 * time.Second)
	clock.Set(leaseExpiry)
	if p.ReportProbe(oldClaim, ProbeSuccess) {
		t.Fatal("expired claim was accepted before reissue")
	}
	if p.ReportProbe(oldClaim, ProbeHardFailure) {
		t.Fatal("expired hard result was accepted before reissue")
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Quarantined {
		t.Fatalf("expired lease state = %q, want quarantined", snapshot.State)
	}
	currentClaim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if currentClaim.identity == oldClaim.identity {
		t.Fatal("lease expiry reused the old claim identity")
	}
	if p.ReportProbe(oldClaim, ProbeSuccess) {
		t.Fatal("stale success report was accepted after reissue")
	}
	if p.ReportProbe(oldClaim, ProbeHardFailure) {
		t.Fatal("stale hard report was accepted after reissue")
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Probing {
		t.Fatalf("stale report consumed current lease: %#v", snapshot)
	}
	if !p.ReportProbe(currentClaim, ProbeSuccess) {
		t.Fatal("current probe success was rejected")
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Healthy || snapshot.Trips != 0 || snapshot.HardFailures != 0 {
		t.Fatalf("current success did not revive the hop: %#v", snapshot)
	}
}

func TestProbeSuccessReturnsHopToOrderAndHalvesSand(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "recovering", "healthy")
	for i := 0; i < 4; i++ {
		p.RecordSoft("recovering")
	}
	for i := 0; i < 3; i++ {
		p.RecordHard("recovering")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "recovering")
	if !p.ReportProbe(claim, ProbeSuccess) {
		t.Fatal("probe success rejected")
	}

	snapshot := requireSnapshot(t, p, "recovering")
	if snapshot.State != Healthy || snapshot.HardFailures != 0 || snapshot.Trips != 0 || !snapshot.NextProbe.IsZero() {
		t.Fatalf("success did not clear quarantine counters: %#v", snapshot)
	}
	requireClose(t, snapshot.Sand, 1, 1e-12)
	order := p.Order(clock.Now())
	seen := requireUniqueStrings(t, order)
	if len(order) != 2 || !seen["recovering"] || !seen["healthy"] {
		t.Fatalf("success did not return both hops to common order: %v", order)
	}
}

func TestProbeHardFailureRetripsWithDoubledBackoff(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if !p.ReportProbe(claim, ProbeHardFailure) {
		t.Fatal("probe hard failure rejected")
	}
	snapshot := requireSnapshot(t, p, "hop")
	if snapshot.State != Quarantined || snapshot.Trips != 2 || snapshot.HardFailures != 3 {
		t.Fatalf("probe failure did not retrip: %#v", snapshot)
	}
	if want := clock.Now().Add(60 * time.Second); !snapshot.NextProbe.Equal(want) {
		t.Fatalf("second-trip next probe = %s, want %s", snapshot.NextProbe, want)
	}
}

func TestInconclusiveProbeKeepsLeaseUntilTwentyFiveSeconds(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	before := requireSnapshot(t, p, "hop")
	if !p.ReportProbe(claim, ProbeInconclusive) {
		t.Fatal("inconclusive result for current claim rejected")
	}
	after := requireSnapshot(t, p, "hop")
	if after.State != Probing || after.Trips != before.Trips || after.HardFailures != before.HardFailures || !after.NextProbe.Equal(before.NextProbe) {
		t.Fatalf("inconclusive result changed quarantine state: before=%#v after=%#v", before, after)
	}
	clock.Set(testEpoch.Add(54*time.Second + 999*time.Millisecond))
	if claims := p.DueProbes(clock.Now()); len(claims) != 0 {
		t.Fatalf("inconclusive lease released early: %d claims", len(claims))
	}
	clock.Set(testEpoch.Add(55 * time.Second))
	if claims := p.DueProbes(clock.Now()); len(claims) != 1 || claims[0].identity == claim.identity {
		t.Fatalf("lease was not replaced at 25 seconds: %#v", claims)
	}
}

func TestZeroAndForeignProbeClaimsAreRejected(t *testing.T) {
	clock := newTestClock()
	first := newTestPool(clock, "hop")
	foreignPool := newTestPool(clock, "hop")
	for _, p := range []*Pool{first, foreignPool} {
		for i := 0; i < 3; i++ {
			p.RecordHard("hop")
		}
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, first.DueProbes(clock.Now()), "hop")
	if first.ReportProbe(ProbeClaim{}, ProbeSuccess) {
		t.Fatal("zero claim was accepted")
	}
	if foreignPool.ReportProbe(claim, ProbeSuccess) {
		t.Fatal("foreign pool claim was accepted")
	}
	if snapshot := requireSnapshot(t, first, "hop"); snapshot.State != Probing {
		t.Fatalf("zero/foreign claim changed owner state: %#v", snapshot)
	}
	if snapshot := requireSnapshot(t, foreignPool, "hop"); snapshot.State != Quarantined {
		t.Fatalf("foreign claim changed destination state: %#v", snapshot)
	}
}

func TestOldProbeClaimsAreIgnoredAfterPoolStateReplacement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Pool, time.Time)
		want   State
	}{
		{
			name: "remove and re-add",
			mutate: func(p *Pool, now time.Time) {
				p.Reconcile([]string{"other"})
				p.Reconcile([]string{"hop"})
			},
			want: Healthy,
		},
		{
			name: "restore",
			mutate: func(p *Pool, now time.Time) {
				p.Restore([]Entry{{Key: "hop", Trips: 2}}, now)
			},
			want: Quarantined,
		},
		{
			name: "record success",
			mutate: func(p *Pool, now time.Time) {
				p.RecordSuccess("hop")
			},
			want: Healthy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newTestClock()
			p := newTestPool(clock, "hop")
			for i := 0; i < 3; i++ {
				p.RecordHard("hop")
			}
			clock.Set(testEpoch.Add(30 * time.Second))
			claim := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
			tc.mutate(p, clock.Now())
			if p.ReportProbe(claim, ProbeSuccess) {
				t.Fatal("old claim success was accepted after state replacement")
			}
			if p.ReportProbe(claim, ProbeHardFailure) {
				t.Fatal("old claim hard result was accepted after state replacement")
			}
			if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != tc.want {
				t.Fatalf("state after stale reports = %q, want %q: %#v", snapshot.State, tc.want, snapshot)
			}
		})
	}
}

func TestRestoreClampsNonPositiveTripCounts(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	p.Restore([]Entry{{Key: "hop", Trips: -int(^uint(0)>>1) - 1}}, clock.Now())
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.Trips != 1 {
		t.Fatalf("restored trip count = %d, want 1", snapshot.Trips)
	}
}
