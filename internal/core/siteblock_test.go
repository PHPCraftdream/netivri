package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"syscall"
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

func setNodeLastOK(h *nodeHealth, at time.Time) {
	h.mu.Lock()
	h.lastOK = at
	h.mu.Unlock()
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

// Connections that all end together, with no sign of life of the node after the
// first of them ended, may be a network outage: no block until the node answers
// something afterwards. Then one more silent end makes it, and the rest that end
// inside the block add nothing.
func TestSilentBurstWaitsForTheNodeToAnswerAfterItsFirstEnd(t *testing.T) {
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
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 0 {
		t.Fatalf("%d blocks from a burst that ended with no answer from the node after it", len(got))
	}
	clock.advance(time.Second)
	endConn(t, g, health, clock, blackholed, silent)
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 1 {
		t.Fatalf("%d block events after the node answered and one more silent end, want 1", len(got))
	}
}

func TestSiteBlocksSilentEndInsideABlockAddsNothing(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < silentNeeded; i++ {
		b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second))
	}
	if b.blockedCount() != 1 {
		t.Fatal("no block")
	}
	if ttl, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); blocked || ttl != 0 {
		t.Fatalf("a silent end inside a block made another: %s, %v", ttl, blocked)
	}
}

func TestSiteBlocksNeedANodeAnswerAfterTheFirstSilentEnd(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	before := now() // the node last answered at this very moment
	for i := 0; i < silentNeeded+2; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, before); blocked {
			t.Fatal("blocked with no answer of the node after the first silent end")
		}
	}
	if ttl, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, before.Add(time.Second)); !blocked || ttl != siteBlockFirst {
		t.Fatalf("ttl, blocked = %s, %v after the node answered, want %s, true", ttl, blocked, siteBlockFirst)
	}
}

// A connection that sat idle and was cancelled a second after its first bytes
// did not wait for an answer.
func TestIdleTimeBeforeTheFirstBytesIsNotWaitingForAnAnswer(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	for i := 0; i < 2*silentNeeded; i++ {
		conn, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
		if err != nil {
			t.Fatal(err)
		}
		clock.advance(30 * time.Second) // idle
		_, _ = conn.Write([]byte("hello"))
		clock.advance(time.Second)
		for _, label := range g.labels {
			nodeUp(health, label)
		}
		_ = conn.Close()
	}
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks from connections that waited one second for an answer", n)
	}
}

// A connection that goes on being answered keeps the site known to work, long
// after its first bytes.
func TestALongAnsweredConnectionKeepsTheSiteFromBeingBlocked(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	long, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = long.Write([]byte("hello"))
	for i := 0; i < 20; i++ { // 20 minutes of data coming back
		_, _ = long.Read(make([]byte, 8))
		clock.advance(time.Minute)
	}
	for i := 0; i < 2*silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if n := g.blocks.blockedCount(); n != 0 {
		t.Fatalf("%d blocks for a site whose connection is still being answered", n)
	}
	_ = long.Close()
}

