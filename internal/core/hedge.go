package core

import (
	"context"
	"errors"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// Hedge dial tunables from docs/design/latency.md.
const (
	hedgeThresholdMin     = 250 * time.Millisecond
	hedgeThresholdMax     = 2 * time.Second
	hedgeThresholdDefault = time.Second

	// The per-member hedge budget in fixed-point units of 1/10 token kept
	// in nodeHealth.hedgeUnits: capacity 4 tokens, one token per hedge
	// attempt, one unit refilled per completed dial. All access is atomic
	// compare-and-swap: no lock on the dial hot path.
	hedgeBudgetCap    = 40
	hedgeBudgetCost   = 10
	hedgeBudgetRefill = 1
)

// hedgeEligible reports whether an adapter type sends nothing to the target
// before the first payload, so a cancelled duplicate attempt leaves no trace
// at the target. Verified in the module sources: VLESS writes its request
// header lazily on the first Write (transport/vless/conn.go). Vmess
// (sing-vmess DialConn writes the handshake eagerly) and Shadowsocks
// (sing-shadowsocks2 DialConn writes the request eagerly) and all eager
// types (trojan, socks5, http, direct) are excluded. Replaced in unit tests
// to enable fake or socks5 members; restore with t.Cleanup.
var hedgeEligible = func(t C.AdapterType) bool {
	return t == C.Vless
}

// hedgeTimer waits for the hedge threshold; injectable for tests.
type hedgeTimer interface {
	C() <-chan time.Time
	Stop()
}

type realHedgeTimer struct{ timer *time.Timer }

func (t realHedgeTimer) C() <-chan time.Time { return t.timer.C }
func (t realHedgeTimer) Stop()               { t.timer.Stop() }

// newHedgeTimer is replaced in unit tests with a deterministic fake.
var newHedgeTimer = func(d time.Duration) hedgeTimer {
	return realHedgeTimer{timer: time.NewTimer(d)}
}

// initHedgeBudget makes the hedge budget available; the health table calls
// it when it creates a record.
func (h *nodeHealth) initHedgeBudget() { h.hedgeUnits.Store(hedgeBudgetCap) }

// hedgeTake consumes one hedge token; false when the budget is exhausted.
func (h *nodeHealth) hedgeTake() bool {
	for {
		units := h.hedgeUnits.Load()
		if units < hedgeBudgetCost {
			return false
		}
		if h.hedgeUnits.CompareAndSwap(units, units-hedgeBudgetCost) {
			return true
		}
	}
}

// hedgeRefillDial credits one completed dial to the hedge budget, capped.
func (h *nodeHealth) hedgeRefillDial() {
	for {
		units := h.hedgeUnits.Load()
		if units >= hedgeBudgetCap {
			return
		}
		next := units + hedgeBudgetRefill
		if next > hedgeBudgetCap {
			next = hedgeBudgetCap
		}
		if h.hedgeUnits.CompareAndSwap(units, next) {
			return
		}
	}
}

// clampHedgeThreshold clamps 2 x dial EWMA into [250 ms, 2 s].
func clampHedgeThreshold(d time.Duration) time.Duration {
	if d < hedgeThresholdMin {
		return hedgeThresholdMin
	}
	if d > hedgeThresholdMax {
		return hedgeThresholdMax
	}
	return d
}

// hedgeThreshold is the dial threshold after which a second attempt is
// started: clamp(2 x dial EWMA, 250 ms, 2 s), or 1 s without a dial EWMA.
func (h *nodeHealth) hedgeThreshold() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasDial {
		return hedgeThresholdDefault
	}
	return clampHedgeThreshold(time.Duration(2 * h.dialEWMS * float64(time.Millisecond)))
}

// hedgeThreshold returns the hedge threshold of a registered member label.
func (t *healthTable) hedgeThreshold(label string) time.Duration {
	if h := t.entries[label]; h != nil {
		return h.hedgeThreshold()
	}
	return hedgeThresholdDefault
}

// hedgeCount returns how many hedge attempts were started.
func (t *affinityTable) hedgeCount() uint64 { return t.hedges.Load() }

// hedgeWinCount returns how many logical dials were won by a hedge attempt.
func (t *affinityTable) hedgeWinCount() uint64 { return t.hedgeWins.Load() }

// hedgeResult is the outcome of one dial attempt.
type hedgeResult struct {
	conn C.Conn
	err  error
}

// collectHedge waits for one cancelled attempt and closes the connection it
// may still have produced, so no loser attempt leaks.
func collectHedge(ch <-chan hedgeResult) {
	r := <-ch
	if r.conn != nil {
		_ = r.conn.Close()
	}
}

