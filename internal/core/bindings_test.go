package core

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// bindingClock returns an injectable now function over an atomic unix-nano
// value, plus setters for the tests.
func bindingClock(start time.Time) (*atomic.Int64, func() time.Time) {
	nanos := &atomic.Int64{}
	nanos.Store(start.UnixNano())
	return nanos, func() time.Time { return time.Unix(0, nanos.Load()) }
}

func TestSnapshotBindings(t *testing.T) {
	base := time.Unix(1700000000, 0)
	nanos, now := bindingClock(base)
	table := newAffinityTable(now, 0)

	pick := func(member int, provisional bool) func() (int, bool) {
		return func() (int, bool) { return member, provisional }
	}
	// alice/example.com -> member 0, live, non-provisional, created at base.
	table.acquire(affinityKey{login: "alice", site: "example.com"}, func(int) bool { return false }, pick(0, false))
	// alice/example.org -> provisional, created at base: never snapshotted.
	table.acquire(affinityKey{login: "alice", site: "example.org"}, func(int) bool { return false }, pick(1, true))
	// bob/stale.example -> non-provisional but last used 25 h before base:
	// expired at the snapshot time.
	nanos.Store(base.Add(-25 * time.Hour).UnixNano())
	table.acquire(affinityKey{login: "bob", site: "stale.example"}, func(int) bool { return false }, pick(2, false))
	nanos.Store(base.UnixNano())

	inst := instanceWith(&routingState{
		table:         table,
		accountRoutes: map[string]string{"alice": "p~DE", "bob": "p~DE"},
		routeLabels:   map[string][]string{"p~DE": {"p/n1", "p/n2", "p/n3"}},
	})
	got := inst.SnapshotBindings()
	if len(got) != 1 {
		t.Fatalf("SnapshotBindings = %+v, want only the live non-provisional binding", got)
	}
	r := got[0]
	if r.Login != "alice" || r.Site != "example.com" || r.Label != "p/n1" {
		t.Fatalf("SnapshotBindings[0] = %+v", r)
	}
	if !r.BoundAt.Equal(base) || !r.LastUsed.Equal(base) {
		t.Fatalf("SnapshotBindings[0] times = %v %v, want %v", r.BoundAt, r.LastUsed, base)
	}

	// A snapshot never refreshes lastUsed: after the clock moves past the
	// provisional lifetime (so the provisional binding is gone) the recorded
	// last_used of the live binding stays where it was last used.
	nanos.Store(base.Add(3 * time.Minute).UnixNano())
	got = inst.SnapshotBindings()
	if len(got) != 1 || !got[0].LastUsed.Equal(base) {
		t.Fatalf("SnapshotBindings refreshed lastUsed: %+v", got)
	}

	// Snapshot expiry: past the TTL nothing is live any more (alice's
	// last_used is base+1m by now, the provisional binding is long gone and
	// bob's is 25 h before that).
	nanos.Store(base.Add(25 * time.Hour).UnixNano())
	if got := inst.SnapshotBindings(); len(got) != 0 {
		t.Fatalf("SnapshotBindings after the TTL = %+v, want none", got)
	}

	// Without a table there is nothing.
	if got := (&Instance{}).SnapshotBindings(); got != nil {
		t.Fatalf("SnapshotBindings without a table = %+v", got)
	}
}

func TestLoadInitialBindings(t *testing.T) {
	base := time.Unix(1700000000, 0)
	nanos, now := bindingClock(base)
	ttl := defaultAffinityTTL

	o := Options{
		RouteNodes:   map[string][]map[string]any{"p~DE": {map[string]any{"name": "n1", "type": "socks5", "server": "192.0.2.10", "port": 1080}, map[string]any{"name": "n2", "type": "socks5", "server": "192.0.2.11", "port": 1080}}},
		NodeLabels:   map[string][]string{"p~DE": {"p/n1", "p/n2"}},
		AccountRoute: map[string]string{"alice": "p~DE"},
	}
	plans, err := buildRoutePlans(o)
	if err != nil {
		t.Fatalf("buildRoutePlans: %v", err)
	}
	table := newAffinityTable(now, ttl)
	// The stored last_used; the TTL continues from it, so the expiry checks
	// below are computed from storedLastUsed, not from base.
	storedLastUsed := base.Add(-time.Minute)
	loaded := loadInitialBindings(table, plans, o.AccountRoute, []BindingRecord{
		{Login: "alice", Site: "example.com", Label: "p/n2", BoundAt: base.Add(-time.Hour), LastUsed: storedLastUsed},
		// Expired by lastUsed + TTL.
		{Login: "alice", Site: "old.example", Label: "p/n1", BoundAt: base.Add(-48 * time.Hour), LastUsed: base.Add(-ttl - time.Minute)},
		// Unknown label, unknown login, empty fields.
		{Login: "alice", Site: "unknown.example", Label: "p/nope", BoundAt: base, LastUsed: base},
		{Login: "carol", Site: "example.com", Label: "p/n1", BoundAt: base, LastUsed: base},
		{Login: "alice", Site: "blank.example", Label: "", BoundAt: base, LastUsed: base},
	})
	if loaded != 1 {
		t.Fatalf("loaded = %d, want 1", loaded)
	}

	// The record for p/n2 is bound to member 1, keeps its own bound_at and
	// last_used, and a fresh pick would have chosen member 0.
	shard := table.shards[table.shardIndex("alice", "example.com")]
	shard.mu.RLock()
	b, ok := shard.bindings[affinityKey{login: "alice", site: "example.com"}]
	shard.mu.RUnlock()
	if !ok {
		t.Fatal("the record for p/n2 was not installed")
	}
	if b.member != 1 {
		t.Fatalf("member = %d, want 1 (p/n2)", b.member)
	}
	if !b.boundAt.Equal(base.Add(-time.Hour)) || b.lastUsed.Load() != base.Add(-time.Minute).UnixNano() {
		t.Fatalf("loaded binding kept bound_at %v last_used %v", b.boundAt, b.lastUsed.Load())
	}

	// The idle TTL continues from the stored last_used: peek does not
	// refresh, so it shows the binding dying exactly past the stored
	// last_used + TTL. Two minutes of margin on each side of the boundary:
	// lastUsed+TTL-2min is live, lastUsed+TTL+2min is expired.
	nanos.Store(storedLastUsed.Add(ttl - 2*time.Minute).UnixNano())
	if member, ok := table.peek(affinityKey{login: "alice", site: "example.com"}); !ok || member != 1 {
		t.Fatalf("peek at lastUsed+TTL-2min = %d %v, want the loaded member", member, ok)
	}
	nanos.Store(storedLastUsed.Add(ttl + 2*time.Minute).UnixNano())
	if _, ok := table.peek(affinityKey{login: "alice", site: "example.com"}); ok {
		t.Fatal("peek at lastUsed+TTL+2min still finds the binding")
	}

	// An expired record was never installed.
	if _, ok := table.peek(affinityKey{login: "alice", site: "old.example"}); ok {
		t.Fatal("an expired record was loaded")
	}
}

