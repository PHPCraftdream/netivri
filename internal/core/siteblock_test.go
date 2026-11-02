package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

const blackholed = "149.154.167.51" // a destination the first node never reaches

// blockGroup is a sticky group of fake members, one per entry of tiers
// (n0, n1, ...), on one fake clock, whose connections are scripted so that
// they can be answered or stay silent; every node starts up and has a delay.
func blockGroup(t *testing.T, tiers []int) (*stickyGroup, *healthTable, *fakeClockNow, *eventSink) {
	t.Helper()
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	members := make([]member, len(tiers))
	labels := make([]string, len(tiers))
	for i := range tiers {
		labels[i] = "n" + string(rune('0'+i))
		members[i] = &fakeMember{name: labels[i], conn: func() C.Conn { return &scriptConn{} }}
	}
	table := newAffinityTable(now, 0)
	table.setReturnAfter(time.Minute)
	health := newHealthTable()
	health.now = now
	g := newStickyGroupMembers("netivri-group-0", members, labels, table, health, nil)
	g.tiers = tiers
	sink := &eventSink{}
	g.events = sink.add
	for _, label := range labels {
		health.recordProbe(label, true, 50*time.Millisecond)
	}
	return g, health, clock, sink
}

// connEnd says how a test connection ends.
type connEnd struct {
	answer bool          // the node answers the first bytes
	wait   time.Duration // how long it lives
	noPing bool          // no node passes a probe meanwhile (the network is out)
	down   bool          // the first node goes down meanwhile
}

var silent = connEnd{wait: 37 * time.Second}

// endConn opens a connection of alice to host, sends data, lets it live and
// closes it as end says.
func endConn(t *testing.T, g *stickyGroup, health *healthTable, clock *fakeClockNow, host string, end connEnd) {
	t.Helper()
	conn, err := g.DialContext(context.Background(), dialMetadata("alice", host))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("hello"))
	if end.answer {
		_, _ = conn.Read(make([]byte, 8))
	}
	clock.advance(end.wait)
	if !end.noPing {
		for _, label := range g.labels {
			nodeUp(health, label)
		}
	}
	if end.down {
		nodeDown(health, "n0")
	}
	_ = conn.Close()
}

func ofKind(sink *eventSink, kind string) []ConnEvent {
	var out []ConnEvent
	for _, ev := range sink.all() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestSilentSiteLeavesTheNodeAfterThreeSilentConnections(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	for i := 0; i < silentNeeded-1; i++ {
		endConn(t, g, health, clock, blackholed, silent)
		if n := g.blocks.blockedCount(); n != 0 {
			t.Fatalf("%d blocks after %d silent connections, want none yet", n, i+1)
		}
		if got := chosen(t, g, blackholed); got != 0 {
			t.Fatalf("site on %d before the block, want 0", got)
		}
	}
	endConn(t, g, health, clock, blackholed, silent)
	if n := g.blocks.blockedCount(); n != 1 {
		t.Fatalf("%d blocks after %d silent connections, want 1", n, silentNeeded)
	}
	blocks := ofKind(sink, ConnSiteBlocked)
	if len(blocks) != 1 || blocks[0].Label != "n0" || blocks[0].Block != siteBlockFirst || blocks[0].Target != blackholed+":443" || blocks[0].Login != "alice" {
		t.Fatalf("block events = %+v, want one: alice, n0, %s, %s:443", blocks, siteBlockFirst, blackholed)
	}

	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("blocked site chose %d, want the next tier 1", got)
	}
	moves := ofKind(sink, ConnSiteMoved)
	if len(moves) != 1 || moves[0].From != "n0" || moves[0].Label != "n1" || moves[0].Why != MoveNoAnswer {
		t.Fatalf("move events = %+v, want one n0 -> n1 for no answer", moves)
	}
	if got := chosen(t, g, "other.example"); got != 0 {
		t.Fatalf("another site chose %d, want it to stay on 0", got)
	}
	conn, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if n := g.members[1].(*fakeMember).dials.Load(); n != 1 {
		t.Errorf("n1 carried %d connections of the blocked site, want 1", n)
	}
}