// While the first node is blocked for a site the site stays on the second one:
// the better tier does not attract it back, and the second node, which keeps
// answering, is not blocked by the dead connections that come after.
func TestBlockedSiteStaysOnTheSecondNodeAndKeepsWhatItRemembers(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	for i := 0; i < silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, silent)
	}
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("site chose %d, want 1", got)
	}
	clock.advance(10 * time.Minute) // the first node has been up for long, the block lasts 30
	endConn(t, g, health, clock, blackholed, connEnd{answer: true, wait: time.Second})
	for i := 0; i < 2*silentNeeded; i++ {
		endConn(t, g, health, clock, blackholed, connEnd{wait: 37 * time.Second})
		if got := chosen(t, g, blackholed); got != 1 {
			t.Fatalf("site chose %d, want it to stay on 1", got)
		}
	}
	if n := g.blocks.blockedCount(); n != 1 {
		t.Fatalf("%d blocks, want only the first node blocked", n)
	}
	if moves := ofKind(sink, ConnSiteMoved); len(moves) != 1 {
		t.Errorf("%d moves, want the one away from the blocked node: %+v", len(moves), moves)
	}
	if g.table.returnCount() != 0 {
		t.Errorf("%d returns to the better tier while it is blocked", g.table.returnCount())
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
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); blocked {
			t.Fatal("blocked too early")
		}
	}
	clock.advance(silentWindow + time.Minute)
	if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); blocked {
		t.Fatal("silent connections from two different windows blocked a node")
	}
	for i := 0; i < silentNeeded-2; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); blocked {
			t.Fatal("blocked too early in the new run")
		}
	}
	if ttl, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); !blocked || ttl != siteBlockFirst {
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
			if got, blocked := b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second)); blocked {
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
		b.silentEnd("a", fmt.Sprint("site", i), 0, 37*time.Second, false, now().Add(time.Second))
	}
	b.silentEnd("a", "one-more", 0, 37*time.Second, false, now().Add(time.Second))
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
	b.silentEnd("a", "one-more", 0, 37*time.Second, false, now().Add(time.Second))
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

func TestLastOKAtIsWhenTheNodeLastAnswered(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	health := newHealthTable()
	health.now = now
	health.register([]string{"n0"})
	if !health.health("n0").lastOKAt().IsZero() {
		t.Fatal("a node that never answered has an answer time")
	}
	nodeUp(health, "n0")
	first := now()
	clock.advance(time.Second)
	nodeUp(health, "n0")
	if got := health.health("n0").lastOKAt(); !got.After(first) {
		t.Fatalf("last ok = %v, want after %v", got, first)
	}
}

func TestSamePickKeepsTheBindingAndItsAnswerMemory(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "s"}
	pick := func() (int, bool) { return 0, false }
	table.acquire(key, noDownMember, pick)
	table.noteAnswer(key, 0, now())
	allDown := func(int) bool { return true }
	if member, rebound := table.acquire(key, allDown, pick); member != 0 || rebound {
		t.Fatalf("member, rebound = %d, %v, want 0, false", member, rebound)
	}
	if !table.answeredWithin(key, 0, time.Minute, now()) {
		t.Fatal("the binding lost its answer memory when the pick kept the node")
	}
	if table.rebindCount() != 0 {
		t.Fatalf("%d rebinds, want 0", table.rebindCount())
	}
}

func TestDialTimeoutBlocksOnceWithTargetDetails(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	g.blocks.minLife = 0
	md := dialMetadata("alice", "timeout.example")
	md.DstPort = 8443
	f := &timeoutMember{fakeMember: g.members[0].(*fakeMember), during: func() { clock.advance(time.Nanosecond); nodeUp(health, "n0") }}
	f.err = &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	g.members[0] = f
	for i := 0; i < silentNeeded; i++ {
		c, e := g.DialContext(context.Background(), md)
		if c != nil || !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatalf("dial=%v %v", c, e)
		}
		clock.advance(time.Second)
	}
	got := ofKind(sink, ConnSiteBlocked)
	if len(got) != 1 || got[0].Label != "n0" || got[0].Target != "timeout.example:8443" || got[0].Port != 8443 || got[0].Why != MoveNoAnswer || got[0].Site == 0 {
		t.Fatalf("events=%+v", got)
	}
	if got := chosen(t, g, "timeout.example"); got != 1 {
		t.Fatalf("route=%d, want 1", got)
	}
}

type timeoutMember struct {
	*fakeMember
	during func()
}

func (m *timeoutMember) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	m.dials.Add(1)
	m.during()
	return nil, m.err
}

