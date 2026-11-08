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
