package revive

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/check"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

type testClock struct {
	mu     sync.Mutex
	now    time.Time
	ticker *testTicker
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *testClock) NewTicker(time.Duration) Ticker {
	c.ticker = &testTicker{ch: make(chan time.Time)}
	return c.ticker
}

type testTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *testTicker) Chan() <-chan time.Time { return t.ch }
func (t *testTicker) Stop()                  { t.stopped.Store(true) }

type testHop string

func (h testHop) Key() string { return string(h) }
func (testHop) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}

type fixedProber pool.ProbeOutcome

func (p fixedProber) Probe(context.Context, dial.Hop) pool.ProbeOutcome { return pool.ProbeOutcome(p) }

func testRegistration(clock *testClock, keys ...string) Registration {
	hops := make([]dial.Hop, len(keys))
	entries := make([]pool.Entry, len(keys))
	for i, key := range keys {
		hops[i] = testHop(key)
		entries[i] = pool.Entry{Key: key, Trips: 1}
	}
	p := pool.New(keys, pool.Options{Clock: clock.Now})
	p.Restore(entries, clock.Now())
	return Registration{Pool: p, Hops: hops, Prober: fixedProber(pool.ProbeSuccess)}
}

func testReviver(t *testing.T, registration Registration, options Options) *Reviver {
	t.Helper()
	if options.Timeout >= 25*time.Second {
		t.Fatal("test timeout must be shorter than probe lease")
	}
	r, err := New([]Registration{registration}, options)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRoundRevivesDueQuarantine(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "opaque-key")
	var events []Event
	r := testReviver(t, registration, Options{
		Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := registration.Pool.Snapshot()[0].State; got != pool.Healthy {
		t.Fatalf("state = %v, want healthy", got)
	}
	if len(events) != 1 || events[0] != (Event{Kind: Revived, Key: "opaque-key", Trips: 0}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestRoundHardProbeRetripsWithLongerBackoff(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "opaque-key")
	registration.Prober = fixedProber(pool.ProbeHardFailure)
	var events []Event
	r := testReviver(t, registration, Options{
		Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := registration.Pool.Snapshot()[0]
	if snapshot.State != pool.Quarantined || snapshot.Trips != 2 || !snapshot.NextProbe.Equal(clock.Now().Add(60*time.Second)) {
		t.Fatalf("after hard probe: %#v", snapshot)
	}
	if len(events) != 1 || events[0] != (Event{Kind: Quarantined, Key: "opaque-key", Trips: 2}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestRoundKeepsLeaseForExecutedInconclusiveProbe(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "inconclusive")
	var calls atomic.Int32
	registration.Prober = proberFunc(func(context.Context, dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		return pool.ProbeInconclusive
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want one executed probe", calls.Load())
	}
	if got := snapshotState(registration.Pool, "inconclusive"); got != pool.Probing {
		t.Fatalf("executed inconclusive state = %q, want probing until lease expiry", got)
	}
	if claims := registration.Pool.DueProbes(clock.Now()); len(claims) != 0 {
		t.Fatalf("executed inconclusive probe released its lease: %#v", claims)
	}
}

func TestRoundReleasesClaimsWithoutEnoughLeaseTime(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "runs", "too-late")
	var calls atomic.Int32
	registration.Prober = proberFunc(func(context.Context, dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		clock.Advance(24 * time.Second)
		return pool.ProbeInconclusive
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want only the claim with sufficient lease remaining", calls.Load())
	}
	if got := snapshotState(registration.Pool, "too-late"); got != pool.Quarantined {
		t.Fatalf("late claim state = %q, want quarantined", got)
	}
	claims := registration.Pool.DueProbes(clock.Now())
	if len(claims) != 1 || claims[0].Key() != "too-late" {
		t.Fatalf("late claim was not immediately available: %#v", claims)
	}
}

func TestRoundBoundsConcurrencyAndReportsEveryDueClaim(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "a", "b", "c", "d")
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	var calls atomic.Int32
	registration.Prober = proberFunc(func(ctx context.Context, _ dial.Hop) pool.ProbeOutcome {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
		return pool.ProbeSuccess
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: 20 * time.Second, Concurrency: 2, Clock: clock})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Round(ctx) }()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum active = %d, want exactly the configured bound 2", got)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("probe calls = %d, want all 4 due claims", got)
	}
	if claims := registration.Pool.DueProbes(clock.Now()); len(claims) != 0 {
		t.Fatalf("successful reports left %d claims due", len(claims))
	}
}

type proberFunc func(context.Context, dial.Hop) pool.ProbeOutcome

func (f proberFunc) Probe(ctx context.Context, hop dial.Hop) pool.ProbeOutcome { return f(ctx, hop) }

var _ check.Prober = proberFunc(nil)

func TestEligibilityIsCheckedBeforeLeaseAndBeforeProbe(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "blocked", "eligible")
	var calls atomic.Int32
	registration.Eligible = func(key string) bool { return key != "blocked" }
	registration.Prober = proberFunc(func(context.Context, dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		return pool.ProbeSuccess
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want only eligible hop", calls.Load())
	}
	if got := snapshotState(registration.Pool, "eligible"); got != pool.Healthy {
		t.Fatalf("successful peer state = %q, want healthy", got)
	}
	if snapshotState(registration.Pool, "blocked") != pool.Quarantined {
		t.Fatal("ineligible hop was not left quarantined")
	}
	if claims := registration.Pool.DueProbes(clock.Now()); len(claims) != 1 || claims[0].Key() != "blocked" {
		t.Fatalf("ineligible hop was not immediately leaseable: %#v", claims)
	}
}

func TestEligibilityChangeAfterClaimReleasesWithoutDial(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "changes")
	var eligibilityChecks atomic.Int32
	var calls atomic.Int32
	var events atomic.Int32
	var mutations atomic.Int32
	registration.Eligible = func(string) bool { return eligibilityChecks.Add(1) <= 2 }
	registration.Prober = proberFunc(func(context.Context, dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		return pool.ProbeSuccess
	})
	registration.Mutate = func(_ string, change func()) {
		mutations.Add(1)
		change()
	}
	r := testReviver(t, registration, Options{
		Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock,
		OnEvent: func(Event) { events.Add(1) },
	})
	if err := r.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("prober calls = %d, want no dial after eligibility changed", calls.Load())
	}
	if events.Load() != 0 {
		t.Fatalf("events = %d, want no event for releasing a skipped claim", events.Load())
	}
	if mutations.Load() != 1 {
		t.Fatalf("mutation seam calls = %d, want one release mutation", mutations.Load())
	}
	if snapshotState(registration.Pool, "changes") != pool.Quarantined {
		t.Fatal("skipped hop did not remain quarantined")
	}
	if claims := registration.Pool.DueProbes(clock.Now()); len(claims) != 1 {
		t.Fatalf("skipped hop lease was not immediately available: %#v", claims)
	}
}

func TestCancellationBeforeWorkerProbeReleasesClaim(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "cancel-before-worker")
	var eligibilityChecks atomic.Int32
	var calls atomic.Int32
	var cancel context.CancelFunc
	registration.Eligible = func(string) bool {
		if eligibilityChecks.Add(1) == 3 {
			cancel()
		}
		return true
	}
	registration.Prober = proberFunc(func(context.Context, dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		return pool.ProbeSuccess
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock})
	ctx, cancelRound := context.WithCancel(context.Background())
	cancel = cancelRound
	defer cancelRound()
	if err := r.Round(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Round error = %v, want canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("prober calls = %d, want cancellation before worker probe", calls.Load())
	}
	if got := snapshotState(registration.Pool, "cancel-before-worker"); got != pool.Quarantined {
		t.Fatalf("canceled claim state = %q, want quarantined", got)
	}
	if claims := registration.Pool.DueProbes(clock.Now()); len(claims) != 1 {
		t.Fatalf("canceled pre-worker lease was not immediately available: %#v", claims)
	}
}

func TestCancellationBeforeQueuedProbeReleasesClaim(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "running", "queued")
	started := make(chan struct{})
	var calls atomic.Int32
	registration.Prober = proberFunc(func(ctx context.Context, hop dial.Hop) pool.ProbeOutcome {
		calls.Add(1)
		if hop.Key() == "running" {
			close(started)
			<-ctx.Done()
			return pool.ProbeInconclusive
		}
		return pool.ProbeSuccess
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: 20 * time.Second, Concurrency: 1, Clock: clock})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	done := make(chan error, 1)
	go func() { done <- r.Round(ctx) }()
	select {
	case <-started:
	case <-waitCtx.Done():
		t.Fatal(waitCtx.Err())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Round error = %v, want canceled", err)
		}
	case <-waitCtx.Done():
		t.Fatal("Round did not return after cancellation: ", waitCtx.Err())
	}
	if calls.Load() != 1 {
		t.Fatalf("prober calls = %d, want only the first probe before cancellation", calls.Load())
	}
	if got := snapshotState(registration.Pool, "queued"); got != pool.Quarantined {
		t.Fatalf("queued hop state = %q, want quarantined", got)
	}
	claims := registration.Pool.DueProbes(clock.Now())
	if len(claims) != 1 || claims[0].Key() != "queued" {
		t.Fatalf("canceled queued claim was not released for immediate retry: %#v", claims)
	}
}