func TestRecordDialTimeoutUsesIPTargetFallback(t *testing.T) {
	g, _, clock, sink := blockGroup(t, []int{0})
	g.blocks.minLife = 0
	md := &C.Metadata{DstIP: netip.MustParseAddr("2001:db8::7"), DstPort: 443}
	for i := 0; i < silentNeeded; i++ {
		start := clock.now
		setNodeLastOK(g.healths[0], start.Add(time.Nanosecond))
		g.recordDialTimeout("alice", "v6", 0, "n0", md, start, time.Second)
		clock.advance(time.Second)
	}
	got := ofKind(sink, ConnSiteBlocked)
	if len(got) != 1 || got[0].Target != "[2001:db8::7]:443" {
		t.Fatalf("events=%+v", got)
	}
}

func TestDialTimeoutDoesNotCountBelowMinimumLife(t *testing.T) {
	g, _, clock, sink := blockGroup(t, []int{0})
	g.blocks.minLife = time.Hour
	md := dialMetadata("alice", "short.example")
	for i := 0; i < silentNeeded; i++ {
		start := clock.now
		setNodeLastOK(g.healths[0], start.Add(time.Nanosecond))
		g.recordDialTimeout("alice", "short", 0, "n0", md, start, time.Second)
		if g.blocks.any() {
			t.Fatal("short-lived evidence was recorded")
		}
		clock.advance(time.Second)
	}
	start := clock.now
	setNodeLastOK(g.healths[0], start.Add(time.Hour))
	g.recordDialTimeout("alice", "short", 0, "n0", md, start, time.Hour)
	if !g.blocks.any() {
		t.Fatal("evidence at minLife was not recorded")
	}
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 0 {
		t.Fatalf("events=%+v", got)
	}
}

func TestDialTimeoutDoesNotCountWithoutLaterNodeAnswer(t *testing.T) {
	g, _, clock, sink := blockGroup(t, []int{0})
	g.blocks.minLife = 0
	md := dialMetadata("alice", "outage.example")
	start := clock.now
	setNodeLastOK(g.healths[0], start)
	for i := 0; i < silentNeeded+1; i++ {
		g.recordDialTimeout("alice", "outage", 0, "n0", md, start, time.Second)
		clock.advance(time.Second)
	}
	if len(ofKind(sink, ConnSiteBlocked)) != 0 {
		t.Fatal("blocked without later node answer")
	}
}

func TestDialTimeoutEvidenceRequiresAnswerAfterStart(t *testing.T) {
	g, _, clock, _ := blockGroup(t, []int{0})
	g.blocks.minLife = 0
	start := clock.now
	setNodeLastOK(g.healths[0], start)
	md := dialMetadata("alice", "no-later-answer.example")
	for i := 0; i < silentNeeded+1; i++ {
		g.recordDialTimeout("alice", "same", 0, "n0", md, start, time.Second)
		clock.advance(time.Second)
	}
	if g.blocks.any() {
		t.Fatal("timeouts without a later answer created evidence")
	}
}

func TestDialTimeoutAnswerResetsRun(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	g.blocks.minLife = 0
	md := dialMetadata("alice", "reset.example")
	f := &timeoutMember{fakeMember: g.members[0].(*fakeMember), during: func() {
		clock.advance(time.Nanosecond)
		nodeUp(health, "n0")
	}}
	f.err = &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	g.members[0] = f
	dialTimeout := func() { t.Helper(); _, _ = g.DialContext(context.Background(), md); clock.advance(time.Second) }
	dialTimeout()
	dialTimeout()
	g.members[0] = &fakeMember{name: "n0", conn: func() C.Conn { return &scriptConn{} }}
	endConn(t, g, health, clock, "reset.example", connEnd{answer: true, wait: time.Second})
	g.members[0] = f
	for i := 0; i < silentNeeded; i++ {
		dialTimeout()
	}
	if g.blocks.blockedCount() != 0 || len(ofKind(sink, ConnSiteBlocked)) != 0 {
		t.Fatal("timeouts spanning an answer created a block")
	}
}

