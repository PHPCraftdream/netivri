package pool

import (
	"testing"
)

func quarantinedClaim(t *testing.T, p *Pool, clock *testClock) ProbeClaim {
	t.Helper()
	for range hardTripThreshold {
		p.RecordHard("hop")
	}
	clock.Set(testEpoch.Add(initialBackoff))
	return requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
}

func TestReleaseProbeClearsOnlyLeaseAndPreservesQuarantine(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	p.RecordSoft("hop")
	claim := quarantinedClaim(t, p, clock)
	before := requireSnapshot(t, p, "hop")

	if !p.ReleaseProbe(claim) {
		t.Fatal("current lease was not released")
	}
	after := requireSnapshot(t, p, "hop")
	if after.State != Quarantined || after.Sand != before.Sand || after.HardFailures != before.HardFailures || after.Trips != before.Trips || !after.NextProbe.Equal(before.NextProbe) {
		t.Fatalf("release changed hop health: before=%#v after=%#v", before, after)
	}
	if claims := p.DueProbes(clock.Now()); len(claims) != 1 || claims[0].identity == claim.identity {
		t.Fatalf("released hop was not immediately re-leased with a new identity: %#v", claims)
	}
}

func TestReleaseProbeRejectsZeroForeignStaleAndExpiredClaims(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	foreign := newTestPool(clock, "hop")
	claim := quarantinedClaim(t, p, clock)
	ownerBefore := requireSnapshot(t, p, "hop")
	foreignBefore := requireSnapshot(t, foreign, "hop")
	if p.ReleaseProbe(ProbeClaim{}) {
		t.Fatal("zero claim was released")
	}
	if got := requireSnapshot(t, p, "hop"); got != ownerBefore {
		t.Fatalf("zero claim changed owner state: before=%#v after=%#v", ownerBefore, got)
	}
	if foreign.ReleaseProbe(claim) {
		t.Fatal("foreign claim was released")
	}
	if got := requireSnapshot(t, foreign, "hop"); got != foreignBefore {
		t.Fatalf("foreign claim changed destination state: before=%#v after=%#v", foreignBefore, got)
	}
	if !p.ReleaseProbe(claim) {
		t.Fatal("current claim was not released")
	}
	current := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if p.ReleaseProbe(claim) {
		t.Fatal("stale claim released the newer lease")
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Probing {
		t.Fatalf("stale release changed current lease: %#v", snapshot)
	}

	clock.Set(clock.Now().Add(probeLease))
	if p.ReleaseProbe(current) {
		t.Fatal("expired claim was released")
	}
	if snapshot := requireSnapshot(t, p, "hop"); snapshot.State != Quarantined {
		t.Fatalf("expired release changed quarantine state: %#v", snapshot)
	}
	reissued := requireSingleClaim(t, p.DueProbes(clock.Now()), "hop")
	if reissued.identity == current.identity || p.ReleaseProbe(current) {
		t.Fatal("expired claim released or reused the reissued lease")
	}
}

func TestReleaseProbeIsNoOpAfterPoolStateReplacement(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "hop")
	claim := quarantinedClaim(t, p, clock)
	p.Restore([]Entry{{Key: "hop", Trips: 2}}, clock.Now())
	before := requireSnapshot(t, p, "hop")
	if p.ReleaseProbe(claim) {
		t.Fatal("claim was released after pool state replacement")
	}
	if after := requireSnapshot(t, p, "hop"); after != before {
		t.Fatalf("rejected release changed state: before=%#v after=%#v", before, after)
	}
}
