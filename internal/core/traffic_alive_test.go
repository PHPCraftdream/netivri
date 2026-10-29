package core

import (
	"errors"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

func trafficHealth(t *testing.T) (*healthTable, *fakeClockNow, *[]NodeEvent) {
	t.Helper()
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	table := newHealthTable()
	table.now = now
	var events []NodeEvent
	table.notify = func(ev NodeEvent) { events = append(events, ev) }
	table.register([]string{"a"})
	table.recordProbe("a", true, 50*time.Millisecond)
	return table, clock, &events
}

func TestFailedProbesDoNotCountWhileDataFlows(t *testing.T) {
	table, clock, _ := trafficHealth(t)
	table.noteTraffic("a")
	for i := 0; i < 5; i++ {
		clock.advance(10 * time.Second)
		table.recordProbe("a", false, 0)
	}
	if table.down("a") {
		t.Fatal("a node that moves data went down on failed probes")
	}
	// The data stops: the traffic stops vouching after trafficAliveFor, and
	// two failed probes then count as before.
	clock.advance(trafficAliveFor)
	table.recordProbe("a", false, 0)
	if table.down("a") {
		t.Fatal("down after one failed probe")
	}
	table.recordProbe("a", false, 0)
	if !table.down("a") {
		t.Fatal("two failed probes without traffic did not bring the node down")
	}
}

func TestTrafficBringsAProbeDownNodeBack(t *testing.T) {
	table, clock, events := trafficHealth(t)
	table.recordProbe("a", false, 0)
	table.recordProbe("a", false, 0)
	if !table.down("a") {
		t.Fatal("node not down after two failed probes")
	}
	clock.advance(30 * time.Second)
	table.noteTraffic("a")
	if table.down("a") {
		t.Fatal("a node moving data stayed down")
	}
	got := *events
	if len(got) != 2 || !got[0].Down || got[1].Down || got[1].Downtime != 30*time.Second {
		t.Fatalf("events = %+v, want down then back after 30s", got)
	}
}

func TestTrafficDoesNotHideFailedDials(t *testing.T) {
	table, _, _ := trafficHealth(t)
	table.noteTraffic("a")
	nodeDown(table, "a") // three failed dials
	if !table.down("a") {
		t.Fatal("failed dials were ignored because data flows")
	}
	table.noteTraffic("a")
	if !table.down("a") {
		t.Fatal("traffic cleared the dial failures")
	}
}

func TestCarryHealthKeepsTraffic(t *testing.T) {
	table, clock, _ := trafficHealth(t)
	table.noteTraffic("a")
	carried := carryHealth(table, []string{"a"}, nil)
	carried.now = table.now
	clock.advance(time.Second)
	carried.recordProbe("a", false, 0)
	carried.recordProbe("a", false, 0)
	if carried.down("a") {
		t.Fatal("the traffic time was lost on reconfigure")
	}
}

func TestThroughputPollerNotesTraffic(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	health := newHealthTable()
	health.now = now
	health.register([]string{"a"})
	health.recordProbe("a", true, 50*time.Millisecond)
	poller := newThroughputPoller(nil, health, nil, map[string]string{"netivri-r0-0": "a"}, nil)
	base := time.Unix(1_000_000, 0)
	tracker := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{
		Chain: C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start: base.Add(-time.Minute),
	}}
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{tracker}}

	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second) // baseline
	clock.advance(5 * time.Second)
	health.recordProbe("a", false, 0)
	health.recordProbe("a", false, 0)
	if !health.down("a") {
		t.Fatal("setup: node should be down by two failed probes")
	}
	// A few bytes come back, far below the throughput sample gate.
	tracker.info.DownloadTotal.Add(512)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)
	if health.down("a") {
		t.Fatal("data came back through the node but it stayed down")
	}
}

func dialFail(table *healthTable, label, site string, err error) {
	table.recordDialAt(label, site, err, 0)
}

func TestFailedDialsToTwoSitesBringANodeDown(t *testing.T) {
	table, _, events := trafficHealth(t)
	boom := errors.New("connection refused")
	dialFail(table, "a", "x.example", boom)
	dialFail(table, "a", "x.example", boom)
	dialFail(table, "a", "x.example", boom)
	if table.down("a") {
		t.Fatal("three failed dials to one site brought the node down")
	}
	dialFail(table, "a", "y.example", boom)
	if !table.down("a") {
		t.Fatal("failed dials to two sites did not bring the node down")
	}
	got := *events
	if len(got) != 1 || got[0].Reason != "4 dial failures (connection refused)" {
		t.Fatalf("events = %+v, want one down with the kind", got)
	}
}

func TestFailedDialsOfOneSiteNeedTenToBringANodeDown(t *testing.T) {
	table, _, _ := trafficHealth(t)
	boom := errors.New("connection refused")
	for i := 0; i < downDialFailuresOneSite-1; i++ {
		dialFail(table, "a", "dead.example", boom)
	}
	if table.down("a") {
		t.Fatal("nine failed dials to one site brought the node down")
	}
	dialFail(table, "a", "dead.example", boom)
	if !table.down("a") {
		t.Fatal("ten failed dials to one site did not bring the node down")
	}
}

func TestDialFailuresWithoutASiteCountAsBefore(t *testing.T) {
	table, _, _ := trafficHealth(t)
	nodeDown(table, "a")
	if !table.down("a") {
		t.Fatal("three failed dials with no site did not bring the node down")
	}
}

func TestSuccessForgetsTheSitesOfFailedDials(t *testing.T) {
	table, _, _ := trafficHealth(t)
	boom := errors.New("timeout")
	dialFail(table, "a", "x.example", boom)
	dialFail(table, "a", "y.example", boom)
	table.recordDial("a", true, time.Millisecond)
	dialFail(table, "a", "x.example", boom)
	dialFail(table, "a", "x.example", boom)
	dialFail(table, "a", "x.example", boom)
	if table.down("a") {
		t.Fatal("the sites of failures before a success were remembered")
	}
}

func TestDialFailureKindIsAFixedWord(t *testing.T) {
	table, _, events := trafficHealth(t)
	odd := errors.New("dial tcp secret-host: weird failure of kind xyz")
	for _, site := range []string{"a.example", "b.example", "c.example"} {
		dialFail(table, "a", site, odd)
	}
	got := *events
	if len(got) != 1 || got[0].Reason != "3 dial failures (error)" {
		t.Fatalf("events = %+v, want reason with the bare kind 'error'", got)
	}
}
