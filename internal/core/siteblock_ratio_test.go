package core

import (
	"testing"
	"time"
)

// silentOK is one silent connection of alice to site "s" through node 0 that
// waited long enough, while the node answered a probe after it began and the
// site was answered recently (so the plain rule is out of play).
func silentOK(b *siteBlocks, now func() time.Time) (time.Duration, bool) {
	return b.silentEnd("a", "s", 0, 37*time.Second, true, now().Add(time.Second))
}

func TestSiteBlocksLossyPathBlocksTheNodeDespiteAnswers(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < silentRatioMin; i++ {
		if i == 5 || i == 10 {
			b.answered("a", "s", 0, true)
		}
		ttl, blocked := silentOK(b, now)
		if i < silentRatioMin-1 {
			if blocked {
				t.Fatalf("blocked at silent connection %d, want the %dth", i+1, silentRatioMin)
			}
			continue
		}
		if !blocked || ttl != siteBlockFirst {
			t.Fatalf("ttl, blocked = %s, %v at silent connection %d, want %s, true", ttl, blocked, i+1, siteBlockFirst)
		}
	}
	ratio, silent, answered := b.blockInfo("a", "s", 0)
	if !ratio || silent != silentRatioMin || answered != 2 {
		t.Fatalf("blockInfo = %v, %d, %d; want the ratio rule over %d silent and 2 answered", ratio, silent, answered, silentRatioMin)
	}
	if !b.blocked("a", "s", 0, now()) {
		t.Fatal("the node is not blocked")
	}
}

func TestSiteBlocksMostlyAnsweredPathIsNotBlocked(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	b.silentEnd("a", "s", 0, 37*time.Second, true, now().Add(time.Second)) // the window starts
	for i := 0; i < 10; i++ {
		b.answered("a", "s", 0, true)
	}
	for i := 0; i < 29; i++ { // 30 silent against 10 answered: 75 percent, under 90
		if _, blocked := silentOK(b, now); blocked {
			t.Fatalf("blocked with %d silent against 10 answered", i+2)
		}
	}
	if b.blockedCount() != 0 {
		t.Fatal("a block exists")
	}
}

func TestSiteBlocksFewSilentConnectionsAreNotEnoughForTheRatio(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < silentRatioMin-1; i++ {
		if _, blocked := silentOK(b, now); blocked {
			t.Fatalf("blocked after %d silent connections while the site was answered recently", i+1)
		}
	}
}

func TestSiteBlocksRatioWindowStartsOver(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for round := 0; round < 2; round++ {
		for i := 0; i < silentRatioMin-1; i++ {
			if _, blocked := silentOK(b, now); blocked {
				t.Fatalf("round %d: blocked by %d silent connections of one window", round, i+1)
			}
		}
		clock.advance(silentWindow + time.Minute)
	}
}

func TestSiteBlocksNodeAnswerAfterTheWindowBeganIsNeededForTheRatio(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	before := now() // the node last answered a probe at this moment
	for i := 0; i < 2*silentRatioMin; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, 37*time.Second, true, before); blocked {
			t.Fatal("blocked with no probe answer after the window began (the network may be out)")
		}
	}
	if ttl, blocked := b.silentEnd("a", "s", 0, 37*time.Second, true, before.Add(time.Second)); !blocked || ttl != siteBlockFirst {
		t.Fatalf("ttl, blocked = %s, %v after the node answered, want %s, true", ttl, blocked, siteBlockFirst)
	}
}

func blockByRatio(t *testing.T, b *siteBlocks, now func() time.Time) time.Duration {
	t.Helper()
	var ttl time.Duration
	for i := 0; i < silentRatioMin; i++ {
		if got, blocked := silentOK(b, now); blocked {
			ttl = got
		}
	}
	if ttl == 0 {
		t.Fatal("no ratio block")
	}
	return ttl
}

func TestSiteBlocksAnswerDoesNotLiftARatioBlock(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	blockByRatio(t, b, now)
	b.answered("a", "s", 0, true)  // a connection that began before the block
	b.answered("a", "s", 0, false) // one that goes on being answered
	if !b.blocked("a", "s", 0, now()) {
		t.Fatal("an answer lifted the block of the ratio rule")
	}
	if ttl, blocked := silentOK(b, now); blocked || ttl != 0 {
		t.Fatalf("a silent end inside the block made another: %s, %v", ttl, blocked)
	}
}

func TestSiteBlocksPlainBlockIsStillLiftedByAnAnswer(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < silentNeeded; i++ {
		b.silentEnd("a", "s", 0, 37*time.Second, false, now().Add(time.Second))
	}
	if !b.blocked("a", "s", 0, now()) {
		t.Fatal("no plain block")
	}
	if ratio, _, _ := b.blockInfo("a", "s", 0); ratio {
		t.Fatal("a plain block is reported as a ratio block")
	}
	b.answered("a", "s", 0, false)
	if b.blocked("a", "s", 0, now()) {
		t.Fatal("the plain block was not lifted by an answer")
	}
}

