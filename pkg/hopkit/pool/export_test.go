package pool

import (
	"testing"
	"time"
)

func TestExportPersistsOnlyQuarantinedMembersIncludingActiveLease(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "healthy", "active", "waiting")
	p.RecordSoft("healthy")
	for i := 0; i < 3; i++ {
		p.RecordHard("active")
		p.RecordHard("waiting")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claims := p.DueProbes(clock.Now())
	if len(claims) != 2 {
		t.Fatalf("due claims = %d, want 2", len(claims))
	}
	if got := p.Export(); len(got) != 2 || got[0].Key != "active" || got[1].Key != "waiting" {
		t.Fatalf("export should contain quarantined entries in insertion order: %#v", got)
	}
	entries := p.Export()
	for _, entry := range entries {
		if entry.Trips != 1 {
			t.Errorf("%q trip count = %d, want 1", entry.Key, entry.Trips)
		}
		if !entry.NextProbe.Equal(testEpoch.Add(30 * time.Second)) {
			t.Errorf("%q exported next probe = %s", entry.Key, entry.NextProbe)
		}
	}
	if p.ReportProbe(claims[0], ProbeInconclusive) != true {
		t.Fatal("active lease inconclusive result rejected")
	}
	if got := p.Export(); len(got) != 2 || got[0].Key != "active" || got[1].Key != "waiting" {
		t.Fatalf("active lease was not included in export: %#v", got)
	}
	entries[0].Key = "corrupted"
	if got := p.Export(); got[0].Key != "active" {
		t.Fatalf("mutating exported slice changed pool: %#v", got)
	}
}

func TestRestoreRoundTripReplacementAndImmediateEligibility(t *testing.T) {
	clock := newTestClock()
	source := newTestPool(clock, "healthy", "quarantined")
	for i := 0; i < 3; i++ {
		source.RecordHard("quarantined")
	}
	clock.Set(testEpoch.Add(30 * time.Second))
	claim := requireSingleClaim(t, source.DueProbes(clock.Now()), "quarantined")
	if !source.ReportProbe(claim, ProbeHardFailure) {
		t.Fatal("source probe hard failure rejected")
	}
	future := testEpoch.Add(24 * time.Hour)
	// Persisted NextProbe is deliberately future-valued; Restore replaces it with now.
	entries := source.Export()
	if len(entries) != 1 || entries[0].Key != "quarantined" || entries[0].Trips != 2 {
		t.Fatalf("source export = %#v", entries)
	}
	if !entries[0].NextProbe.Equal(testEpoch.Add(90 * time.Second)) {
		t.Fatalf("source next probe = %s, want %s", entries[0].NextProbe, testEpoch.Add(90*time.Second))
	}
	entries[0].NextProbe = future
	entries = append(entries, Entry{Key: "unknown", Trips: 9, NextProbe: future})
	if !source.Export()[0].NextProbe.Equal(testEpoch.Add(90 * time.Second)) {
		t.Fatal("unexpected source export due time")
	}

	restored := newTestPool(clock, "healthy", "quarantined")
	restored.Restore(entries, clock.Now())
	if got := restored.Export(); len(got) != 1 || got[0].Key != "quarantined" || got[0].Trips != 2 {
		t.Fatalf("restore did not retain only known entry/trip count: %#v", got)
	}
	if got := restored.Snapshot(); len(got) != 2 || got[0].State != Healthy || got[1].State != Quarantined || got[1].HardFailures != 3 || got[1].Trips != 2 {
		t.Fatalf("restored snapshot = %#v", got)
	}
	if !restored.Export()[0].NextProbe.Equal(clock.Now()) {
		t.Fatalf("restore did not make entry due now: %#v", restored.Export())
	}
	claim = requireSingleClaim(t, restored.DueProbes(clock.Now()), "quarantined")

	// Restore is replacement, not a merge: absent members revive and active leases are revoked.
	restored.Restore([]Entry{{Key: "healthy", Trips: 2, NextProbe: future}}, clock.Now())
	if restored.ReportProbe(claim, ProbeSuccess) {
		t.Fatal("pre-replacement lease remained valid")
	}
	snapshot := restored.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Key != "healthy" || snapshot[0].State != Quarantined || snapshot[0].Trips != 2 || snapshot[0].HardFailures != 3 || snapshot[1].Key != "quarantined" || snapshot[1].State != Healthy || snapshot[1].Trips != 0 {
		t.Fatalf("replacement restore did not replace prior quarantine: %#v", snapshot)
	}
	if got := restored.Export(); len(got) != 1 || got[0].Key != "healthy" || got[0].Trips != 2 {
		t.Fatalf("replacement export = %#v", got)
	}
}

