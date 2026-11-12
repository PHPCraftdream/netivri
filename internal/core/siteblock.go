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
//
// A path can also be lossy and not dead: a few connections get an answer, most
// get none. Each answer would clear the count above and the node would stay in
// use for the site for hours. So within one silentWindow, silentRatioMin silent
// connections that are at least silentRatioPercent of all the connections that
// were silent or answered at first bytes also block the node. Such a block is
// not lifted by an answer (a connection that began before it, or one that
// goes on being answered): it ends with its time, and the strikes stay while
// the node is still mostly silent for the site.
const (
	// silentNeeded silent connections within silentWindow, none of them
	// answered, block a node for a site.
	silentNeeded = 3
	silentWindow = 10 * time.Minute
	// silentRatioMin and silentRatioPercent: see above.
	silentRatioMin     = 20
	silentRatioPercent = 90
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
	siteBlockKeep          = 24 * time.Hour
	siteBlockPruneInterval = time.Minute
)

// silentMinLife: a connection that waited for an answer for less than this (from
// the first bytes it sent) is a client that left, not a destination that does
// not answer, for the plain rule. A variable only so that a test can shorten it.
var silentMinLife = 5 * time.Second

// silentRatioMinLife is the same bound for the ratio rule. Some clients give up
// after 2-3 s without an answer and retry at once (Telegram): under
// silentMinLife none of their connections would count and a dead path stays in
// use. The ratio rule needs many silent connections that are the large
// majority, which a client that only leaves early does not make.
var silentRatioMinLife = 2 * time.Second

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

	// The ratio rule: connections of the current window (it starts at
	// windowStart and lasts silentWindow) that were silent, and the ones that
	// were answered at their first bytes.
	windowStart      time.Time
	winSilent, winOK int
	ratio            bool // the block in force came from the ratio rule
	// How the last block came about, for the log: the ratio rule and the
	// connections it counted (zero for the plain rule).
	lastRatio                bool
	lastSilent, lastAnswered int
}

// count records one connection in the current window, starting a new window
// when the last one is over.
func (st *blockState) count(now time.Time, silent bool) {
	if st.windowStart.IsZero() || now.Sub(st.windowStart) > silentWindow {
		st.windowStart, st.winSilent, st.winOK = now, 0, 0
	}
	if silent {
		st.winSilent++
	} else {
		st.winOK++
	}
}

// mostlySilent reports whether the window holds enough silent connections and
// they are the large majority.
func (st *blockState) mostlySilent() bool {
	return st.winSilent >= silentRatioMin && st.winSilent*100 >= silentRatioPercent*(st.winSilent+st.winOK)
}

// block starts a block of the next length and returns it.
func (st *blockState) block(now time.Time) time.Duration {
	st.strikes++
	ttl := siteBlockFirst << min(st.strikes-1, 4)
	if ttl > siteBlockMax {
		ttl = siteBlockMax
	}
	st.until, st.silent = now.Add(ttl), 0
	return ttl
}

type siteBlocks struct {
	mu      sync.RWMutex
	m       map[blockKey]*blockState
	n       atomic.Int64 // len(m), read without the lock on the hot path
	now     func() time.Time
	minLife time.Duration // see silentMinLife
	// ratioMinLife: see silentRatioMinLife; never above minLife in effect.
	ratioMinLife time.Duration
	pruneAfter   time.Time
	pruneScans   uint64
}

func newSiteBlocks(now func() time.Time) *siteBlocks {
	if now == nil {
		now = time.Now
	}
	return &siteBlocks{m: map[blockKey]*blockState{}, now: now, minLife: silentMinLife, ratioMinLife: silentRatioMinLife}
}

