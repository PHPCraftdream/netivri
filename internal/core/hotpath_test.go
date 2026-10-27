package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Table test pinning selectMember: expectations are hand-computed from the
// documented rules; near-equal ties use the independent fnv64aRef helper.
func TestSelectMemberTable(t *testing.T) {
	const login, site = "alice", "example.com"
	tests := []struct {
		name  string
		views []memberHealthView
		want  int
	}{
		{
			// Tier filter beats score: only tier 0 is considered, even
			// though the tier-1 member scores much better.
			name: "tier filter beats score",
			views: []memberHealthView{
				{Label: "t0", Known: true, Score: 100, DelayMS: 100, Tier: 0},
				{Label: "t1", Known: true, Score: 10, DelayMS: 10, Tier: 1},
			},
			want: 0,
		},
		{
			// Every tier-0 member is down, so the pool falls through to
			// tier 1, whose only member wins.
			name: "tier 0 all down falls through to tier 1",
			views: []memberHealthView{
				{Label: "a", Known: true, Score: 100, Tier: 0, Down: true},
				{Label: "b", Known: true, Score: 50, DelayMS: 50, Tier: 1},
			},
			want: 1,
		},
		{
			// Known (has delay data) beats unknown regardless of score.
			name: "known beats unknown",
			views: []memberHealthView{
				{Label: "a", Score: 1},
				{Label: "b", Known: true, Score: 100, DelayMS: 100},
			},
			want: 1,
		},
		{
			// Near-equal excludes far: tolerance is max(20, 15% of 100) =
			// 20, so 130 > 120 is out; n1 and n2 are tied by hash.
			name: "near-equal excludes far",
			views: []memberHealthView{
				{Label: "n1", Known: true, Score: 100, DelayMS: 100},
				{Label: "n2", Known: true, Score: 110, DelayMS: 110},
				{Label: "far", Known: true, Score: 130, DelayMS: 130},
			},
			want: -1, // computed below by hash
		},
		{
			// Same as the previous case, but n2 is down and so excluded
			// from the near set: n1 wins outright.
			name: "down member excluded from near set",
			views: []memberHealthView{
				{Label: "n1", Known: true, Score: 100, DelayMS: 100},
				{Label: "n2", Known: true, Score: 110, DelayMS: 110, Down: true},
				{Label: "far", Known: true, Score: 130, DelayMS: 130},
			},
			want: 0,
		},
		{
			// Everything is down: the fallback picks the lowest tier (0),
			// whose only member b wins.
			name: "all down picks lowest tier",
			views: []memberHealthView{
				{Label: "a", Known: true, Score: 10, Down: true, Tier: 1},
				{Label: "b", Known: true, Score: 5, Down: true, Tier: 0},
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		want := tt.want
		if want == -1 {
			// Near-equal tie between n1 and n2: the higher
			// fnv64a(login|site|label) wins.
			want = 0
			if fnv64aRef(login+"|"+site+"|n2") > fnv64aRef(login+"|"+site+"|n1") {
				want = 1
			}
		}
		if got := selectMember(tt.views, login, site); got != want {
			t.Fatalf("%s: selectMember = %d, want %d", tt.name, got, want)
		}
	}
}

// The hit path of acquire takes only the shard read lock and must not
// allocate or call pick.
func TestAffinityTableAcquireHitAllocatesNothing(t *testing.T) {
	_, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, time.Hour)
	key := affinityKey{login: "alice", site: "example.com"}
	if m, rebound := table.acquire(key, noDownMember, func() (int, bool) { return 2, false }); m != 2 || rebound {
		t.Fatalf("initial acquire = (%d, %v), want (2, false)", m, rebound)
	}
	picks := 0
	var member int
	var rebound bool
	allocs := testing.AllocsPerRun(100, func() {
		member, rebound = table.acquire(key, noDownMember, func() (int, bool) { picks++; return 2, false })
	})
	if allocs != 0 {
		t.Fatalf("acquire hit allocates %v times, want 0", allocs)
	}
	if member != 2 || rebound {
		t.Fatalf("acquire hit = (%d, %v), want (2, false)", member, rebound)
	}
	if picks != 0 {
		t.Fatal("the hit path must not call pick")
	}
}

// Concurrent failure records leave the node down; one success afterwards
// clears both consecutive counters and brings it back.
func TestNodeHealthConcurrentRecordsDeterministicOutcome(t *testing.T) {
	var h nodeHealth
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				h.recordDial(false, 0)
				h.recordProbe(false, 0)
			}
		}()
	}
	wg.Wait()
	if !h.Down() {
		t.Fatal("node with only failures is not down")
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.recordProbe(true, time.Millisecond)
		}()
	}
	wg.Wait()
	if h.Down() {
		t.Fatal("a success must clear both consecutive failure counters")
	}
}

