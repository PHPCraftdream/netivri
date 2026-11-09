package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHealthMemberKeyTracksEffectiveHops(t *testing.T) {
	first := healthMemberKey("shared", "socks5://user:secret@pre.example:1080", "")
	if first == healthMemberKey("shared", "socks5://user:secret@other.example:1080", "") {
		t.Fatal("different effective pre hops shared a health key")
	}
	shared := healthMemberKey("shared", "socks5://user:secret@pre.example:1080", "none")
	if first != shared {
		t.Fatal("equivalent effective hops did not share a health key")
	}
	if strings.Contains(first, "secret") {
		t.Fatal("health key contains proxy credentials")
	}
	health := newHealthTable()
	health.registerMembers([]healthMember{{key: first, label: "shared"}, {key: shared, label: "shared"}})
	if health.health(first) != health.health(shared) {
		t.Fatal("equivalent hops did not share a health record")
	}
}

func TestHealthMembersForManualPlansFallsBackToLabels(t *testing.T) {
	members := healthMembersForPlans([]routePlan{{labels: []string{"manual"}}})
	if len(members) != 1 || members[0].key != "manual" || members[0].label != "manual" || members[0].hops != (hopSet{}) {
		t.Fatalf("manual plan members = %+v, want label fallback without hops", members)
	}
	health := newHealthTable()
	health.registerMembers(members)
	if health.health("manual") == nil {
		t.Fatal("manual plan label was not registered")
	}
}

func TestCarryHealthKeepsSameLabelHopRecordsIsolated(t *testing.T) {
	old := newHealthTable()
	aKey := healthMemberKey("shared", "socks5://alice.example:1080", "")
	bKey := healthMemberKey("shared", "socks5://bob.example:1080", "")
	members := []healthMember{{key: aKey, label: "shared"}, {key: bKey, label: "shared"}}
	old.registerMembers(members)
	old.recordProbeKey(aKey, false, 0)
	old.recordProbeKey(aKey, false, 0)
	if !old.downKey(aKey) || old.downKey(bKey) {
		t.Fatal("old health records were not isolated")
	}

	var events []NodeEvent
	carried := carryHealthMembers(old, members, func(event NodeEvent) { events = append(events, event) })
	if !carried.downKey(aKey) || carried.downKey(bKey) {
		t.Fatal("carried health records were not isolated")
	}
	if carried.health(aKey) == carried.health(bKey) {
		t.Fatal("different hop keys shared a carried health record")
	}
	carried.recordProbeKey(aKey, true, time.Millisecond)
	if len(events) != 1 || events[0].Label != "shared" || events[0].Down {
		t.Fatalf("carried health event = %+v, want recovery for public label shared", events)
	}
}

func TestHealthKeysIsolateFailuresAndPreserveLabels(t *testing.T) {
	var nodeEvents []NodeEvent
	health := newHealthTable()
	health.notify = func(event NodeEvent) { nodeEvents = append(nodeEvents, event) }
	aKey := healthMemberKey("shared", "socks5://alice.example:1080", "")
	bKey := healthMemberKey("shared", "socks5://bob.example:1080", "")
	backupKey := healthMemberKey("backup", "", "")
	if aKey == bKey {
		t.Fatal("same label with different hops shared a health key")
	}

	a := &fakeMember{name: "alice-member"}
	failure := errors.New("bob.example:1080 connect error: connection refused")
	bad := &fakeMember{name: "bob-member", err: failure}
	backup := &fakeMember{name: "backup-member"}
	table := newAffinityTable(nil, 0)
	observer := &recordedObserver{}
	alice := newStickyGroupMembersWithHealthKeys("alice-route", []member{a}, []string{"shared"}, []string{aKey}, nil, table, health, observer)
	bob := newStickyGroupMembersWithHealthKeys("bob-route", []member{bad, backup}, []string{"shared", "backup"}, []string{bKey, backupKey}, []hopSet{{pre: chainHop("socks5://bob.example:1080")}, {}}, table, health, observer)
	var connEvents []ConnEvent
	bob.events = func(event ConnEvent) { connEvents = append(connEvents, event) }

	health.recordProbeKey(bKey, true, time.Millisecond)
	health.recordProbeKey(backupKey, true, time.Second)
	if index, _ := bob.choose("bob", "third.example"); index != 0 {
		t.Fatalf("Bob initially chose member %d, want failing member 0", index)
	}
	health.recordDialAtKey(bKey, "one.example", failure, time.Millisecond)
	conn, err := alice.DialContext(context.Background(), dialMetadata("alice", "site.example"))
	if err != nil {
		t.Fatalf("Alice dial between Bob failures: %v", err)
	}
	_ = conn.Close()
	health.recordDialAtKey(bKey, "two.example", failure, time.Millisecond)
	conn, err = alice.DialContext(context.Background(), dialMetadata("alice", "site.example"))
	if err != nil {
		t.Fatalf("second Alice dial between Bob failures: %v", err)
	}
	_ = conn.Close()

	conn, err = bob.DialContext(context.Background(), dialMetadata("bob", "third.example"))
	if err != nil {
		t.Fatalf("Bob fallback dial: %v", err)
	}
	if conn == nil {
		t.Fatal("Bob fallback dial returned a nil connection")
	}
	_ = conn.Close()
	if bad.dials.Load() != 1 || backup.dials.Load() != 1 {
		t.Fatalf("dial results: bad=%d backup=%d", bad.dials.Load(), backup.dials.Load())
	}
	if !health.downKey(bKey) || health.downKey(aKey) {
		t.Fatalf("health isolation: Bob down=%v, Alice down=%v", health.downKey(bKey), health.downKey(aKey))
	}
	if len(nodeEvents) != 1 || nodeEvents[0].Label != "shared" || !nodeEvents[0].Down {
		t.Fatalf("node health events = %+v, want down event for public label shared", nodeEvents)
	}
	if len(connEvents) < 2 || connEvents[0].Kind != ConnDialFailed || connEvents[0].Label != "shared" || connEvents[0].Hop != HopPre || connEvents[1].Kind != ConnSiteMoved || connEvents[1].From != "shared" || connEvents[1].Label != "backup" {
		t.Fatalf("connection events = %+v, want failure/move with public labels", connEvents)
	}
	dials := observer.dialSamples()
	if len(dials) != 4 || !dials[0].ok || dials[0].label != "shared" || !dials[1].ok || dials[1].label != "shared" || dials[2].ok || dials[2].label != "shared" || !dials[3].ok || dials[3].label != "backup" {
		t.Fatalf("observer dial labels/results = %+v", dials)
	}
}
