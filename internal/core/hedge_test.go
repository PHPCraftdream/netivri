package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// trackedConn records whether Close was called.
type trackedConn struct {
	fakeConn
	closed atomic.Bool
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)
	return nil
}

// scriptedDial is one queued member dial behaviour.
type scriptedDial struct {
	wait chan struct{} // non-nil: the dial blocks until this is closed
	conn C.Conn
	err  error
}

// scriptedMember serves queued dial scripts in call order and records every
// received metadata pointer. It deliberately ignores attempt contexts so
// tests release attempts deterministically.
type scriptedMember struct {
	name      string
	mu        sync.Mutex
	scripts   []scriptedDial
	next      int
	metadatas []*C.Metadata
	started   chan struct{} // one token per started dial
}

func newScriptedMember(name string, scripts ...scriptedDial) *scriptedMember {
	return &scriptedMember{
		name:    name,
		scripts: scripts,
		started: make(chan struct{}, 16),
	}
}

func (m *scriptedMember) Name() string { return m.name }

func (m *scriptedMember) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	m.mu.Lock()
	i := m.next
	m.next++
	var s scriptedDial
	if i < len(m.scripts) {
		s = m.scripts[i]
	}
	m.metadatas = append(m.metadatas, metadata)
	m.mu.Unlock()
	select {
	case m.started <- struct{}{}:
	default:
	}
	if s.wait != nil {
		<-s.wait
	}
	return s.conn, s.err
}

func (m *scriptedMember) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	return nil, errors.New("scriptedMember: packet connections not supported")
}

func (m *scriptedMember) SupportUDP() bool              { return false }
func (m *scriptedMember) IsL3Protocol(*C.Metadata) bool { return false }

func (m *scriptedMember) clonePointers() []*C.Metadata {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*C.Metadata(nil), m.metadatas...)
}

// fakeHedgeTimer is a hedge timer the test fires manually.
type fakeHedgeTimer struct {
	fire    chan time.Time
	stopped atomic.Bool
}

func (f *fakeHedgeTimer) C() <-chan time.Time { return f.fire }
func (f *fakeHedgeTimer) Stop()               { f.stopped.Store(true) }

// useFakeHedgeTimer replaces the timer seam with a deterministic fake for
// the test and restores it on cleanup. It returns a function firing the
// current timer (once) and a function reporting whether it was stopped.
func useFakeHedgeTimer(t *testing.T) (fire func(), stopped func() bool) {
	t.Helper()
	old := newHedgeTimer
	var current atomic.Pointer[fakeHedgeTimer]
	newHedgeTimer = func(time.Duration) hedgeTimer {
		fake := &fakeHedgeTimer{fire: make(chan time.Time, 1)}
		current.Store(fake)
		return fake
	}
	t.Cleanup(func() { newHedgeTimer = old })
	return func() {
			if fake := current.Load(); fake != nil {
				fake.fire <- time.Now()
			}
		}, func() bool {
			fake := current.Load()
			return fake != nil && fake.stopped.Load()
		}
}

// newHedgeTestGroup builds a one-member group with hedging enabled.
func newHedgeTestGroup(t *testing.T, m *scriptedMember, observer Observer) (*stickyGroup, *healthTable) {
	t.Helper()
	health := newHealthTable()
	g := newStickyGroupMembers("netivri-group-0", []member{m}, []string{m.name},
		newAffinityTable(nil, 0), health, observer)
	g.hedgeAll = true
	return g, health
}