func TestSilentBlockIsPerAccountAndSite(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	for i := 0; i < silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if got, _ := g.choose("bob", blackholed); got != 0 {
		t.Errorf("bob's site chose %d, want 0: the block is alice's", got)
	}
	if got := chosen(t, g, "149.154.167.52"); got != 0 {
		t.Errorf("a neighbour address chose %d, want 0: the block is for one site", got)
	}
}

func TestSiteThatWasAnsweredIsNotBlockedByStrayDeadConnections(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	endConn(t, g, health, clock, blackholed, connEnd{answer: true, wait: time.Second})
	for i := 0; i < silentNeeded+2; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if n := g.blocks.blockedCount(); n != 0 || len(ofKind(sink, ConnSiteBlocked)) != 0 {
		t.Fatalf("%d blocks for a site that answers", n)
	}
	if got := chosen(t, g, blackholed); got != 0 {
		t.Fatalf("site chose %d, want 0", got)
	}
}

func TestSilentConnectionsOfAClientThatLeftDoNotCount(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	for i := 0; i < 2*silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, connEnd{wait: silentMinLife - time.Second})
	}
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks from connections that lived under %s", n, silentMinLife)
	}
}

func TestNoBlockWhenTheNodeIsDownOrTheNetworkIsOut(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	for i := 0; i < 2*silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, connEnd{wait: 37 * time.Second, noPing: true})
	}
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks while no node answered anything (the network is out)", n)
	}
	for i := 0; i < 2*silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, connEnd{wait: 37 * time.Second, down: true})
		nodeUp(health, "n0")
	}
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks for connections that ended while the node was down", n)
	}
}

func TestBlockGrowsOnEveryRepeatAndStartsOverAfterAnAnswer(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	block := func(want time.Duration) {
		t.Helper()
		before := len(ofKind(sink, ConnSiteBlocked))
		for i := 0; i < silentNeeded; i++ {
			endConn(t, g, health, clock, blackholed, silent)
		}
		got := ofKind(sink, ConnSiteBlocked)
		if len(got) != before+1 || got[before].Block != want {
			t.Fatalf("block events = %+v, want a new one of %s", got, want)
		}
	}
	block(siteBlockFirst)
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("site chose %d during the block, want 1", got)
	}

	clock.advance(siteBlockFirst)
	if got := chosen(t, g, blackholed); got != 0 {
		t.Fatalf("site chose %d after the block ended, want it back on 0", got)
	}
	block(2 * siteBlockFirst)
	clock.advance(2*siteBlockFirst - time.Minute)
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("site chose %d a minute before the second block ended, want 1", got)
	}
	clock.advance(2 * time.Minute)
	if got := chosen(t, g, blackholed); got != 0 {
		t.Fatalf("site chose %d after the second block, want 0", got)
	}

	endConn(t, g, health, clock, blackholed, connEnd{answer: true, wait: time.Second})
	clock.advance(silentWindow + time.Minute)
	block(siteBlockFirst) // an answer in between starts the doubling over
}

func TestAnAnswerThroughTheBlockedNodeEndsTheBlock(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	// A connection opened before the block that gets its answer after it.
	early, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if g.blocks.blockedCount() != 1 {
		t.Fatal("no block to end")
	}
	_, _ = early.Write([]byte("hello"))
	_, _ = early.Read(make([]byte, 8))
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks after the site answered through the blocked node", n)
	}
	_ = early.Close()
}

func TestSilentConnectionsEndingInsideABlockAddNothing(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	var inFlight []C.Conn
	for i := 0; i < 2*silentNeeded; i++ {
		c, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("hello"))
		inFlight = append(inFlight, c)
	}
	clock.advance(37 * time.Second)
	for _, label := range g.labels {
		nodeUp(health, label)
	}
	for _, c := range inFlight {
		_ = c.Close()
	}
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 1 {
		t.Fatalf("%d block events from %d silent connections that ended together, want 1", len(got), len(inFlight))
	}
}

func TestBlockedOnEveryNodeStillPicksANode(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	for i := 0; i < silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("site chose %d, want 1", got)
	}
	for i := 0; i < silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if n := g.blocks.blockedCount(); n != 2 {
		t.Fatalf("%d blocks, want both nodes blocked", n)
	}
	if got := chosen(t, g, blackholed); got < 0 || got > 1 {
		t.Fatalf("site chose %d with every node blocked", got)
	}
	if _, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed)); err != nil {
		t.Fatalf("a dial with every node blocked failed: %v", err)
	}
}