func TestRestoreIgnoresRemovedKeysAndUnknownEntries(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "kept", "removed")
	for i := 0; i < 3; i++ {
		p.RecordHard("removed")
	}
	p.Reconcile([]string{"kept"})
	p.Restore([]Entry{{Key: "removed", Trips: 8}, {Key: "unknown", Trips: 9}}, clock.Now())
	if got := p.Snapshot(); len(got) != 1 || got[0].Key != "kept" || got[0].State != Healthy {
		t.Fatalf("restore resurrected removed or unknown key: %#v", got)
	}
	if got := p.Export(); len(got) != 0 {
		t.Fatalf("unknown restore entries were exported: %#v", got)
	}
}

func TestReconcilePreservesSurvivingHealthAndLeaseAndDropsRemoved(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "active", "waiting", "healthy", "removed", "")
	p.RecordSoft("active")
	p.RecordSoft("active")
	p.RecordSoft("healthy")
	for i := 0; i < 3; i++ {
		p.RecordHard("active")
	}
	p.RecordHard("removed")
	clock.Set(testEpoch.Add(30 * time.Second))
	claims := p.DueProbes(clock.Now())
	claim := requireSingleClaim(t, claims, "active")
	for i := 0; i < 3; i++ {
		p.RecordHard("waiting")
	}

	p.Reconcile([]string{"healthy", "active", "active", "", "new", "waiting", "new"})
	snapshot := p.Snapshot()
	if len(snapshot) != 5 {
		t.Fatalf("reconcile retained duplicate or removed members: %#v", snapshot)
	}
	wantOrder := []string{"healthy", "active", "", "new", "waiting"}
	for i, want := range wantOrder {
		if snapshot[i].Key != want {
			t.Fatalf("snapshot[%d].Key = %q, want %q", i, snapshot[i].Key, want)
		}
	}
	byKey := make(map[string]HopSnapshot, len(snapshot))
	for _, state := range snapshot {
		byKey[state.Key] = state
	}
	if byKey["active"].State != Probing || byKey["active"].HardFailures != 3 || byKey["active"].Trips != 1 {
		t.Fatalf("surviving active quarantine state was not retained: %#v", byKey["active"])
	}
	if byKey["active"].Sand != 1 {
		t.Fatalf("surviving active sand = %g, want 1", byKey["active"].Sand)
	}
	if !byKey["active"].NextProbe.Equal(testEpoch.Add(30 * time.Second)) {
		t.Fatalf("surviving active next probe = %s, want %s", byKey["active"].NextProbe, testEpoch.Add(30*time.Second))
	}
	if byKey["waiting"].State != Quarantined || byKey["waiting"].HardFailures != 3 || byKey["waiting"].Trips != 1 {
		t.Fatalf("surviving quarantine state was not retained: %#v", byKey["waiting"])
	}
	if !byKey["waiting"].NextProbe.Equal(testEpoch.Add(60 * time.Second)) {
		t.Fatalf("surviving waiting next probe = %s, want %s", byKey["waiting"].NextProbe, testEpoch.Add(60*time.Second))
	}
	if byKey["healthy"].State != Healthy || byKey["healthy"].HardFailures != 0 || byKey["healthy"].Trips != 0 || byKey["healthy"].Sand != 0.5 {
		t.Fatalf("surviving healthy state changed: %#v", byKey["healthy"])
	}
	if byKey["new"].State != Healthy || byKey["new"].Sand != 0 || byKey[""].State != Healthy {
		t.Fatalf("new or empty-key member was not initialized healthy: %#v %#v", byKey["new"], byKey[""])
	}
	if p.ReportProbe(claim, ProbeSuccess) != true {
		t.Fatal("reconcile invalidated active lease for a surviving key")
	}
	if got := requireSnapshot(t, p, "active"); got.State != Healthy || got.Trips != 0 {
		t.Fatalf("surviving active claim could not revive member: %#v", got)
	}
	for _, state := range p.Snapshot() {
		if state.Key == "removed" {
			t.Fatal("removed key survived reconciliation")
		}
	}
}

func TestReconcileEmptyMembershipIsSafe(t *testing.T) {
	clock := newTestClock()
	p := newTestPool(clock, "a")
	p.Reconcile(nil)
	if got := p.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot after empty reconcile = %#v", got)
	}
	if got := p.Export(); len(got) != 0 {
		t.Fatalf("export after empty reconcile = %#v", got)
	}
	if got := p.Order(clock.Now()); len(got) != 0 {
		t.Fatalf("order after empty reconcile = %v", got)
	}
}
