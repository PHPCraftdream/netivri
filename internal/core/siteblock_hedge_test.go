package core

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

func TestHedgeDialTimeoutEvidenceCountsLogicalDialOnce(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	m := newScriptedMember("n0",
		scriptedDial{wait: release, err: timeout},
		scriptedDial{wait: release, err: timeout},
	)
	g, health, clock, sink := blockGroup(t, []int{0})
	g.members[0] = m
	g.hedgeAll = true
	g.blocks.minLife = 0
	fire, _ := useFakeHedgeTimer(t)
	md := dialMetadata("alice", "www.example.com")

	out := dialAsync(g, md)
	await := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(testTimeoutUnit):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	await(m.started, "primary attempt")
	fire()
	await(m.started, "hedge attempt")

	// Both attempts are now blocked on release, so clock changes happen before
	// either timeout is recorded; no attempt can concurrently read this clock.
	clock.advance(time.Nanosecond)
	nodeUp(health, "n0")
	closeRelease()
	select {
	case result := <-out:
		if !errors.Is(result.err, os.ErrDeadlineExceeded) {
			t.Fatalf("DialContext error = %v, want deadline exceeded", result.err)
		}
	case <-time.After(testTimeoutUnit):
		t.Fatal("timed out waiting for dial result")
	}

	key := blockKey{login: "alice", site: siteKey("www.example.com", netip.Addr{}), member: 0}
	g.blocks.mu.RLock()
	state := g.blocks.m[key]
	silent := 0
	if state != nil {
		silent = state.silent
	}
	g.blocks.mu.RUnlock()
	if silent != 1 {
		t.Fatalf("silent evidence = %d, want 1 for one logical dial", silent)
	}
	if failed := ofKind(sink, ConnDialFailed); len(failed) != 1 {
		t.Fatalf("ConnDialFailed events = %d, want 1", len(failed))
	}
	if got := g.blocks.blockedCount(); got != 0 {
		t.Fatalf("blocks after two actual failed attempts = %d, want none", got)
	}
}