func TestSiteBlocksRunOfSilenceStartsOverAfterTheWindow(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < silentNeeded-1; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false); blocked {
			t.Fatal("blocked too early")
		}
	}
	clock.advance(silentWindow + time.Minute)
	if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false); blocked {
		t.Fatal("silent connections from two different windows blocked a node")
	}
	for i := 0; i < silentNeeded-2; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false); blocked {
			t.Fatal("blocked too early in the new run")
		}
	}
	if ttl, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false); !blocked || ttl != siteBlockFirst {
		t.Fatalf("ttl, blocked = %s, %v, want %s, true", ttl, blocked, siteBlockFirst)
	}
}

func TestSiteBlocksBlockNeverExceedsTheMaximum(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	var last time.Duration
	for strike := 0; strike < 10; strike++ {
		var ttl time.Duration
		for i := 0; i < silentNeeded; i++ {
			if got, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false); blocked {
				ttl = got
			}
		}
		if ttl == 0 {
			t.Fatalf("strike %d made no block", strike)
		}
		last = ttl
		clock.advance(ttl + time.Second)
	}
	if last != siteBlockMax {
		t.Fatalf("the tenth block lasts %s, want the maximum %s", last, siteBlockMax)
	}
}

func TestSiteBlocksCapDropsNewEvidenceAndPrunesIdleRecords(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < siteBlockCap; i++ {
		b.silentEnd("a", fmt.Sprint("site", i), 0, 37*time.Second, false)
	}
	b.silentEnd("a", "one-more", 0, 37*time.Second, false)
	if got := b.n.Load(); got != siteBlockCap {
		t.Fatalf("%d records, want the cap %d", got, siteBlockCap)
	}
	b.mu.RLock()
	_, kept := b.m[blockKey{"a", "one-more", 0}]
	b.mu.RUnlock()
	if kept {
		t.Fatal("a record was added over the cap")
	}
	clock.advance(silentWindow + time.Minute)
	b.silentEnd("a", "one-more", 0, 37*time.Second, false)
	if got := b.n.Load(); got > siteBlockCap || got < 1 {
		t.Fatalf("%d records after pruning, want 1..%d", got, siteBlockCap)
	}
	b.mu.RLock()
	_, kept = b.m[blockKey{"a", "one-more", 0}]
	b.mu.RUnlock()
	if !kept {
		t.Fatal("the idle records were not pruned to make room")
	}
}

func TestSiteBlocksNilIsInert(t *testing.T) {
	var b *siteBlocks
	if b.any() || b.blocked("a", "s", 0, time.Now()) || b.blockedCount() != 0 {
		t.Fatal("a nil table blocks something")
	}
	b.answered("a", "s", 0)
}

func TestBindingRemembersWhichNodeAnsweredTheSite(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "s"}
	table.noteAnswer(key, 0, now()) // no binding yet: nothing is recorded
	if table.answeredWithin(key, 0, time.Hour, now()) {
		t.Fatal("an answer was recorded without a binding")
	}
	table.acquire(key, noDownMember, func() (int, bool) { return 0, false })
	table.noteAnswer(key, 0, now())
	if !table.answeredWithin(key, 0, time.Minute, now()) {
		t.Fatal("the answer is not remembered")
	}
	if table.answeredWithin(key, 1, time.Minute, now()) {
		t.Fatal("an answer of node 0 counts for node 1")
	}
	clock.advance(2 * time.Minute)
	if table.answeredWithin(key, 0, time.Minute, now()) {
		t.Fatal("an old answer still counts")
	}
}

func TestOKSinceIsStrictlyAfter(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	health := newHealthTable()
	health.now = now
	health.register([]string{"n0"})
	start := now()
	if health.health("n0").okSince(start) {
		t.Fatal("a node that never answered counts as answering")
	}
	nodeUp(health, "n0")
	if health.health("n0").okSince(start) {
		t.Fatal("an answer at the very moment counts as after it")
	}
	clock.advance(time.Second)
	nodeUp(health, "n0")
	if !health.health("n0").okSince(start) {
		t.Fatal("an answer after the start does not count")
	}
}