func TestSiteBlocksRatioStrikesStayWhileMostlySilentAndGrow(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	if ttl := blockByRatio(t, b, now); ttl != siteBlockFirst {
		t.Fatalf("first block = %s, want %s", ttl, siteBlockFirst)
	}
	clock.advance(siteBlockFirst + time.Second) // the block is over
	for i := 0; i < 5; i++ {
		silentOK(b, now)
	}
	b.answered("a", "s", 0, true) // one answer among the silent ones: not healthy
	var ttl time.Duration
	for i := 0; i < silentRatioMin; i++ {
		if got, blocked := silentOK(b, now); blocked {
			ttl = got
		}
	}
	if ttl != 2*siteBlockFirst {
		t.Fatalf("second block = %s, want %s: the strike stays while the node is mostly silent", ttl, 2*siteBlockFirst)
	}
}

func TestSiteBlocksRatioStrikesResetWhenTheNodeRecovers(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	blockByRatio(t, b, now)
	clock.advance(siteBlockFirst + time.Second)
	b.answered("a", "s", 0, true) // the first connection after the block is answered
	if b.blocked("a", "s", 0, now()) {
		t.Fatal("still blocked after the time and an answer")
	}
	clock.advance(time.Second)
	if ttl := blockByRatio(t, b, now); ttl != siteBlockFirst {
		t.Fatalf("block after a recovery = %s, want the first length %s", ttl, siteBlockFirst)
	}
}

func TestSiteBlocksOnlyFirstAnswersCountInTheWindow(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	b.silentEnd("a", "s", 0, 37*time.Second, true, now().Add(time.Second))
	for i := 0; i < 100; i++ { // one connection that goes on being answered, reported again and again
		b.answered("a", "s", 0, false)
	}
	blockByRatio(t, b, now)
	if _, silent, answered := b.blockInfo("a", "s", 0); answered != 0 || silent < silentRatioMin {
		t.Fatalf("blockInfo silent, answered = %d, %d; later reports of one connection must not count", silent, answered)
	}
}

func TestStickyGroupLossySiteLeavesTheNodeAndTheEventSaysHow(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	answered := connEnd{answer: true, wait: time.Second}
	lossy := connEnd{wait: 6 * time.Second}
	endConn(t, g, health, clock, blackholed, answered) // the site works at times: the plain rule is out of play
	for i := 0; i < silentRatioMin; i++ {
		if i == 5 || i == 12 {
			endConn(t, g, health, clock, blackholed, answered)
		}
		if n := g.blocks.blockedCount(); n != 0 {
			t.Fatalf("%d blocks after %d silent connections, want none yet", n, i)
		}
		endConn(t, g, health, clock, blackholed, lossy)
	}
	blocks := ofKind(sink, ConnSiteBlocked)
	if len(blocks) != 1 {
		t.Fatalf("block events = %+v, want one", blocks)
	}
	ev := blocks[0]
	if ev.Label != "n0" || ev.Login != "alice" || ev.Block != siteBlockFirst || ev.Silent != silentRatioMin || ev.Answered != 2 {
		t.Fatalf("block event = %+v, want n0, alice, %s, %d silent, 2 answered", ev, siteBlockFirst, silentRatioMin)
	}
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("the site chose %d, want the next node 1", got)
	}
}

func TestStickyGroupMostlyAnsweredSiteStays(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	answered := connEnd{answer: true, wait: time.Second}
	lossy := connEnd{wait: 6 * time.Second}
	endConn(t, g, health, clock, blackholed, lossy)
	for i := 0; i < 30; i++ {
		endConn(t, g, health, clock, blackholed, lossy)
		if i%3 == 0 {
			endConn(t, g, health, clock, blackholed, answered)
		}
	}
	if n := g.blocks.blockedCount(); n != 0 || len(ofKind(sink, ConnSiteBlocked)) != 0 {
		t.Fatalf("%d blocks for a site answered every few connections", n)
	}
	if got := chosen(t, g, blackholed); got != 0 {
		t.Fatalf("the site chose %d, want to stay on 0", got)
	}
}

// A client that gives up after about 3 s without an answer and retries at once
// (Telegram does) never reached silentMinLife: its dead path stayed in use for
// many minutes. The ratio rule counts such connections.
func TestSiteBlocksRatioCountsClientsThatGiveUpEarly(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	early := 3 * time.Second
	if early >= silentMinLife || early < silentRatioMinLife {
		t.Fatalf("test wait %s must lie in [%s, %s)", early, silentRatioMinLife, silentMinLife)
	}
	for i := 0; i < silentRatioMin; i++ {
		ttl, blocked := b.silentEnd("a", "s", 0, early, true, now().Add(time.Second))
		if i < silentRatioMin-1 {
			if blocked {
				t.Fatalf("blocked at silent connection %d, want the %dth", i+1, silentRatioMin)
			}
			continue
		}
		if !blocked || ttl != siteBlockFirst {
			t.Fatalf("ttl, blocked = %s, %v at silent connection %d, want %s, true", ttl, blocked, i+1, siteBlockFirst)
		}
	}
	if ratio, silent, answered := b.blockInfo("a", "s", 0); !ratio || silent != silentRatioMin || answered != 0 {
		t.Fatalf("blockInfo = %v, %d, %d; want the ratio rule over %d silent", ratio, silent, answered, silentRatioMin)
	}
}

