package pool

import (
	"math"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"
)

var testEpoch = time.Unix(1_700_000_000, 0).UTC()

type testClock struct {
	nanos atomic.Int64
}

func newTestClock() *testClock {
	clock := &testClock{}
	clock.Set(testEpoch)
	return clock
}

func (c *testClock) Now() time.Time {
	return time.Unix(0, c.nanos.Load()).UTC()
}

func (c *testClock) Set(now time.Time) {
	c.nanos.Store(now.UnixNano())
}

func newTestPool(clock *testClock, keys ...string) *Pool {
	return newTestPoolWithSeed(clock, 0x12345678, 0x9abcdef0, keys...)
}

func newTestPoolWithSeed(clock *testClock, seed1, seed2 uint64, keys ...string) *Pool {
	return New(keys, Options{
		Clock: clock.Now,
		RNG:   rand.NewPCG(seed1, seed2),
	})
}

func requireClose(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.IsNaN(got) || got < want-tolerance || got > want+tolerance {
		t.Errorf("got %.12g, want %.12g ± %.3g", got, want, tolerance)
	}
}

func requireSnapshot(t *testing.T, p *Pool, key string) HopSnapshot {
	t.Helper()
	for _, snapshot := range p.Snapshot() {
		if snapshot.Key == key {
			return snapshot
		}
	}
	t.Fatalf("snapshot for key %q not found in %#v", key, p.Snapshot())
	return HopSnapshot{}
}

func requireSingleClaim(t *testing.T, claims []ProbeClaim, key string) ProbeClaim {
	t.Helper()
	if len(claims) != 1 {
		t.Fatalf("got %d probe claims, want exactly one", len(claims))
	}
	if got := claims[0].Key(); got != key {
		t.Fatalf("claim key = %q, want %q", got, key)
	}
	return claims[0]
}

func requireUniqueStrings(t *testing.T, values []string) map[string]bool {
	t.Helper()
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			t.Errorf("duplicate key %q in %v", value, values)
		}
		seen[value] = true
	}
	return seen
}