// Concurrent sticky hits interleaved with successful dial records never
// rebind and never create extra bindings.
func TestStickyGroupConcurrentChooseWithRecording(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	if m, rebound := g.choose("u", "example.com"); m != 1 || rebound {
		t.Fatalf("initial choose = (%d, %v), want (1, false)", m, rebound)
	}
	var mismatch atomic.Bool
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			if m, rebound := g.choose("u", "example.com"); m != 1 || rebound {
				mismatch.Store(true)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			health.recordDial("a", true, time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			health.recordDial("b", true, time.Millisecond)
		}
	}()
	wg.Wait()
	if mismatch.Load() {
		t.Fatal("a sticky hit moved or rebound under concurrent recording")
	}
	if got := table.rebindCount(); got != 0 {
		t.Fatalf("rebinds = %d, want 0", got)
	}
	if got := table.count(); got != 1 {
		t.Fatalf("bindings = %d, want 1", got)
	}
}

// Distinct fresh keys under concurrency each install exactly one binding.
func TestStickyGroupConcurrentFreshKeysCount(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	var wg sync.WaitGroup
	for id := 0; id < 4; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				g.choose(fmt.Sprintf("u%d-%d", id, j), "example.com")
			}
		}(id)
	}
	wg.Wait()
	if got := table.count(); got != 100 {
		t.Fatalf("bindings = %d, want 100", got)
	}
}

// recordWithin runs fn in a goroutine and fails the test if it does not
// return quickly; it pins that records return promptly.
func recordWithin(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s on a success deadlocked", name)
	}
}

// A success must clear BOTH consecutive counters: after one probe failure
// plus a success, a single new failure must not mark the node down.
func TestNodeHealthSuccessClearsCounters(t *testing.T) {
	var h nodeHealth
	h.recordProbe(false, 0)
	if h.Down() {
		t.Fatal("one probe failure must not be down")
	}
	recordWithin(t, "recordProbe", func() { h.recordProbe(true, 100*time.Millisecond) })
	h.recordProbe(false, 0)
	if h.Down() {
		t.Fatal("one failure after a success must not be down: the success did not clear the counter")
	}
	// The same through dial failures cleared by a probe success.
	var d nodeHealth
	d.recordDial(false, 0)
	d.recordDial(false, 0)
	if d.Down() {
		t.Fatal("two dial failures must not be down")
	}
	recordWithin(t, "recordProbe", func() { d.recordProbe(true, time.Millisecond) })
	d.recordDial(false, 0)
	if d.Down() {
		t.Fatal("one dial failure after a probe success must not be down")
	}
}

// Mixed concurrent records on one node, then one success: the node must be
// up, the failure rate a valid fraction, and a fresh streak of exactly two
// probe failures must take it down.
func TestNodeHealthConcurrentMixedThenRecover(t *testing.T) {
	var h nodeHealth
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if (i+j)%3 == 0 {
					h.recordProbe(true, time.Millisecond)
				} else {
					h.recordDial(false, 0)
				}
			}
		}(i)
	}
	wg.Wait()
	h.recordProbe(true, time.Millisecond)
	if h.Down() {
		t.Fatal("the success after mixed records must clear the down state")
	}
	if r := h.failRate(); r < 0 || r > 1 {
		t.Fatalf("failRate = %v, want within [0,1]", r)
	}
	h.recordProbe(false, 0)
	h.recordProbe(false, 0)
	if !h.Down() {
		t.Fatal("two fresh probe failures must mark the node down")
	}
}

// The announce hook must run with every lock released: it reads the node's
// health view and records a successful dial on another node, both of which
// take locks and would self-deadlock if any were held.
func TestHealthAnnounceRunsAfterUnlock(t *testing.T) {
	table := newHealthTable()
	table.register([]string{"p/a", "p/b"})
	done := make(chan struct{})
	table.entries["p/a"].notify = func(ev NodeEvent) {
		_ = table.health(ev.Label).view(ev.Label)
		table.recordDial("p/b", true, time.Millisecond)
		close(done)
	}
	go func() {
		table.recordProbe("p/a", false, 0)
		table.recordProbe("p/a", false, 0)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the announce hook deadlocked or never ran")
	}
	if !table.down("p/a") {
		t.Fatal("two consecutive probe failures did not mark p/a down")
	}
}

// Concurrent hits that race with a rebind (the write-lock slow path) must
// not deadlock: the read-lock hit path releases its lock before the slow
// path takes the write lock.
func TestAffinityConcurrentHitsWithRebindNoDeadlock(t *testing.T) {
	_, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, time.Hour)
	key := affinityKey{login: "alice", site: "example.com"}
	if m, rebound := table.acquire(key, noDownMember, func() (int, bool) { return 1, false }); m != 1 || rebound {
		t.Fatalf("initial acquire = (%d, %v), want (1, false)", m, rebound)
	}
	down := atomic.Bool{}
	isDown := func(member int) bool { return member == 1 && down.Load() }
	pick := func() (int, bool) { return 2, false }

	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				table.acquire(key, isDown, pick)
			}
		}()
	}
	go func() {
		defer wg.Done()
		down.Store(true)
		table.acquire(key, isDown, pick) // a real rebind while hits run
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent hits and a rebind deadlocked")
	}
	// Exactly one real member change happened: the site moved 1 -> 2 once.
	if got := table.rebindCount(); got < 1 {
		t.Fatalf("rebinds = %d, want at least 1", got)
	}
	if got := table.count(); got != 1 {
		t.Fatalf("bindings = %d, want 1", got)
	}
}
