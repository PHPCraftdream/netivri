package core

import (
	"strconv"
	"testing"
	"time"
)

func TestSiteBlocksRateLimitsFailedPrunesAndReclaimsExpiredRecords(t *testing.T) {
	clock, now := fakeClock(time.Unix(1_000_000, 0))
	b := newSiteBlocks(now)
	for i := 0; i < siteBlockCap; i++ {
		b.silentEnd("a", "site"+strconv.Itoa(i), 0, 37*time.Second, false, now().Add(time.Second))
	}
	if got := b.n.Load(); got != siteBlockCap {
		t.Fatalf("records = %d, want cap %d", got, siteBlockCap)
	}

	b.silentEnd("a", "first-miss", 0, 37*time.Second, false, now().Add(time.Second))
	if b.pruneScans != 1 {
		t.Fatalf("prune scans = %d after the first full-table miss, want 1", b.pruneScans)
	}
	for i := 0; i < 100; i++ {
		b.silentEnd("a", "miss"+strconv.Itoa(i), 0, 37*time.Second, false, now().Add(time.Second))
	}
	if b.pruneScans != 1 {
		t.Fatalf("prune scans = %d after repeated misses, want the failed scan not to repeat", b.pruneScans)
	}
	if got := b.n.Load(); got != siteBlockCap {
		t.Fatalf("records after repeated misses = %d, want cap %d", got, siteBlockCap)
	}

	clock.advance(silentWindow + time.Minute)
	b.silentEnd("a", "after-expiry", 0, 37*time.Second, false, now().Add(time.Second))
	if b.pruneScans != 2 {
		t.Fatalf("prune scans = %d after records expired, want 2", b.pruneScans)
	}
	if got := b.n.Load(); got != 1 {
		t.Fatalf("records after expired entries were reclaimed = %d, want 1", got)
	}
	b.mu.RLock()
	_, kept := b.m[blockKey{"a", "after-expiry", 0}]
	b.mu.RUnlock()
	if !kept {
		t.Fatal("new record was not inserted after expired records were reclaimed")
	}
}
