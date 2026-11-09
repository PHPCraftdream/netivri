package core

import (
	"context"
	"testing"
	"time"
)

func TestAffinityAnswerTimesArePerMemberAndKey(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "site.example"}
	otherAccount := affinityKey{login: "bob", site: "site.example"}
	otherSite := affinityKey{login: "alice", site: "other.example"}
	for _, k := range []affinityKey{key, otherAccount, otherSite} {
		table.acquire(k, noDownMember, func() (int, bool) { return 0, false })
	}

	table.noteAnswer(key, 0, now())
	if !table.answeredWithin(key, 0, time.Minute, now()) {
		t.Fatal("member 0 answer was not remembered")
	}
	for _, k := range []affinityKey{otherAccount, otherSite} {
		if table.answeredWithin(k, 0, time.Minute, now()) {
			t.Fatalf("answer for %v leaked to another account or site", k)
		}
	}

	clock.advance(9 * time.Minute)
	table.noteAnswer(key, 1, now())
	for _, k := range []affinityKey{otherAccount, otherSite} {
		if table.answeredWithin(k, 1, time.Minute, now()) {
			t.Fatalf("member 1 answer for %v leaked to another account or site", k)
		}
	}
	if !table.answeredWithin(key, 0, silentWindow, now()) {
		t.Fatal("member 1 answer replaced member 0's recent answer")
	}
	clock.advance(2 * time.Minute)
	if table.answeredWithin(key, 0, silentWindow, now()) {
		t.Fatal("member 0 stale answer still counts")
	}
	if !table.answeredWithin(key, 1, silentWindow, now()) {
		t.Fatal("member 1 recent answer expired with member 0's answer")
	}
	clock.advance(9 * time.Minute)
	if table.answeredWithin(key, 1, silentWindow, now()) {
		t.Fatal("member 1 stale answer still counts")
	}
}

func TestAffinityAnswerHistorySurvivesMemberRebind(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "site.example"}
	table.acquire(key, noDownMember, func() (int, bool) { return 0, false })
	table.noteAnswer(key, 0, now())
	clock.advance(9 * time.Minute)

	member, rebound := table.acquire(key, func(member int) bool { return member == 0 }, func() (int, bool) {
		return 1, false
	})
	if member != 1 || !rebound {
		t.Fatalf("rebind = %d, %v; want member 1, true", member, rebound)
	}
	if !table.answeredWithin(key, 0, silentWindow, now()) {
		t.Fatal("recent member 0 answer was lost during rebind")
	}
	table.noteAnswer(key, 1, now())
	clock.advance(2 * time.Minute)
	if table.answeredWithin(key, 0, silentWindow, now()) {
		t.Fatal("stale member 0 answer still counts after rebind")
	}
	if !table.answeredWithin(key, 1, silentWindow, now()) {
		t.Fatal("rebound member's answer was not retained")
	}
}

func TestAffinityAnswerHistoryExpiresWithBinding(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	table := newAffinityTable(now, 10*time.Minute)
	key := affinityKey{login: "alice", site: "site.example"}
	table.acquire(key, noDownMember, func() (int, bool) { return 0, false })
	table.noteAnswer(key, 0, now())
	clock.advance(10 * time.Minute)
	if table.answeredWithin(key, 0, silentWindow, now()) {
		t.Fatal("answer outlived its expired binding")
	}
	table.noteAnswer(key, 1, now())
	table.acquire(key, noDownMember, func() (int, bool) { return 1, false })
	for _, member := range []int{0, 1} {
		if table.answeredWithin(key, member, silentWindow, now()) {
			t.Fatalf("member %d answer survived creation of a new binding", member)
		}
	}
}

func TestStickySiteRecentAnswerSurvivesLateAnswerFromOldMember(t *testing.T) {
	g, health, clock, _ := blockGroup(t, []int{0, 1})
	key := affinityKey{login: "alice", site: blackholed}
	g.table.acquire(key, noDownMember, func() (int, bool) { return 0, false })
	oldConn, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = oldConn.Write([]byte("hello"))

	nodeDown(health, "n0")
	newConn, err := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
	if err != nil {
		t.Fatal(err)
	}
	if member, ok := g.table.peek(key); !ok || member != 1 {
		t.Fatalf("binding after node failure = %d, %v; want member 1, true", member, ok)
	}
	_, _ = newConn.Write([]byte("hello"))
	_, _ = newConn.Read(make([]byte, 8))
	_, _ = oldConn.Read(make([]byte, 8))
	if !g.table.answeredWithin(key, 1, silentWindow, clock.Now()) {
		t.Fatal("late answer from member 0 erased member 1's recent answer")
	}

	for i := 0; i < silentNeeded; i++ {
		conn, dialErr := g.DialContext(context.Background(), dialMetadata("alice", blackholed))
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		if member, ok := g.table.peek(key); !ok || member != 1 {
			t.Fatalf("silent connection %d used binding %d, %v; want member 1", i+1, member, ok)
		}
		_, _ = conn.Write([]byte("hello"))
		clock.advance(37 * time.Second)
		nodeUp(health, "n1")
		_ = conn.Close()
		if got := g.blocks.blockedCount(); got != 0 {
			t.Fatalf("%d blocks after silent member 1 connection %d despite its recent answer", got, i+1)
		}
	}
	_ = newConn.Close()
	_ = oldConn.Close()
}
