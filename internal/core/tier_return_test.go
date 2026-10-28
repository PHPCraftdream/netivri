package core

import (
	"testing"
	"time"
)

// tierReturnGroup is a sticky group over fake members, one per entry of tiers
// (named n0, n1, ...), with the binding table and the health table on one fake
// clock; every node starts up and has a delay.
func tierReturnGroup(t *testing.T, tiers []int, returnAfter time.Duration) (*stickyGroup, *affinityTable, *healthTable, *fakeClockNow) {
	t.Helper()
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	members := make([]member, len(tiers))
	labels := make([]string, len(tiers))
	for i := range tiers {
		labels[i] = "n" + string(rune('0'+i))
		members[i] = &fakeMember{name: labels[i]}
	}
	table := newAffinityTable(now, 0)
	table.setReturnAfter(returnAfter)
	health := newHealthTable()
	health.now = now
	g := newStickyGroupMembers("netivri-group-0", members, labels, table, health, nil)
	g.tiers = tiers
	for _, label := range labels {
		health.recordProbe(label, true, 50*time.Millisecond)
	}
	return g, table, health, clock
}

func nodeDown(health *healthTable, label string) {
	for i := 0; i < downDialFailures; i++ {
		health.recordDial(label, false, 0)
	}
}

func nodeUp(health *healthTable, label string) {
	health.recordProbe(label, true, 50*time.Millisecond)
}

func chosen(t *testing.T, g *stickyGroup, site string) int {
	t.Helper()
	index, _ := g.choose("alice", site)
	return index
}

func TestBindingReturnsToTheBetterTierOnceItWasStable(t *testing.T) {
	g, table, health, clock := tierReturnGroup(t, []int{0, 1}, 0)
	nodeDown(health, "n0")
	if got := chosen(t, g, "one.example"); got != 1 {
		t.Fatalf("site bound to %d while the first tier is down, want 1", got)
	}
	nodeUp(health, "n0")

	clock.advance(defaultTierReturnAfter - time.Second)
	if got := chosen(t, g, "one.example"); got != 1 {
		t.Fatalf("binding moved to %d before the better tier was stable, want 1", got)
	}
	clock.advance(2 * time.Second)
	if got := chosen(t, g, "one.example"); got != 0 {
		t.Fatalf("binding is on %d after the better tier was stable, want it back on 0", got)
	}
	if got := chosen(t, g, "one.example"); got != 0 {
		t.Fatalf("binding left the better tier again: %d", got)
	}
	if table.returnCount() != 1 || table.rebindCount() != 0 {
		t.Errorf("returns=%d rebinds=%d, want 1 and 0 (a new binding is no rebind)", table.returnCount(), table.rebindCount())
	}
}

func TestReturnTimerRestartsWhenTheBetterNodeFlaps(t *testing.T) {
	g, _, health, clock := tierReturnGroup(t, []int{0, 1}, time.Minute)
	nodeDown(health, "n0")
	chosen(t, g, "one.example") // binds to n1
	nodeUp(health, "n0")
	clock.advance(50 * time.Second)

	// n0 drops and comes back: the minute starts over.
	health.recordProbe("n0", false, 0)
	health.recordProbe("n0", false, 0)
	clock.advance(10 * time.Second)
	nodeUp(health, "n0")
	clock.advance(59 * time.Second)
	if got := chosen(t, g, "one.example"); got != 1 {
		t.Fatalf("binding moved to %d 59 s after the node came back, want it on 1", got)
	}
	clock.advance(2 * time.Second)
	if got := chosen(t, g, "one.example"); got != 0 {
		t.Fatalf("binding is on %d after a stable minute, want 0", got)
	}
}

func TestNoReturnWhileTheBetterTierIsDownOrWhenOff(t *testing.T) {
	g, table, health, clock := tierReturnGroup(t, []int{0, 1}, 0)
	nodeDown(health, "n0")
	chosen(t, g, "one.example")
	clock.advance(time.Hour)
	if got := chosen(t, g, "one.example"); got != 1 || table.returnCount() != 0 {
		t.Fatalf("bound to %d, returns=%d: a down better tier attracted the binding", got, table.returnCount())
	}

	off, offTable, offHealth, offClock := tierReturnGroup(t, []int{0, 1}, -1)
	nodeDown(offHealth, "n0")
	chosen(t, off, "one.example")
	nodeUp(offHealth, "n0")
	offClock.advance(time.Hour)
	if got := chosen(t, off, "one.example"); got != 1 || offTable.returnCount() != 0 {
		t.Fatalf("bound to %d, returns=%d with the return off", got, offTable.returnCount())
	}
}

