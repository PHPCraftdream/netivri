package core

import (
	"hash/fnv"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAffinityTTL  = 24 * time.Hour
	provisionalLifetime = 2 * time.Minute
	maxAffinityBindings = 100000
	affinityShards      = 16
	affinityEvictSample = 16
)

// FNV-1a 64-bit constants; the shard index is computed without allocating.
const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// affinityHash is FNV-1a of "login|site" written in turn, so no concatenated
// string is built on the hot path. Streaming FNV over the parts equals FNV
// over the concatenation.
func affinityHash(login, site string) uint64 {
	h := fnvOffset64
	for i := 0; i < len(login); i++ {
		h ^= uint64(login[i])
		h *= fnvPrime64
	}
	h ^= uint64('|')
	h *= fnvPrime64
	for i := 0; i < len(site); i++ {
		h ^= uint64(site[i])
		h *= fnvPrime64
	}
	return h
}

// affinityKey identifies a binding: one login and one site.
type affinityKey struct {
	login string
	site  string
}

// binding is one site binding. lastUsed is atomic so eviction and count can
// read it while another goroutine holds no extra lock.
type binding struct {
	member      int
	boundAt     time.Time
	lastUsed    atomic.Int64 // unix nanoseconds
	provisional bool
}

type affinityShard struct {
	mu       sync.Mutex
	bindings map[affinityKey]*binding
	cap      int
}

// affinityTable maps (login, site) to the member index of the route's sticky
// group. It is sharded so a per-dial acquire takes one small shard lock.
// Keys are idle-expired after the TTL (a provisional binding after its
// shorter lifetime) and bounded per shard, evicting the oldest lastUsed.
type affinityTable struct {
	shards  [affinityShards]*affinityShard
	now     func() time.Time
	ttl     time.Duration
	rebinds atomic.Uint64
}

// newAffinityTable builds the binding table; now is injectable for tests and
// nil means time.Now. A ttl of 0 means the default of 24 hours.
func newAffinityTable(now func() time.Time, ttl time.Duration) *affinityTable {
	if now == nil {
		now = time.Now
	}
	if ttl == 0 {
		ttl = defaultAffinityTTL
	}
	t := &affinityTable{now: now, ttl: ttl}
	for i := range t.shards {
		t.shards[i] = &affinityShard{
			bindings: make(map[affinityKey]*binding),
			cap:      maxAffinityBindings / affinityShards,
		}
	}
	return t
}

func (t *affinityTable) shardIndex(login, site string) int {
	return int(affinityHash(login, site) % affinityShards)
}

