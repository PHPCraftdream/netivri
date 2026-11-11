package core

import (
	"reflect"
	"testing"
	"time"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func TestHopPoolMergeRetainsHealthFromEveryPredecessor(t *testing.T) {
	now := time.Unix(2400, 0)
	m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	firstSpec := newHopPoolSpec(preHopPool, []string{"socks5://a.invalid:1080", "socks5://x.invalid:1080"}, 0)
	secondSpec := newHopPoolSpec(preHopPool, []string{"socks5://b.invalid:1080", "socks5://y.invalid:1080"}, 0)
	first := pool.New(firstSpec.keys, pool.Options{Clock: func() time.Time { return now }})
	second := pool.New(secondSpec.keys, pool.Options{Clock: func() time.Time { return now }})
	first.RecordHard(firstSpec.keys[0])
	first.RecordHard(firstSpec.keys[0])
	second.RecordSoft(secondSpec.keys[0])
	for range 3 {
		second.RecordHard(secondSpec.keys[0])
	}

	firstID := poolIdentity(preHopPool, firstSpec.keys)
	secondID := poolIdentity(preHopPool, secondSpec.keys)
	m.pools[firstID] = first
	m.pools[secondID] = second
	firstLogical := hopPoolLogicalKey{role: preHopPool, route: "merge-route", node: 0}
	secondLogical := hopPoolLogicalKey{role: preHopPool, route: "merge-route", node: 1}
	m.uses[firstLogical] = &hopPoolUse{logical: firstLogical, role: preHopPool, route: firstLogical.route, spec: firstSpec, pool: first}
	m.uses[secondLogical] = &hopPoolUse{logical: secondLogical, role: preHopPool, route: secondLogical.route, spec: secondSpec, pool: second}
	firstOldKeys := []string{first.Snapshot()[0].Key, first.Snapshot()[1].Key}
	secondOldKeys := []string{second.Snapshot()[0].Key, second.Snapshot()[1].Key}

	mergedSpec := newHopPoolSpec(preHopPool, []string{"socks5://a.invalid:1080", "socks5://b.invalid:1080"}, 1)
	uses := []*hopPoolUse{
		{logical: firstLogical, role: preHopPool, route: firstLogical.route, spec: mergedSpec},
		{logical: secondLogical, role: preHopPool, route: secondLogical.route, spec: mergedSpec},
	}
	assigned := m.assignPoolsLocked(uses)
	merged := assigned[poolIdentity(preHopPool, mergedSpec.keys)]
	if merged == nil || merged == first || merged == second {
		t.Fatal("merged pool did not get a distinct destination")
	}
	if got := poolState(merged, mergedSpec.keys[0]); got.HardFailures != 2 || got.State != pool.Healthy {
		t.Fatalf("merged A health = %#v, want healthy with two hard failures", got)
	}
	if got := poolState(merged, mergedSpec.keys[1]); got.State != pool.Quarantined || got.Trips != 1 || got.HardFailures != 3 || got.Sand <= 0 {
		t.Fatalf("merged B did not retain second predecessor health: %#v", got)
	}
	if got, want := []string{first.Snapshot()[0].Key, first.Snapshot()[1].Key}, firstOldKeys; !reflect.DeepEqual(got, want) {
		t.Fatalf("first predecessor membership changed: got %v, want %v", got, want)
	}
	if got, want := []string{second.Snapshot()[0].Key, second.Snapshot()[1].Key}, secondOldKeys; !reflect.DeepEqual(got, want) {
		t.Fatalf("second predecessor membership changed: got %v, want %v", got, want)
	}
	if got := poolState(first, firstSpec.keys[0]); got.HardFailures != 2 || got.State != pool.Healthy {
		t.Fatalf("first predecessor health changed: %#v", got)
	}
	if got := poolState(second, secondSpec.keys[0]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("second predecessor health changed: %#v", got)
	}
}
