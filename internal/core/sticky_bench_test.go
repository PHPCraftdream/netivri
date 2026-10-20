//go:build bench

package core

import (
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// Micro-benchmarks of the sticky-group hot path. They reuse the fake members
// and the group/table constructors of the unit tests (sticky_test.go,
// affinity_test.go), which are compiled into the same test binary.

// benchStickyGroup builds a sticky group over n fake members with seeded
// probe delays so selection is deterministic and fast.
func benchStickyGroup(b *testing.B, n int) *stickyGroup {
	b.Helper()
	members := make([]member, n)
	labels := make([]string, n)
	for i := 0; i < n; i++ {
		m := &fakeMember{name: fmt.Sprintf("n%d", i)}
		members[i] = m
		labels[i] = m.name
	}
	table := newAffinityTable(nil, 0)
	health := newHealthTable()
	g := newStickyGroupMembers("netivri-group-bench", members, labels, table, health, nil)
	for i, label := range labels {
		health.recordProbe(label, true, time.Duration(50+i)*time.Millisecond)
	}
	return g
}

func BenchmarkStickyChooseHit(b *testing.B) {
	g := benchStickyGroup(b, 2)
	const login, site = "bench", "example.com"
	if _, rebound := g.choose(login, site); rebound {
		b.Fatal("the first choose must not rebind")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.choose(login, site)
	}
}

func BenchmarkStickyChooseHitParallel(b *testing.B) {
	g := benchStickyGroup(b, 2)
	// Pre-bind many keys spread over the shards.
	const keys = 4096
	logins := make([]string, keys)
	for i := 0; i < keys; i++ {
		logins[i] = fmt.Sprintf("user%d", i)
		g.choose(logins[i], "example.com")
	}
	var workers atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker walks its own sequence of keys: no shared counter in
		// the measured loop, so the shard locks are what is measured.
		i := int(workers.Add(1)) * 257
		for pb.Next() {
			i++
			_, _ = g.choose(logins[i%keys], "example.com")
		}
	})
}

func BenchmarkStickyChooseMiss(b *testing.B) {
	for _, members := range []int{2, 20} {
		b.Run(fmt.Sprintf("members-%d", members), func(b *testing.B) {
			g := benchStickyGroup(b, members)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// A new key each iteration forces a full selection.
				login := fmt.Sprintf("miss%d", i)
				_, _ = g.choose(login, "miss.example")
			}
		})
	}
}

func BenchmarkSiteKey(b *testing.B) {
	cases := []struct {
		name string
		host string
	}{
		{"subdomain", "www.example.com"},
		{"multi-label-suffix", "a.b.example.co.uk"},
		{"ip-literal", "192.0.2.1"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = siteKey(tc.host, netip.Addr{})
			}
		})
	}
}

func BenchmarkHealthRecordDial(b *testing.B) {
	health := newHealthTable()
	health.register([]string{"a", "b"})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			health.recordDial("a", true, time.Millisecond)
		}
	})
}