// acquire returns the member for key in one atomic step. A live binding
// whose member is not down is returned and its lastUsed refreshed; an
// expired binding is deleted; a missing binding or one whose member is down
// calls pick once, installs the result and returns it. rebound is true only
// when an existing live binding was moved to a different member because its
// member was down; the rebind counter is incremented exactly there, once per
// real rebind (the shard lock serialises concurrent callers). isDown and pick
// may read the health table but must not take shard locks.
func (t *affinityTable) acquire(key affinityKey, isDown func(member int) bool, pick func() (member int, provisional bool)) (member int, rebound bool) {
	now := t.now()
	shard := t.shards[t.shardIndex(key.login, key.site)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	b, ok := shard.bindings[key]
	wasDown, previous := false, 0
	if ok {
		if !t.expired(b, now) {
			if !isDown(b.member) {
				b.lastUsed.Store(now.UnixNano())
				return b.member, false
			}
			wasDown, previous = true, b.member
		} else {
			delete(shard.bindings, key)
		}
	}
	member, provisional := pick()
	if wasDown && member != previous {
		// A real rebind: the site moves to another node. Picking the same
		// node again (every node is down) changes nothing for the site.
		t.rebinds.Add(1)
		rebound = true
	}
	nb := &binding{member: member, boundAt: now, provisional: provisional}
	nb.lastUsed.Store(now.UnixNano())
	shard.bindings[key] = nb
	if len(shard.bindings) > shard.cap {
		shard.evictOldest()
	}
	return member, rebound
}

// peek returns the live binding's member without refreshing it or creating
// one; ok is false when there is no live binding. Display paths use peek so
// they never create, refresh or count bindings.
func (t *affinityTable) peek(key affinityKey) (member int, ok bool) {
	now := t.now()
	shard := t.shards[t.shardIndex(key.login, key.site)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	b, found := shard.bindings[key]
	if !found || t.expired(b, now) {
		return 0, false
	}
	return b.member, true
}

func (t *affinityTable) expired(b *binding, now time.Time) bool {
	if b.provisional && now.Sub(b.boundAt) >= provisionalLifetime {
		return true
	}
	return now.UnixNano()-b.lastUsed.Load() >= int64(t.ttl)
}

// evictOldest evicts the entry with the oldest lastUsed among at most 16
// entries visited by map iteration: O(1) per insert instead of a full scan.
func (s *affinityShard) evictOldest() {
	var oldestKey affinityKey
	oldest := int64(math.MaxInt64)
	n := 0
	for key, b := range s.bindings {
		if lastUsed := b.lastUsed.Load(); lastUsed < oldest {
			oldest, oldestKey = lastUsed, key
		}
		n++
		if n >= affinityEvictSample {
			break
		}
	}
	delete(s.bindings, oldestKey)
}

// count returns the number of live bindings; expired bindings are excluded
// by the same rule acquire uses, but not deleted.
func (t *affinityTable) count() int {
	now := t.now()
	n := 0
	for _, shard := range t.shards {
		shard.mu.Lock()
		for _, b := range shard.bindings {
			if !t.expired(b, now) {
				n++
			}
		}
		shard.mu.Unlock()
	}
	return n
}

// rebindCount returns how often a binding was replaced because its node was
// down.
func (t *affinityTable) rebindCount() uint64 {
	return t.rebinds.Load()
}

// selectMember picks the member index for a new binding per the design:
// candidates are the members that are not down and have a delay; if there are
// none, the members that are not down; if there are none, all members.
// best is the lowest score; candidates within max(20 ms, 15 % of best) of
// best are near-equal, and among them the one with the highest
// fnv64a(login|site|label) wins, so equal nodes share the sites of an
// account deterministically. Returns -1 only for an empty route.
func selectMember(views []memberHealthView, login, site string) int {
	pool := make([]int, 0, len(views))
	for i, view := range views {
		if view.Known && !view.Down {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		for i, view := range views {
			if !view.Down {
				pool = append(pool, i)
			}
		}
	}
	if len(pool) == 0 {
		for i := range views {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		return -1
	}
	if !views[pool[0]].Known {
		// No delay data in the candidate set: a deterministic hash pick.
		return pickByHash(views, pool, login, site)
	}
	best := pool[0]
	for _, i := range pool[1:] {
		if views[i].Score < views[best].Score {
			best = i
		}
	}
	tolerance := 0.15 * views[best].Score
	if tolerance < 20 {
		tolerance = 20
	}
	near := make([]int, 0, len(pool))
	for _, i := range pool {
		if views[i].Score <= views[best].Score+tolerance {
			near = append(near, i)
		}
	}
	return pickByHash(views, near, login, site)
}

func pickByHash(views []memberHealthView, pool []int, login, site string) int {
	best := pool[0]
	bestHash := fnv64a(login + "|" + site + "|" + views[best].Label)
	for _, i := range pool[1:] {
		if h := fnv64a(login + "|" + site + "|" + views[i].Label); h > bestHash {
			best, bestHash = i, h
		}
	}
	return best
}

func fnv64a(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// provisionalBinding reports whether a new binding is provisional: more than
// half of the route's members still have no delay data. A provisional
// binding expires after 2 minutes and the next use selects again.
func provisionalBinding(views []memberHealthView) bool {
	unknown := 0
	for _, view := range views {
		if !view.Known {
			unknown++
		}
	}
	return unknown*2 > len(views)
}