func TestDialTimeoutEvidenceIsPerAccountAndSite(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	g.blocks.minLife = 0
	md := dialMetadata("alice", "isolated.example")
	f := &timeoutMember{fakeMember: g.members[0].(*fakeMember), during: func() { clock.advance(time.Nanosecond); nodeUp(health, "n0") }}
	f.err = &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	g.members[0] = f
	for i := 0; i < silentNeeded; i++ {
		_, _ = g.DialContext(context.Background(), md)
		clock.advance(time.Second)
	}
	if g.blocks.blocked("bob", "isolated.example", 0, clock.now) {
		t.Fatal("alice's timeout block affected bob")
	}
	if g.blocks.blocked("alice", "other.example", 0, clock.now) {
		t.Fatal("timeout block affected another site")
	}
	if len(ofKind(sink, ConnSiteBlocked)) != 1 {
		t.Fatalf("block events=%+v, want only alice's site", ofKind(sink, ConnSiteBlocked))
	}
}

func TestDialTimeoutRetryRecordsEvidenceOnlyForRetryNode(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	g.blocks.minLife = 0
	md := dialMetadata("alice", "retry.example")
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	f0 := &timeoutMember{fakeMember: g.members[0].(*fakeMember), during: func() {
		clock.advance(time.Nanosecond)
		nodeUp(health, "n0")
		health.recordDialAt("n0", "seed-one", errors.New("failure"), time.Second)
		health.recordDialAt("n0", "seed-two", errors.New("failure"), time.Second)
	}}
	f0.err = timeoutErr
	f1 := &timeoutMember{fakeMember: g.members[1].(*fakeMember), during: func() {
		clock.advance(time.Nanosecond)
		nodeUp(health, "n1")
	}}
	f1.err = timeoutErr
	g.members[0] = f0
	g.members[1] = f1
	_, err := g.DialContext(context.Background(), md)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("dial error = %v, want deadline exceeded", err)
	}
	if !health.down("n0") {
		t.Fatal("n0 did not go down")
	}
	g.blocks.mu.RLock()
	_, key1 := g.blocks.m[blockKey{"alice", siteKey("retry.example", netip.Addr{}), 1}]
	_, key0 := g.blocks.m[blockKey{"alice", siteKey("retry.example", netip.Addr{}), 0}]
	silent1 := 0
	if record := g.blocks.m[blockKey{"alice", siteKey("retry.example", netip.Addr{}), 1}]; record != nil {
		silent1 = record.silent
	}
	g.blocks.mu.RUnlock()
	if key0 || !key1 || silent1 != 1 {
		t.Fatalf("retry evidence: node0=%v node1=%v silent=%d, want no node0 evidence and one silent node1 record", key0, key1, silent1)
	}
	if got := f0.dials.Load(); got != 1 {
		t.Errorf("n0 dials = %d, want 1", got)
	}
	if got := f1.dials.Load(); got != 1 {
		t.Errorf("n1 dials = %d, want 1", got)
	}
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 0 {
		t.Errorf("blocked events = %+v, want none", got)
	}
}

func TestDialTimeoutFailureKindsDoNotCreateEvidence(t *testing.T) {
	for _, err := range []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNRESET},
		&net.DNSError{Err: "no such host", Name: "x.example"},
		&net.OpError{Op: "dial", Net: "tls", Err: errors.New("handshake failed")},
		errors.New("other"),
	} {
		g, health, clock, sink := blockGroup(t, []int{0})
		g.blocks.minLife = 0
		f := &timeoutMember{fakeMember: g.members[0].(*fakeMember), during: func() {
			clock.advance(time.Nanosecond)
			nodeUp(health, "n0")
		}}
		f.err = err
		g.members[0] = f
		for i := 0; i < silentNeeded+1; i++ {
			_, _ = g.DialContext(context.Background(), dialMetadata("alice", "x.example"))
			clock.advance(time.Second)
		}
		if g.blocks.any() || len(ofKind(sink, ConnSiteBlocked)) != 0 {
			t.Fatalf("%v created evidence", err)
		}
	}
}