func TestReturnGoesToTheBestStableTierStepByStep(t *testing.T) {
	g, table, health, clock := tierReturnGroup(t, []int{0, 1, 2}, time.Minute)
	nodeDown(health, "n0")
	nodeDown(health, "n1")
	if got := chosen(t, g, "one.example"); got != 2 {
		t.Fatalf("bound to %d, want the third tier 2", got)
	}
	nodeUp(health, "n1")
	clock.advance(2 * time.Minute)
	if got := chosen(t, g, "one.example"); got != 1 {
		t.Fatalf("bound to %d, want the second tier 1 (the first is still down)", got)
	}
	nodeUp(health, "n0")
	clock.advance(2 * time.Minute)
	if got := chosen(t, g, "one.example"); got != 0 {
		t.Fatalf("bound to %d, want the first tier 0", got)
	}
	if table.returnCount() != 2 {
		t.Errorf("returns = %d, want 2", table.returnCount())
	}
}

func TestOpenConnectionsAreNotTheBindingsBusiness(t *testing.T) {
	// A binding returns only for the next connection of the site: choose is
	// the only place that looks, and it does not touch anything open.
	g, _, health, clock := tierReturnGroup(t, []int{0, 1}, time.Minute)
	nodeDown(health, "n0")
	chosen(t, g, "one.example")
	nodeUp(health, "n0")
	clock.advance(2 * time.Minute)
	if got := g.peekMember("alice", "one.example"); got != 1 {
		t.Fatalf("peek moved the binding to %d; only a dial may move it", got)
	}
}

func TestBetterTierCheckDoesNotAllocate(t *testing.T) {
	g, _, health, _ := tierReturnGroup(t, []int{0, 0, 1, 1}, 0)
	nodeDown(health, "n0")
	nodeDown(health, "n1")
	chosen(t, g, "fallback.example") // bound in the second tier
	nodeUp(health, "n0")
	chosen(t, g, "home.example")
	if allocs := testing.AllocsPerRun(100, func() { g.choose("alice", "fallback.example") }); allocs != 0 {
		t.Errorf("a binding in a worse tier allocates %v times per dial", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { g.choose("alice", "home.example") }); allocs != 0 {
		t.Errorf("a binding in the first tier allocates %v times per dial", allocs)
	}
}

func TestSetReturnAfter(t *testing.T) {
	table := newAffinityTable(nil, 0)
	for _, c := range []struct{ in, want time.Duration }{
		{0, defaultTierReturnAfter}, {time.Minute, time.Minute}, {-time.Second, -1},
	} {
		table.setReturnAfter(c.in)
		if table.returnAfter != c.want {
			t.Errorf("setReturnAfter(%v) = %v, want %v", c.in, table.returnAfter, c.want)
		}
	}
}

func TestCarryHealthKeepsUpSince(t *testing.T) {
	clock, now := fakeClock(time.Unix(2_000_000, 0))
	old := newHealthTable()
	old.now = now
	old.register([]string{"p/a"})
	h := old.health("p/a")
	registered := h.upSince.Load()
	clock.advance(time.Hour)
	if !h.stableFor(clock.Now().UnixNano(), 30*time.Minute) {
		t.Fatal("a node up for an hour is not stable for 30 minutes")
	}
	carried := carryHealth(old, []string{"p/a", "p/new"}, nil)
	if got := carried.health("p/a").upSince.Load(); got != registered {
		t.Errorf("carried upSince = %d, want %d", got, registered)
	}
	nodeDown(old, "p/a")
	if old.health("p/a").stableFor(clock.Now().UnixNano(), 0) {
		t.Error("a down node counts as stable")
	}
	old.recordProbe("p/a", true, time.Millisecond)
	if got := old.health("p/a").upSince.Load(); got != clock.Now().UnixNano() {
		t.Errorf("upSince after coming back = %d, want the time it came back", got)
	}
}