// waitClosed polls until cond holds; only for conditions another goroutine
// produces asynchronously (the collector closing a late connection).
func waitClosed(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeoutUnit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

const testTimeoutUnit = 2 * time.Second

type dialOutcome struct {
	conn C.Conn
	err  error
}

// dialAsync runs DialContext on its own goroutine.
func dialAsync(g *stickyGroup, md *C.Metadata) <-chan dialOutcome {
	out := make(chan dialOutcome, 1)
	go func() {
		conn, err := g.DialContext(context.Background(), md)
		out <- dialOutcome{conn: conn, err: err}
	}()
	return out
}

// TestHedgeWinsAndLoserClosed: the primary is slow, the hedge attempt wins,
// the cancelled primary is collected and its late connection is closed, and
// each attempt dials with its own metadata.
func TestHedgeWinsAndLoserClosed(t *testing.T) {
	hold := make(chan struct{})
	c1 := &trackedConn{fakeConn: fakeConn{}}
	c2 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a",
		scriptedDial{wait: hold, conn: c1},
		scriptedDial{conn: c2},
	)
	obs := &recordedObserver{}
	g, health := newHedgeTestGroup(t, m, obs)
	fire, _ := useFakeHedgeTimer(t)
	md := dialMetadata("alice", "www.example.com")

	out := dialAsync(g, md)
	<-m.started // primary attempt started
	fire()
	<-m.started // hedge attempt started
	result := <-out
	if result.err != nil {
		t.Fatalf("DialContext: %v", result.err)
	}
	if result.conn != C.Conn(c2) {
		t.Fatal("the hedge attempt did not win")
	}
	if got := g.table.hedgeCount(); got != 1 {
		t.Fatalf("hedges = %d, want 1", got)
	}
	if got := g.table.hedgeWinCount(); got != 1 {
		t.Fatalf("hedgeWins = %d, want 1", got)
	}
	dials := obs.dialSamples()
	if len(dials) != 1 || dials[0].label != "a" || !dials[0].ok {
		t.Fatalf("observer dials = %+v, want one ok call for a", dials)
	}
	if !health.entries["a"].hasDial {
		t.Fatal("the logical success was not recorded")
	}
	// Attempts must not share the metadata pointer.
	pointers := m.clonePointers()
	if len(pointers) != 2 || pointers[0] == pointers[1] {
		t.Fatalf("attempt metadata pointers = %v, want two distinct", pointers)
	}
	// Release the cancelled primary: its late connection must be closed.
	close(hold)
	waitClosed(t, func() bool { return c1.closed.Load() })
	if c2.closed.Load() {
		t.Fatal("the winning connection was closed")
	}
}

// TestHedgePrimaryWinsBeforeThreshold: a fast primary means no hedge, and
// the timer is stopped.
func TestHedgePrimaryWinsBeforeThreshold(t *testing.T) {
	c1 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a", scriptedDial{conn: c1})
	obs := &recordedObserver{}
	g, _ := newHedgeTestGroup(t, m, obs)
	_, stopped := useFakeHedgeTimer(t)

	result := <-dialAsync(g, dialMetadata("alice", "www.example.com"))
	if result.err != nil {
		t.Fatalf("DialContext: %v", result.err)
	}
	if m.clonePointers() == nil || len(m.clonePointers()) != 1 {
		t.Fatalf("dials = %d, want 1", len(m.clonePointers()))
	}
	if got := g.table.hedgeCount(); got != 0 {
		t.Fatalf("hedges = %d, want 0", got)
	}
	if got := g.table.hedgeWinCount(); got != 0 {
		t.Fatalf("hedgeWins = %d, want 0", got)
	}
	if dials := obs.dialSamples(); len(dials) != 1 || !dials[0].ok {
		t.Fatalf("observer dials = %+v, want one ok call", dials)
	}
	if !stopped() {
		t.Fatal("the hedge timer was not stopped on the fast path")
	}
	if c1.closed.Load() {
		t.Fatal("the returned connection was closed")
	}
}