// hedgeable reports whether the member at index may be hedged: fake members
// only through the test field hedgeAll, real ones by their adapter type via
// the hedgeEligible predicate.
func (g *stickyGroup) hedgeable(index int) bool {
	if g.hedgeAll {
		return true
	}
	if index < 0 || index >= len(g.proxies) || g.proxies[index] == nil {
		return false
	}
	return hedgeEligible(g.proxies[index].Type())
}

// dialMember performs one logical dial on members[index]: hedged when the
// member is hedgeable, plain otherwise. recorded is false when the caller's
// context ended the dial before any attempt completed; the caller then makes
// no health or observer record and no budget refill.
func (g *stickyGroup) dialMember(ctx context.Context, index int, metadata *C.Metadata) (C.Conn, error, time.Duration, bool) {
	m := g.members[index]
	if !g.hedgeable(index) {
		start := time.Now()
		conn, err := m.DialContext(ctx, metadata)
		return conn, err, time.Since(start), true
	}
	h := g.healths[index]
	threshold := hedgeThresholdDefault
	if h != nil {
		threshold = h.hedgeThreshold()
	}
	return g.dialHedged(ctx, m, metadata, threshold, h)
}

// dialHedged dials m once; if the attempt is still running after threshold,
// one more attempt to the SAME member is started with a cloned metadata and
// its own context, consuming one budget token. The first success wins; the
// loser attempt is cancelled and a collector goroutine closes the connection
// it may still return. The primary may use the original metadata and runs in
// its own goroutine so the winner can be selected. recorded is false only
// when the caller cancelled the logical dial (a deadline still counts).
func (g *stickyGroup) dialHedged(ctx context.Context, m member, metadata *C.Metadata, threshold time.Duration, h *nodeHealth) (C.Conn, error, time.Duration, bool) {
	start := time.Now()
	actx, acancel := context.WithCancel(ctx)
	defer acancel()
	// A caller that gave up says nothing about the node; an expired deadline
	// (the tunnel's dial timeout) does.
	recordable := func() bool { return !errors.Is(ctx.Err(), context.Canceled) }

	// The timer and the hedge metadata are prepared before the primary
	// starts: a primary that reports its start finds the timer in place, and
	// the clone never races with the primary's own use of the original.
	timer := newHedgeTimer(threshold)
	defer timer.Stop()
	hedgeMetadata := metadata.Clone()

	primary := make(chan hedgeResult, 1)
	go func() {
		conn, err := m.DialContext(actx, metadata)
		primary <- hedgeResult{conn: conn, err: err}
	}()

	var (
		hedge         chan hedgeResult // nil until a hedge attempt starts
		hcancel       context.CancelFunc
		hedgeDone     bool
		primaryDone   bool
		primaryResult hedgeResult
	)
	defer func() {
		if hcancel != nil {
			hcancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			acancel()
			if !primaryDone {
				go collectHedge(primary)
			}
			if hcancel != nil && !hedgeDone {
				hcancel()
				go collectHedge(hedge)
			}
			return nil, ctx.Err(), time.Since(start), recordable()
		case <-timer.C():
			if hedgeDone || hcancel != nil {
				continue
			}
			// One token per hedge; without one no hedge is started.
			if h == nil || !h.hedgeTake() {
				continue
			}
			g.table.hedges.Add(1)
			hedgeCtx, cancel := context.WithCancel(ctx)
			hcancel = cancel
			hedge = make(chan hedgeResult, 1)
			results := hedge
			go func() {
				conn, err := m.DialContext(hedgeCtx, hedgeMetadata)
				results <- hedgeResult{conn: conn, err: err}
			}()
		case r := <-hedge:
			hedgeDone = true
			if r.err == nil {
				acancel()
				if !primaryDone {
					go collectHedge(primary)
				}
				g.table.hedgeWins.Add(1)
				return r.conn, nil, time.Since(start), true
			}
			if primaryDone {
				// Every started attempt failed: the primary error is
				// returned and recorded as the one logical failure.
				return nil, primaryResult.err, time.Since(start), recordable()
			}
			// The hedge failed first: keep waiting for the primary.
		case r := <-primary:
			primaryDone = true
			primaryResult = r
			if r.err == nil {
				if hcancel != nil && !hedgeDone {
					hcancel()
					go collectHedge(hedge)
				}
				return r.conn, nil, time.Since(start), true
			}
			if hcancel == nil {
				// Failed before the threshold: no hedge is started and the
				// existing retry-on-down logic applies.
				return nil, r.err, time.Since(start), recordable()
			}
			if hedgeDone {
				return nil, r.err, time.Since(start), recordable()
			}
			// The hedge is still running; wait for it.
		}
	}
}