// Under silentRatioMinLife a connection is a client that left: no rule counts it.
func TestSiteBlocksWaitsUnderTheRatioMinimumLeaveNoEvidence(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < 2*silentRatioMin; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, silentRatioMinLife-time.Millisecond, true, now().Add(time.Second)); blocked {
			t.Fatalf("blocked by a connection that waited under %s", silentRatioMinLife)
		}
	}
	if b.any() {
		t.Fatal("waits under the ratio minimum were recorded")
	}
	if _, blocked := b.silentEnd("a", "s", 0, silentRatioMinLife, true, now().Add(time.Second)); blocked || !b.any() {
		t.Fatalf("a wait of exactly %s was not recorded (or blocked at once)", silentRatioMinLife)
	}
}

// The plain rule keeps its bound: short waits count for the ratio only.
func TestSiteBlocksPlainRuleIgnoresWaitsUnderTheMinimumLife(t *testing.T) {
	_, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	short := silentMinLife - time.Second
	for i := 0; i < silentNeeded+2; i++ {
		if _, blocked := b.silentEnd("a", "s", 0, short, false, now().Add(time.Second)); blocked {
			t.Fatalf("the plain rule blocked after %d connections that waited %s", i+1, short)
		}
	}
	for i := 0; i < silentNeeded; i++ {
		ttl, blocked := b.silentEnd("a", "s", 0, silentMinLife, false, now().Add(time.Second))
		if i < silentNeeded-1 && blocked {
			t.Fatalf("plain block after %d long waits, want %d", i+1, silentNeeded)
		}
		if i == silentNeeded-1 && (!blocked || ttl != siteBlockFirst) {
			t.Fatalf("ttl, blocked = %s, %v after %d long waits, want %s, true", ttl, blocked, silentNeeded, siteBlockFirst)
		}
	}
	if ratio, _, _ := b.blockInfo("a", "s", 0); ratio {
		t.Fatal("the block came from the ratio rule, want the plain one")
	}
}

// The ratio bound never exceeds the plain one: a test (or a caller) that
// shortens minLife shortens both.
func TestSiteBlocksShortestLifeIsTheLowerBound(t *testing.T) {
	b := newSiteBlocks(nil)
	if got := b.shortestLife(); got != silentRatioMinLife {
		t.Fatalf("shortestLife = %s, want %s", got, silentRatioMinLife)
	}
	b.minLife = time.Millisecond
	if got := b.shortestLife(); got != time.Millisecond {
		t.Fatalf("shortestLife with minLife 1ms = %s, want 1ms", got)
	}
}

func TestStickyGroupSiteWhoseClientGivesUpEarlyLeavesTheNode(t *testing.T) {
	g, health, clock, sink := blockGroup(t, []int{0, 1})
	answered := connEnd{answer: true, wait: time.Second}
	early := connEnd{wait: 3 * time.Second}
	endConn(t, g, health, clock, blackholed, answered) // the plain rule is out of play
	for i := 0; i < silentRatioMin; i++ {
		if n := g.blocks.blockedCount(); n != 0 {
			t.Fatalf("%d blocks after %d silent connections, want none yet", n, i)
		}
		endConn(t, g, health, clock, blackholed, early)
	}
	blocks := ofKind(sink, ConnSiteBlocked)
	if len(blocks) != 1 {
		t.Fatalf("block events = %+v, want one", blocks)
	}
	if ev := blocks[0]; ev.Label != "n0" || ev.Silent != silentRatioMin || ev.Answered != 0 {
		t.Fatalf("block event = %+v, want n0, %d silent, 0 answered", ev, silentRatioMin)
	}
	if got := chosen(t, g, blackholed); got != 1 {
		t.Fatalf("the site chose %d, want the next node 1", got)
	}
}

// A dial that timed out after the ratio minimum but under the plain one is
// evidence for the ratio rule only.
func TestDialTimeoutUnderTheMinimumLifeCountsForTheRatioOnly(t *testing.T) {
	g, _, clock, sink := blockGroup(t, []int{0, 1})
	md := dialMetadata("alice", "early.example")
	took := silentMinLife - time.Second
	for i := 0; i < silentNeeded+1; i++ {
		start := clock.now
		setNodeLastOK(g.healths[0], start.Add(time.Nanosecond))
		g.recordDialTimeout("alice", "early.example", 0, "n0", md, start, took)
		clock.advance(time.Second)
	}
	if !g.blocks.any() {
		t.Fatal("a dial timeout over the ratio minimum left no evidence")
	}
	if got := ofKind(sink, ConnSiteBlocked); len(got) != 0 {
		t.Fatalf("the plain rule blocked on dial timeouts under %s: %+v", silentMinLife, got)
	}
}