// TestHedgeBothFailRecordsOneFailure: both attempts fail, the primary error
// is returned, and exactly one failure is recorded.
func TestHedgeBothFailRecordsOneFailure(t *testing.T) {
	hold := make(chan struct{})
	primaryErr := errors.New("primary stalled out")
	hedgeErr := errors.New("hedge refused")
	m := newScriptedMember("a",
		scriptedDial{wait: hold, err: primaryErr},
		scriptedDial{err: hedgeErr},
	)
	obs := &recordedObserver{}
	g, health := newHedgeTestGroup(t, m, obs)
	fire, _ := useFakeHedgeTimer(t)

	out := dialAsync(g, dialMetadata("alice", "www.example.com"))
	<-m.started
	fire()
	<-m.started
	close(hold) // the stalled primary now fails too
	result := <-out
	if !errors.Is(result.err, primaryErr) {
		t.Fatalf("error = %v, want the primary error %v", result.err, primaryErr)
	}
	if dials := obs.dialSamples(); len(dials) != 1 || dials[0].ok {
		t.Fatalf("observer dials = %+v, want exactly one failure", dials)
	}
	if got := g.table.hedgeCount(); got != 1 || g.table.hedgeWinCount() != 0 {
		t.Fatalf("hedges/hedgeWins = %d/%d, want 1/0",
			g.table.hedgeCount(), g.table.hedgeWinCount())
	}
	if health.entries["a"].consDialFail != 1 {
		t.Fatalf("consecutive dial failures = %d, want 1", health.entries["a"].consDialFail)
	}
}

// TestHedgeFailsPrimaryWins: a failed hedge attempt is not recorded as a
// failure; the primary still wins and is returned.
func TestHedgeFailsPrimaryWins(t *testing.T) {
	hold := make(chan struct{})
	c1 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a",
		scriptedDial{wait: hold, conn: c1},
		scriptedDial{err: errors.New("hedge refused")},
	)
	obs := &recordedObserver{}
	g, _ := newHedgeTestGroup(t, m, obs)
	fire, _ := useFakeHedgeTimer(t)

	out := dialAsync(g, dialMetadata("alice", "www.example.com"))
	<-m.started
	fire()
	<-m.started
	close(hold) // the hedge failed at once; now the primary succeeds
	result := <-out
	if result.err != nil {
		t.Fatalf("DialContext: %v", result.err)
	}
	if result.conn != C.Conn(c1) {
		t.Fatal("the primary attempt did not win after the hedge failed")
	}
	if dials := obs.dialSamples(); len(dials) != 1 || !dials[0].ok {
		t.Fatalf("observer dials = %+v, want one ok call", dials)
	}
	if got := g.table.hedgeWinCount(); got != 0 {
		t.Fatalf("hedgeWins = %d, want 0", got)
	}
}

// TestHedgeCallerCancellationRecordsNothing: cancelling the caller's
// context ends the logical dial without any record; both attempts are
// collected and their late connections closed.
func TestHedgeCallerCancellationRecordsNothing(t *testing.T) {
	hold1 := make(chan struct{})
	hold2 := make(chan struct{})
	c1 := &trackedConn{fakeConn: fakeConn{}}
	c2 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a",
		scriptedDial{wait: hold1, conn: c1},
		scriptedDial{wait: hold2, conn: c2},
	)
	obs := &recordedObserver{}
	g, health := newHedgeTestGroup(t, m, obs)
	fire, _ := useFakeHedgeTimer(t)

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan dialOutcome, 1)
	go func() {
		conn, err := g.DialContext(ctx, dialMetadata("alice", "www.example.com"))
		out <- dialOutcome{conn: conn, err: err}
	}()
	<-m.started
	fire()
	<-m.started
	cancel()
	result := <-out
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", result.err)
	}
	if dials := obs.dialSamples(); len(dials) != 0 {
		t.Fatalf("observer dials = %+v, want none", dials)
	}
	if health.entries["a"].ringLen != 0 || health.entries["a"].hasDial {
		t.Fatal("a cancelled logical dial was recorded")
	}
	close(hold1)
	close(hold2)
	waitClosed(t, func() bool { return c1.closed.Load() && c2.closed.Load() })
}

