package core

import (
	"sync"
	"sync/atomic"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// A node can pass its probes and carry most sites and still never reach one
// destination (an address blocked on its side). The node is not down, so the
// route never leaves it, and every connection to that destination waits for an
// answer that does not come. siteBlocks notices it per (account, site, node):
// connections that sent data and got nothing back, with none that was
// answered, block that node for that site for a while, and the site's next
// connection takes the next node of the route, a worse tier included. The
// block grows on every repeat and ends at once when a connection is answered.
const (
	// silentNeeded silent connections within silentWindow, none of them
	// answered, block a node for a site.
	silentNeeded = 3
	silentWindow = 10 * time.Minute
	// answerRefresh is how often a connection that goes on being answered tells
	// the group so (the memory of an answer lasts silentWindow).
	answerRefresh = 30 * time.Second
	// siteBlockFirst is the first block; every repeat doubles it up to
	// siteBlockMax.
	siteBlockFirst = 30 * time.Minute
	siteBlockMax   = 8 * time.Hour
	// siteBlockCap bounds the table; evidence for more sites is dropped.
	siteBlockCap = 4096
	// siteBlockKeep is how long a record that blocks nothing is remembered (for
	// the doubling of a repeat).
	siteBlockKeep = 24 * time.Hour
)

// silentMinLife: a connection that waited for an answer for less than this (from
// the first bytes it sent) is a client that left, not a destination that does
// not answer. A variable only so that a test can shorten it.
var silentMinLife = 5 * time.Second

type blockKey struct {
	login, site string
	member      int
}

// blockState is what is known of one (account, site, node).
type blockState struct {
	silent  int       // silent connections in the current run
	since   time.Time // the first of them
	until   time.Time // blocked until; zero or past when not blocked
	strikes int       // blocks so far, without an answer in between
	touched time.Time
}

type siteBlocks struct {
	mu      sync.RWMutex
	m       map[blockKey]*blockState
	n       atomic.Int64 // len(m), read without the lock on the hot path
	now     func() time.Time
	minLife time.Duration
}

func newSiteBlocks(now func() time.Time) *siteBlocks {
	if now == nil {
		now = time.Now
	}
	return &siteBlocks{m: map[blockKey]*blockState{}, now: now, minLife: silentMinLife}
}

// any reports whether anything is recorded; false makes blocked free.
func (b *siteBlocks) any() bool { return b != nil && b.n.Load() > 0 }

// blocked reports whether the node is blocked for the site of the account.
func (b *siteBlocks) blocked(login, site string, member int, now time.Time) bool {
	if !b.any() {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	st := b.m[blockKey{login, site, member}]
	return st != nil && now.Before(st.until)
}

// clock is the group's time: the binding table's, which tests move.
func (g *stickyGroup) clock() time.Time {
	if g.table != nil {
		return g.table.now()
	}
	return time.Now()
}

// answered records a connection of the site that got data back: the node
// reaches it, so a block ends and the count starts over.
func (b *siteBlocks) answered(login, site string, member int) {
	if !b.any() {
		return
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.m[blockKey{login, site, member}]; st != nil {
		st.silent, st.strikes, st.until, st.touched = 0, 0, time.Time{}, now
	}
}

// silentEnd records a connection or dial timeout that waited without an answer.
// It needs no recent site answer and a node answer after the run began.
func (b *siteBlocks) silentEnd(login, site string, member int, waited time.Duration, answeredRecently bool, okAt time.Time) (time.Duration, bool) {
	if waited < b.minLife || answeredRecently {
		return 0, false
	}
	now := b.now()
	key := blockKey{login, site, member}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[key]
	if st == nil {
		if len(b.m) >= siteBlockCap {
			b.pruneLocked(now)
		}
		if len(b.m) >= siteBlockCap {
			return 0, false
		}
		st = &blockState{}
		b.m[key] = st
		b.n.Store(int64(len(b.m)))
	}
	st.touched = now
	if now.Before(st.until) {
		return 0, false // a connection that began before the block
	}
	if st.silent == 0 || now.Sub(st.since) > silentWindow {
		st.silent, st.since = 1, now
	} else {
		st.silent++
	}
	if st.silent < silentNeeded || !okAt.After(st.since) {
		return 0, false
	}
	st.strikes++
	ttl := siteBlockFirst << min(st.strikes-1, 4)
	if ttl > siteBlockMax {
		ttl = siteBlockMax
	}
	st.until, st.silent = now.Add(ttl), 0
	return ttl, true
}

// pruneLocked forgets the records that block nothing and have not been
// touched for a long time, or whose run of silent connections is over.
func (b *siteBlocks) pruneLocked(now time.Time) {
	for k, st := range b.m {
		if now.Before(st.until) {
			continue
		}
		idle := now.Sub(st.touched)
		if idle > siteBlockKeep || (st.strikes == 0 && idle > silentWindow) {
			delete(b.m, k)
		}
	}
	b.n.Store(int64(len(b.m)))
}

// blockedCount is how many (account, site, node) blocks are in force, for
// tests and status.
func (b *siteBlocks) blockedCount() int {
	if b == nil {
		return 0
	}
	now := b.now()
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, st := range b.m {
		if now.Before(st.until) {
			n++
		}
	}
	return n
}

// answered is called once per connection, when the first bytes come back
// through the member: the site works through it.
func (g *stickyGroup) answered(w *watchConn) {
	if g.blocks == nil || g.table == nil || w.siteStr == "" {
		return
	}
	g.table.noteAnswer(affinityKey{login: w.login, site: w.siteStr}, w.member, g.table.now())
	g.blocks.answered(w.login, w.siteStr, w.member)
}

// recordNoAnswer records a sufficiently long silent attempt for an up node.
// The node must answer after the attempt starts; a recent site answer gates it.
func (g *stickyGroup) recordNoAnswer(login, site string, member int, label string, started time.Time, waited time.Duration, target string, port uint16, siteHash uint64) {
	if g.blocks == nil || g.table == nil || site == "" || member < 0 || member >= len(g.healths) || g.healths[member] == nil || g.memberDown(member) {
		return
	}
	okAt := g.healths[member].lastOKAt()
	if !okAt.After(started) || g.health.down(label) {
		return
	}
	now := g.clock()
	recent := g.table.answeredWithin(affinityKey{login: login, site: site}, member, silentWindow, now)
	ttl, blocked := g.blocks.silentEnd(login, site, member, waited, recent, okAt)
	if blocked {
		g.emit(ConnEvent{Kind: ConnSiteBlocked, Login: login, Label: label, Why: MoveNoAnswer, Port: port, Site: siteHash, Target: target, Block: ttl})
	}
}

// recordDialTimeout records dials that timed out after the minimum wait.
// recordNoAnswer enforces the node-health and recent-site-answer gates.
func (g *stickyGroup) recordDialTimeout(login, site string, member int, label string, md *C.Metadata, started time.Time, took time.Duration) {
	if g.blocks == nil || g.blocks.minLife > took {
		return
	}
	var port uint16
	var hash uint64
	var target string
	if md != nil {
		port = md.DstPort
		target = destination(md.Host, md.DstIP, port)
		hash = siteHash(site)
	}
	g.recordNoAnswer(login, site, member, label, started, took, target, port, hash)
}

// ended records a connection that sent data and got nothing back. It waits
// from the first bytes, and requires an up node with a later answer and no
// recent site answer; a run blocks only after the node answers after its first end.
func (g *stickyGroup) ended(w *watchConn, ev ConnEvent) {
	if g.blocks == nil || g.table == nil || w.siteStr == "" || ev.Down != 0 || ev.Up == 0 {
		return
	}
	if w.member < 0 || w.member >= len(g.healths) || g.healths[w.member] == nil || g.memberDown(w.member) {
		return
	}
	now := g.clock()
	waited := now.Sub(time.Unix(0, w.firstUp.Load()))
	g.recordNoAnswer(w.login, w.siteStr, w.member, w.label, w.start, waited, ev.Target, w.port, w.site)
}