func TestObserveEmitsOpaqueQuarantineTransition(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "opaque-hop-key")
	registration.Pool.RecordSuccess("opaque-hop-key")
	var events []Event
	r := testReviver(t, registration, Options{
		Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	r.Observe(registration.Pool, "opaque-hop-key", func() {
		registration.Pool.RecordHard("opaque-hop-key")
		registration.Pool.RecordHard("opaque-hop-key")
		registration.Pool.RecordHard("opaque-hop-key")
	})
	if len(events) != 1 || events[0] != (Event{Kind: Quarantined, Key: "opaque-hop-key", Trips: 1}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestObserveReportsProbingToHealthyRevival(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "key")
	claim := registration.Pool.DueProbes(clock.Now())[0]
	var events []Event
	r := testReviver(t, registration, Options{
		Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	r.Observe(registration.Pool, "key", func() {
		if !registration.Pool.ReportProbe(claim, pool.ProbeSuccess) {
			t.Fatal("probe report rejected")
		}
	})
	if len(events) != 1 || events[0] != (Event{Kind: Revived, Key: "key", Trips: 0}) {
		t.Fatalf("events = %#v", events)
	}
}
func TestRoundSkipsLeaseStarvationAndRotatesClaims(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "a", "b", "c")
	called := make(map[string]int)
	var calledMu sync.Mutex
	registration.Prober = proberFunc(func(_ context.Context, hop dial.Hop) pool.ProbeOutcome {
		calledMu.Lock()
		called[hop.Key()]++
		calledMu.Unlock()
		clock.Advance(12 * time.Second)
		return pool.ProbeInconclusive
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: time.Second, Concurrency: 1, Clock: clock})
	for round := 0; round < 3; round++ {
		if err := r.Round(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(26 * time.Second)
	}
	calledMu.Lock()
	defer calledMu.Unlock()
	if called["c"] == 0 {
		t.Fatalf("rotating dispatch never probed late claim c: calls=%v", called)
	}
}

func TestCanceledProbeRoundWaitsAndRunStopsTicker(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	registration := testRegistration(clock, "opaque-key")
	started := make(chan struct{})
	registration.Prober = proberFunc(func(ctx context.Context, _ dial.Hop) pool.ProbeOutcome {
		close(started)
		<-ctx.Done()
		return pool.ProbeInconclusive
	})
	r := testReviver(t, registration, Options{Interval: time.Second, Timeout: 20 * time.Second, Concurrency: 1, Clock: clock})
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()
	select {
	case <-started:
	case <-waitCtx.Done():
		t.Fatal(waitCtx.Err())
	}
	cancelRun()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want canceled", err)
		}
	case <-waitCtx.Done():
		t.Fatal("Run did not return after cancellation: ", waitCtx.Err())
	}
	if clock.ticker == nil || !clock.ticker.stopped.Load() {
		t.Fatal("Run did not stop its ticker")
	}
}