func TestLoadedBindingRePickedWhenDown(t *testing.T) {
	base := time.Unix(1700000000, 0)
	_, now := bindingClock(base)
	o := Options{
		RouteNodes:   map[string][]map[string]any{"p~DE": {map[string]any{"name": "n1", "type": "socks5", "server": "192.0.2.10", "port": 1080}}},
		NodeLabels:   map[string][]string{"p~DE": {"p/n1"}},
		AccountRoute: map[string]string{"alice": "p~DE"},
	}
	plans, err := buildRoutePlans(o)
	if err != nil {
		t.Fatalf("buildRoutePlans: %v", err)
	}
	table := newAffinityTable(now, 0)
	loadInitialBindings(table, plans, o.AccountRoute, []BindingRecord{
		{Login: "alice", Site: "example.com", Label: "p/n1", BoundAt: base, LastUsed: base},
	})
	down := map[int]bool{0: true}
	picked := 0
	member, _ := table.acquire(affinityKey{login: "alice", site: "example.com"},
		func(m int) bool { return down[m] },
		func() (int, bool) { picked++; return 0, false })
	if member != 0 || picked == 0 {
		t.Fatalf("acquire with the member down = %d (picks %d), want a re-pick of the only member", member, picked)
	}
}

func TestSnapshotWithFakeGroupMembers(t *testing.T) {
	base := time.Unix(1700000000, 0)
	_, now := bindingClock(base)
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	table := newAffinityTable(now, 0)
	health := newHealthTable()
	g := newStickyGroupMembers("netivri-group-0", []member{a, b}, []string{"p/a", "p/b"}, table, health, nil)
	// Seed delays so the pick is deterministic and the binding is not
	// provisional (both views are known), then dial to bind the site.
	health.recordProbe("p/a", true, 100*time.Millisecond)
	health.recordProbe("p/b", true, 50*time.Millisecond)
	if _, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com")); err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	inst := instanceWith(&routingState{
		table:         table,
		accountRoutes: map[string]string{"alice": "k"},
		routeLabels:   map[string][]string{"k": {"p/a", "p/b"}},
	})
	got := inst.SnapshotBindings()
	if len(got) != 1 || got[0].Label != "p/b" || got[0].Site != "example.com" {
		t.Fatalf("SnapshotBindings = %+v, want the example.com site on p/b", got)
	}
}

// TestLoadBindingRespectsShardCap fills one shard to its cap and checks that
// loading stops there.
func TestLoadBindingRespectsShardCap(t *testing.T) {
	base := time.Unix(1700000000, 0)
	_, now := bindingClock(base)
	table := newAffinityTable(now, 0)
	const keySite = "cap.example"
	target := table.shardIndex("alice", keySite)
	// Fill the target shard with keys that hash into it; the last one is the
	// cap-th binding, so the shard is exactly full.
	pick := func() (int, bool) { return 0, false }
	filled := 0
	for i := 0; filled < shardCapFor(table); i++ {
		site := fmt.Sprintf("fill-%d.example", i)
		if table.shardIndex("alice", site) != target {
			continue
		}
		table.acquire(affinityKey{login: "alice", site: site}, func(int) bool { return false }, pick)
		filled++
	}
	shard := table.shards[target]
	if len(shard.bindings) != shard.cap {
		t.Fatalf("shard holds %d bindings, want the cap %d", len(shard.bindings), shard.cap)
	}
	// A key of the same shard cannot be installed any more; a key of a
	// different shard still can.
	over := fmt.Sprintf("over-%d.example", overSiteIndex(table, "alice", target))
	if table.loadBinding(affinityKey{login: "alice", site: over}, 0, base, base) {
		t.Fatal("loadBinding installed a binding beyond the shard cap")
	}
	other := "other.example"
	if table.shardIndex("alice", other) == target {
		t.Fatal("test setup: other.example hashes into the same shard")
	}
	if !table.loadBinding(affinityKey{login: "alice", site: other}, 0, base, base) {
		t.Fatal("loadBinding refused an empty shard")
	}
}

// shardCapFor returns the per-shard binding cap.
func shardCapFor(table *affinityTable) int { return table.shards[0].cap }

// overSiteIndex returns the smallest i whose site hashes into target.
func overSiteIndex(table *affinityTable, login string, target int) int {
	for i := 0; ; i++ {
		if table.shardIndex(login, fmt.Sprintf("over-%d.example", i)) == target {
			return i
		}
	}
}
