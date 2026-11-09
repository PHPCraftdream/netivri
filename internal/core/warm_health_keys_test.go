package core

import (
	"context"
	"errors"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

func TestProbeGroupRecordsOncePerHealthRecord(t *testing.T) {
	sharedKey := healthMemberKey("p/shared", "", "")
	distinctKey := healthMemberKey("p/distinct", "", "")
	nameKeys := map[string]string{
		"netivri-g0-r0-n0": sharedKey,
		"netivri-g0-r1-n0": sharedKey,
		"netivri-g0-r2-n0": distinctKey,
	}
	nameLabels := map[string]string{
		"netivri-g0-r0-n0": "p/shared",
		"netivri-g0-r1-n0": "p/shared",
		"netivri-g0-r2-n0": "p/distinct",
	}
	health := newHealthTable()
	health.registerMembers([]healthMember{
		{key: sharedKey, label: "p/shared"},
		{key: distinctKey, label: "p/distinct"},
	})
	var callbacks, errorsSeen, physical int
	w := &warmer{
		options: WarmOptions{
			ProbeInterval:    time.Minute,
			ProbeTimeout:     time.Second,
			ProbeURL:         "http://probe.invalid/",
			ProbeConcurrency: 1,
			Same:             func(string) string { return "one-physical-probe" },
			OnProbe: probeHookKeys(nameLabels, nameKeys, nil, nil, func(string, bool, time.Duration) {
				callbacks++
			}),
			OnProbeError: probeErrorHookKeys(nameLabels, nameKeys, nil, func(string, error) {
				errorsSeen++
			}),
			onProbeGroup: probeGroupHealthHook(nameKeys, health),
		},
		clock: testWarmClock{now: time.Unix(100, 0)},
		proxies: []C.Proxy{
			testWarmProxy{name: "netivri-g0-r0-n0", typ: C.Socks5},
			testWarmProxy{name: "netivri-g0-r1-n0", typ: C.Socks5},
			testWarmProxy{name: "netivri-g0-r2-n0", typ: C.Socks5},
		},
	}
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		physical++
		return 0, errors.New("connection refused")
	}

	w.probeAll(context.Background())
	if physical != 1 || callbacks != 3 || errorsSeen != 3 {
		t.Fatalf("first round physical/callback/error counts = %d/%d/%d, want 1/3/3", physical, callbacks, errorsSeen)
	}
	if got := health.health(sharedKey).consProbeFail; got != 1 || health.downKey(sharedKey) {
		t.Fatalf("shared health after first physical failure: failures=%d down=%v, want 1/false", got, health.downKey(sharedKey))
	}
	if got := health.health(distinctKey).consProbeFail; got != 1 || health.downKey(distinctKey) {
		t.Fatalf("distinct health after first physical failure: failures=%d down=%v, want 1/false", got, health.downKey(distinctKey))
	}

	w.probeAll(context.Background())
	if physical != 2 || callbacks != 6 || errorsSeen != 6 {
		t.Fatalf("second round physical/callback/error counts = %d/%d/%d, want 2/6/6", physical, callbacks, errorsSeen)
	}
	if got := health.health(sharedKey).consProbeFail; got != 2 || !health.downKey(sharedKey) {
		t.Fatalf("shared health after second physical failure: failures=%d down=%v, want 2/true", got, health.downKey(sharedKey))
	}
	if got := health.health(distinctKey).consProbeFail; got != 2 || !health.downKey(distinctKey) {
		t.Fatalf("distinct health after second physical failure: failures=%d down=%v, want 2/true", got, health.downKey(distinctKey))
	}
	stats := w.Stats()
	if stats.Probes != 6 || stats.ProbeFailures != 6 || len(stats.Nodes) != 3 {
		t.Fatalf("per-proxy warm stats changed by physical deduplication: %+v", stats)
	}
}

func TestHealthKeysIsolateCarryBackoffAndWarmStatus(t *testing.T) {
	oldKey := healthMemberKey("p/node", "socks5://pre-a.example:1080", "")
	newKey := healthMemberKey("p/node", "socks5://pre-b.example:1080", "")
	old := newHealthTable()
	old.registerMembers([]healthMember{{key: oldKey, label: "p/node"}})
	old.recordProbeKey(oldKey, false, 0)
	old.recordProbeKey(oldKey, false, 0)
	if !old.downKey(oldKey) {
		t.Fatal("setup: old health record should be down")
	}
	changed := carryHealthMembers(old, []healthMember{{key: newKey, label: "p/node"}}, nil)
	if changed.downKey(newKey) {
		t.Fatal("health for the same label but a different hop was carried")
	}
	retained := carryHealthMembers(old, []healthMember{{key: oldKey, label: "p/node"}}, nil)
	if !retained.downKey(oldKey) {
		t.Fatal("health for a retained member key was not carried")
	}

	due := time.Unix(200, 0)
	oldSchedules := map[string]probeSchedule{"old-name": {downRounds: 2, nextDue: due}}
	if got := carryBackoff(oldSchedules, map[string]string{"old-name": oldKey}, map[string]string{"new-name": newKey}); len(got) != 0 {
		t.Fatalf("backoff crossed health keys: %v", got)
	}
	if got := carryBackoff(oldSchedules, map[string]string{"old-name": oldKey}, map[string]string{"new-name": oldKey}); got["new-name"] != oldSchedules["old-name"] {
		t.Fatalf("backoff for a retained health key was lost: %v", got)
	}

	clock, now := fakeClock(time.Unix(300, 0))
	health := newHealthTable()
	health.now = now
	labels := map[string]string{"member-a": "p/node", "member-b": "p/node"}
	keys := map[string]string{"member-a": oldKey, "member-b": newKey}
	health.registerMembers([]healthMember{{key: oldKey, label: "p/node"}, {key: newKey, label: "p/node"}})
	poller := newThroughputPollerWithHealthKeys(nil, health, nil, labels, keys, nil)
	base := time.Unix(300, 0)
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{
		&fakeTracker{id: "large", info: &statistic.TrackerInfo{Chain: C.Chain{"member-a"}, Start: base.Add(time.Second)}},
		&fakeTracker{id: "small", info: &statistic.TrackerInfo{Chain: C.Chain{"member-b"}, Start: base.Add(time.Second)}},
	}}
	poller.source.(*fakeTrackerSource).trackers[0].(*fakeTracker).info.DownloadTotal.Add(1 << 20)
	poller.source.(*fakeTrackerSource).trackers[1].(*fakeTracker).info.DownloadTotal.Add(32 << 10)
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	if !health.health(oldKey).hasThroughput || health.health(newKey).hasThroughput {
		t.Fatalf("throughput was not isolated by health key: old=%v new=%v", health.health(oldKey).hasThroughput, health.health(newKey).hasThroughput)
	}

	clock.advance(trafficAliveFor + time.Second)
	health.recordProbeKey(oldKey, false, 0)
	health.recordProbeKey(oldKey, false, 0)
	nodes := []NodeHealth{{Name: "member-a"}, {Name: "member-b"}}
	annotateDown(nodes, health, keys)
	if !nodes[0].Down || nodes[1].Down {
		t.Fatalf("warm status down flags = %v/%v, want true/false", nodes[0].Down, nodes[1].Down)
	}
	poller.cancel()
}