// TestHedgeBudgetExhaustedNoHedge: without a token the timer fires but no
// second attempt is started.
func TestHedgeBudgetExhaustedNoHedge(t *testing.T) {
	hold := make(chan struct{})
	c1 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a", scriptedDial{wait: hold, conn: c1})
	obs := &recordedObserver{}
	g, health := newHedgeTestGroup(t, m, obs)
	health.entries["a"].hedgeUnits.Store(0) // budget drained
	fire, _ := useFakeHedgeTimer(t)

	out := dialAsync(g, dialMetadata("alice", "www.example.com"))
	<-m.started
	fire()
	close(hold)
	result := <-out
	if result.err != nil || result.conn != C.Conn(c1) {
		t.Fatalf("outcome = %+v, want the primary connection", result)
	}
	if got := g.table.hedgeCount(); got != 0 {
		t.Fatalf("hedges = %d, want 0", got)
	}
	if n := len(m.clonePointers()); n != 1 {
		t.Fatalf("dials = %d, want 1", n)
	}
	// The completed dial refilled the budget even though it was empty.
	if units := health.entries["a"].hedgeUnits.Load(); units != hedgeBudgetRefill {
		t.Fatalf("budget units after one dial = %d, want %d", units, hedgeBudgetRefill)
	}
}

// TestHedgeBudgetTakeAndRefill: the bucket holds 4 tokens, a hedge costs
// one, ten dials refill one token.
func TestHedgeBudgetTakeAndRefill(t *testing.T) {
	var h nodeHealth
	h.initHedgeBudget()
	for i := 0; i < 4; i++ {
		if !h.hedgeTake() {
			t.Fatalf("hedge take %d failed, want success", i+1)
		}
	}
	if h.hedgeTake() {
		t.Fatal("the fifth hedge must be refused")
	}
	for i := 0; i < 9; i++ {
		h.hedgeRefillDial()
	}
	if h.hedgeTake() {
		t.Fatal("nine dials must not refill a whole token")
	}
	h.hedgeRefillDial()
	if !h.hedgeTake() {
		t.Fatal("ten dials must refill one token")
	}
	// Refills cap at the bucket size.
	for i := 0; i < 1000; i++ {
		h.hedgeRefillDial()
	}
	if units := h.hedgeUnits.Load(); units != hedgeBudgetCap {
		t.Fatalf("units = %d, want capped at %d", units, hedgeBudgetCap)
	}
}

// TestHedgeThresholdClamp: threshold = clamp(2 x dial EWMA, 250 ms, 2 s),
// 1 s without a dial EWMA.
func TestHedgeThresholdClamp(t *testing.T) {
	tests := []struct {
		name    string
		dialMS  float64
		hasDial bool
		want    time.Duration
	}{
		{name: "no ewma", want: time.Second},
		{name: "small clamped to min", dialMS: 1, hasDial: true, want: hedgeThresholdMin},
		{name: "exactly min", dialMS: 125, hasDial: true, want: 250 * time.Millisecond},
		{name: "inside range", dialMS: 300, hasDial: true, want: 600 * time.Millisecond},
		{name: "exactly max", dialMS: 1000, hasDial: true, want: hedgeThresholdMax},
		{name: "large clamped to max", dialMS: 10000, hasDial: true, want: hedgeThresholdMax},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var h nodeHealth
			if test.hasDial {
				h.hasDial = true
				h.dialEWMS = test.dialMS
			}
			if got := h.hedgeThreshold(); got != test.want {
				t.Fatalf("threshold = %v, want %v", got, test.want)
			}
		})
	}
	table := newHealthTable()
	table.register([]string{"a"})
	if got := table.hedgeThreshold("a"); got != time.Second {
		t.Fatalf("table threshold without EWMA = %v, want 1 s", got)
	}
	if got := table.hedgeThreshold("unknown"); got != hedgeThresholdDefault {
		t.Fatalf("unknown label threshold = %v, want the default", got)
	}
}