// shortestLife is the least wait that counts for either rule.
func (b *siteBlocks) shortestLife() time.Duration { return min(b.minLife, b.ratioMinLife) }

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
// reaches it, so a block ends and the count starts over. first is true for the
// first bytes of a connection, false for the later reports of one that goes on
// being answered; only the first count in the window of the ratio rule. A block
// of the ratio rule stays (see the comment on the constants).
func (b *siteBlocks) answered(login, site string, member int, first bool) {
	if !b.any() {
		return
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[blockKey{login, site, member}]
	if st == nil {
		return
	}
	if first {
		st.count(now, false)
	}
	st.touched = now
	if st.ratio {
		if now.Before(st.until) || st.winSilent > st.winOK {
			return
		}
		st.ratio = false
	}
	st.silent, st.strikes, st.until = 0, 0, time.Time{}
}

// silentEnd records a connection or dial timeout that waited without an answer.
// The plain rule needs no recent site answer and a wait of minLife; the ratio
// rule counts waits from ratioMinLife; both need a node answer after the run
// (or the window) began.
func (b *siteBlocks) silentEnd(login, site string, member int, waited time.Duration, answeredRecently bool, okAt time.Time) (time.Duration, bool) {
	if waited < b.shortestLife() {
		return 0, false
	}
	now := b.now()
	key := blockKey{login, site, member}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[key]
	if st == nil {
		if len(b.m) >= siteBlockCap && !now.Before(b.pruneAfter) {
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
	st.count(now, true)
	if now.Before(st.until) {
		return 0, false // a connection that began before the block
	}
	if st.mostlySilent() && okAt.After(st.windowStart) {
		st.lastRatio, st.lastSilent, st.lastAnswered = true, st.winSilent, st.winOK
		st.ratio = true
		st.winSilent, st.winOK = 0, 0
		return st.block(now), true
	}
	if answeredRecently || waited < b.minLife {
		return 0, false
	}
	if st.silent == 0 || now.Sub(st.since) > silentWindow {
		st.silent, st.since = 1, now
	} else {
		st.silent++
	}
	if st.silent < silentNeeded || !okAt.After(st.since) {
		return 0, false
	}
	st.lastRatio, st.lastSilent, st.lastAnswered = false, 0, 0
	st.ratio = false
	return st.block(now), true
}

// blockInfo says how the last block of the node for the site came about: by the
// ratio rule (with the connections it counted) or by the plain one.
func (b *siteBlocks) blockInfo(login, site string, member int) (ratio bool, silent, answered int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if st := b.m[blockKey{login, site, member}]; st != nil {
		return st.lastRatio, st.lastSilent, st.lastAnswered
	}
	return false, 0, 0
}

// pruneLocked forgets the records that block nothing and have not been
// touched for a long time, or whose run of silent connections is over.
func (b *siteBlocks) pruneLocked(now time.Time) {
	b.pruneAfter = now.Add(siteBlockPruneInterval)
	b.pruneScans++
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
func (g *stickyGroup) answered(w *watchConn, first bool) {
	if g.blocks == nil || g.table == nil || w.siteStr == "" {
		return
	}
	g.table.noteAnswer(affinityKey{login: w.login, site: w.siteStr}, w.member, g.table.now())
	g.blocks.answered(w.login, w.siteStr, w.member, first)
}

// recordNoAnswer records a sufficiently long silent attempt for an up node.
// The node must answer after the attempt starts; a recent site answer gates it.
func (g *stickyGroup) recordNoAnswer(login, site string, member int, label string, started time.Time, waited time.Duration, target string, port uint16, siteHash uint64) {
	if g.blocks == nil || g.table == nil || site == "" || member < 0 || member >= len(g.healths) || g.healths[member] == nil || g.memberDown(member) {
		return
	}
	okAt := g.healths[member].lastOKAt()
	if !okAt.After(started) {
		return
	}
	now := g.clock()
	recent := g.table.answeredWithin(affinityKey{login: login, site: site}, member, silentWindow, now)
	ttl, blocked := g.blocks.silentEnd(login, site, member, waited, recent, okAt)
	if blocked {
		ratio, silent, answered := g.blocks.blockInfo(login, site, member)
		ev := ConnEvent{Kind: ConnSiteBlocked, Login: login, Label: label, Why: MoveNoAnswer, Port: port, Site: siteHash, Target: target, Block: ttl}
		if ratio {
			ev.Silent, ev.Answered = silent, answered
		}
		g.emit(ev)
	}
}

// recordDialTimeout records dials that timed out after the minimum wait.
// recordNoAnswer enforces the node-health and recent-site-answer gates.
func (g *stickyGroup) recordDialTimeout(login, site string, member int, label string, md *C.Metadata, started time.Time, took time.Duration) {
	if g.blocks == nil || took < g.blocks.shortestLife() {
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
