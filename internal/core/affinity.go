package core

import (
	"hash/fnv"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAffinityTTL = 24 * time.Hour
	// defaultTierReturnAfter is how long a node of a better tier must have
	// been up before bindings in a worse tier are moved back to it.
	defaultTierReturnAfter = 5 * time.Minute
	provisionalLifetime    = 2 * time.Minute
	maxAffinityBindings    = 100000
	affinityShards         = 16
	affinityEvictSample    = 16
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
	mu       sync.RWMutex
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
	returns atomic.Uint64 // bindings moved back to a better tier
	// returnAfter: see defaultTierReturnAfter; negative disables the return.
	returnAfter time.Duration
	hedges      atomic.Uint64 // hedge attempts started (all groups share the table)
	hedgeWins   atomic.Uint64 // logical dials won by the hedge attempt
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
	t := &affinityTable{now: now, ttl: ttl, returnAfter: defaultTierReturnAfter}
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
// whose member is not down is returned and its lastUsed refreshed under the
// shard's read lock; the write lock is taken only on the slow path: an
// expired binding is deleted; a missing binding or one whose member is down
// calls pick once, installs the result and returns it. rebound is true only
// when an existing live binding was moved to a different member because its
// member was down; the rebind counter is incremented exactly there, once per
// real rebind (the shard lock serialises concurrent callers). isDown and pick
// may read the health table but must not take shard locks.
func (t *affinityTable) acquire(key affinityKey, isDown func(member int) bool, pick func() (member int, provisional bool)) (member int, rebound bool) {
	member, rebound, _, _ = t.acquireWith(key, isDown, nil, pick)
	return member, rebound
}

// acquireWith is acquire plus the return to a better tier: better, when not
// nil, reports that a node of a better tier than the bound member's has been
// up long enough, and a live binding whose member is not down is then picked
// again for this connection (returned is true when the site really moved).
// Open connections are not touched. like isDown, better runs under the shard
// lock and must not take shard locks. from is the member the site left when
// rebound or returned is true, otherwise -1.
func (t *affinityTable) acquireWith(key affinityKey, isDown func(member int) bool, better func(member int, now time.Time) bool, pick func() (member int, provisional bool)) (member int, rebound, returned bool, from int) {
	now := t.now()
	shard := t.shards[t.shardIndex(key.login, key.site)]
	shard.mu.RLock()
	if b, ok := shard.bindings[key]; ok && !t.expired(b, now) && !isDown(b.member) && (better == nil || !better(b.member, now)) {
		b.lastUsed.Store(now.UnixNano())
		shard.mu.RUnlock()
		return b.member, false, false, -1
	}
	shard.mu.RUnlock()

	shard.mu.Lock()
	defer shard.mu.Unlock()
	b, ok := shard.bindings[key]
	wasDown, wasWorse, previous := false, false, 0
	if ok {
		if !t.expired(b, now) {
			switch {
			case isDown(b.member):
				wasDown, previous = true, b.member
			case better != nil && better(b.member, now):
				wasWorse, previous = true, b.member
			default:
				b.lastUsed.Store(now.UnixNano())
				return b.member, false, false, -1
			}
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
	if wasWorse && member != previous {
		t.returns.Add(1)
		returned = true
	}
	nb := &binding{member: member, boundAt: now, provisional: provisional}
	nb.lastUsed.Store(now.UnixNano())
	shard.bindings[key] = nb
	if len(shard.bindings) > shard.cap {
		shard.evictOldest()
	}
	from = -1
	if rebound || returned {
		from = previous
	}
	return member, rebound, returned, from
}

// peek returns the live binding's member without refreshing it or creating
// one; ok is false when there is no live binding. Display paths use peek so
// they never create, refresh or count bindings.
func (t *affinityTable) peek(key affinityKey) (member int, ok bool) {
	now := t.now()
	shard := t.shards[t.shardIndex(key.login, key.site)]
	shard.mu.RLock()
	defer shard.mu.RUnlock()
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
		shard.mu.RLock()
		for _, b := range shard.bindings {
			if !t.expired(b, now) {
				n++
			}
		}
		shard.mu.RUnlock()
	}
	return n
}

// carryCounters continues the rebind and hedge counters of old, the table a
// new routing generation replaces.
func (t *affinityTable) carryCounters(old *affinityTable) {
	t.rebinds.Store(old.rebinds.Load())
	t.returns.Store(old.returns.Load())
	t.hedges.Store(old.hedges.Load())
	t.hedgeWins.Store(old.hedgeWins.Load())
}

// returnCount returns how often a binding was moved back to a better tier.
func (t *affinityTable) returnCount() uint64 {
	return t.returns.Load()
}

// setReturnAfter sets the stability time of a better tier from the option:
// 0 is the default, negative disables the return.
func (t *affinityTable) setReturnAfter(d time.Duration) {
	switch {
	case d == 0:
		t.returnAfter = defaultTierReturnAfter
	case d < 0:
		t.returnAfter = -1
	default:
		t.returnAfter = d
	}
}

// rebindCount returns how often a binding was replaced because its node was
// down.
func (t *affinityTable) rebindCount() uint64 {
	return t.rebinds.Load()
}

// selectMember picks the member index for a new binding per the design.
// Members are in priority tiers (memberHealthView.Tier, lower first; all
// zero when the route has no tiers): only the lowest tier that still has a
// member that is not down is considered, or the lowest tier when every member
// is down. Within it, candidates are the members that are not down and have a
// delay; if there are none, the members that are not down; if there are none,
// all of its members. best is the lowest score; candidates within
// max(20 ms, 15 % of best) of best are near-equal, and among them the one with
// the highest fnv64a(login|site|label) wins, so equal nodes share the sites of
// an account deterministically. Returns -1 only for an empty route.
func selectMember(views []memberHealthView, login, site string) int {
	tier, found := 0, false
	for _, view := range views {
		if !view.Down && (!found || view.Tier < tier) {
			tier, found = view.Tier, true
		}
	}
	if !found {
		for _, view := range views {
			if !found || view.Tier < tier {
				tier, found = view.Tier, true
			}
		}
	}
	pool := make([]int, 0, len(views))
	for i, view := range views {
		if view.Tier == tier && view.Known && !view.Down {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		for i, view := range views {
			if view.Tier == tier && !view.Down {
				pool = append(pool, i)
			}
		}
	}
	if len(pool) == 0 {
		for i, view := range views {
			if view.Tier == tier {
				pool = append(pool, i)
			}
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