// TestHedgeNonHedgeableMemberNeverHedges: without hedgeAll and without real
// proxies a fake member is never hedged; the plain path records normally.
func TestHedgeNonHedgeableMemberNeverHedges(t *testing.T) {
	c1 := &trackedConn{fakeConn: fakeConn{}}
	m := newScriptedMember("a", scriptedDial{conn: c1})
	obs := &recordedObserver{}
	health := newHealthTable()
	g := newStickyGroupMembers("netivri-group-0", []member{m}, []string{"a"},
		newAffinityTable(nil, 0), health, obs)
	fire, _ := useFakeHedgeTimer(t)
	_ = fire

	result := <-dialAsync(g, dialMetadata("alice", "www.example.com"))
	if result.err != nil {
		t.Fatalf("DialContext: %v", result.err)
	}
	if got := g.table.hedgeCount(); got != 0 {
		t.Fatalf("hedges = %d, want 0", got)
	}
	if dials := obs.dialSamples(); len(dials) != 1 || !dials[0].ok {
		t.Fatalf("observer dials = %+v, want one ok call", dials)
	}
}

// TestHedgeEligibleTypes: only verified-lazy types are hedgeable.
func TestHedgeEligibleTypes(t *testing.T) {
	if !hedgeEligible(C.Vless) {
		t.Fatal("VLESS must be hedgeable")
	}
	for _, typ := range []C.AdapterType{
		C.Vmess, C.Shadowsocks, C.Trojan, C.Socks5, C.Http, C.Direct,
	} {
		if hedgeEligible(typ) {
			t.Fatalf("adapter type %v must not be hedgeable", typ)
		}
	}
}

// TestHedgeConcurrentDials: concurrent hedged dials stay race-free and all
// succeed with fast members (no hedge fires without the fake timer).
func TestHedgeConcurrentDials(t *testing.T) {
	m := newScriptedMember("a")
	for i := 0; i < 80; i++ {
		m.scripts = append(m.scripts, scriptedDial{conn: &trackedConn{fakeConn: fakeConn{}}})
	}
	obs := &recordedObserver{}
	g, _ := newHedgeTestGroup(t, m, obs)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				md := dialMetadata("user", "site.example")
				if _, err := g.DialContext(context.Background(), md); err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("dial: %v", err)
	}
	if dials := obs.dialSamples(); len(dials) != 80 {
		t.Fatalf("observer dials = %d, want 80", len(dials))
	}
	if got := g.table.hedgeCount(); got != 0 {
		t.Fatalf("hedges = %d, want 0", got)
	}
}

// TestHedgeRetryOnDownStillWorks: a failing dial that makes the node down is
// retried once on the re-selected member, through the hedged path.
func TestHedgeRetryOnDownStillWorks(t *testing.T) {
	a := newScriptedMember("a", scriptedDial{err: errors.New("a refused")})
	b := newScriptedMember("b", scriptedDial{conn: &trackedConn{fakeConn: fakeConn{}}})
	obs := &recordedObserver{}
	health := newHealthTable()
	health.register([]string{"a", "b"})
	g := newStickyGroupMembers("netivri-group-0", []member{a, b}, []string{"a", "b"},
		newAffinityTable(nil, 0), health, obs)
	g.healths[0] = health.health("a")
	g.healths[1] = health.health("b")
	g.hedgeAll = true
	health.recordProbe("a", true, 50*time.Millisecond)
	health.recordProbe("b", true, 100*time.Millisecond)
	if index, _ := g.choose("alice", "example.com"); index != 0 {
		t.Fatalf("site bound to member %d, want 0", index)
	}
	// Two prior failures: this one makes a down, so the site rebinds to b.
	health.recordDial("a", false, 0)
	health.recordDial("a", false, 0)
	a.scripts[0] = scriptedDial{err: errors.New("a refused")}

	out := dialAsync(g, dialMetadata("alice", "www.example.com"))
	result := <-out
	if result.err != nil {
		t.Fatalf("retry dial: %v", result.err)
	}
	if got := g.table.rebindCount(); got != 1 {
		t.Fatalf("rebinds = %d, want 1", got)
	}
	dials := obs.dialSamples()
	if len(dials) != 2 || dials[0].label != "a" || dials[0].ok || dials[1].label != "b" || !dials[1].ok {
		t.Fatalf("observer dials = %+v, want failed a then ok b", dials)
	}
}
