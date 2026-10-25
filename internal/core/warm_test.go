package core

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
)

func TestNormalizeWarmOptions(t *testing.T) {
	o, err := normalizeWarmOptions(WarmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if o.ProbeInterval != 45*time.Second || o.ProbeTimeout != 5*time.Second || o.ProbeURL != defaultProbeURL || o.PoolTTL != 30*time.Second {
		t.Fatalf("defaults: %#v", o)
	}
	if o.ProbeConcurrency != defaultProbeConcurrency {
		t.Fatalf("ProbeConcurrency default = %d, want %d", o.ProbeConcurrency, defaultProbeConcurrency)
	}
	for _, invalid := range []WarmOptions{{ProbeInterval: -1}, {ProbeTimeout: -1}, {PoolTTL: -1}, {PoolSize: -1}, {ProbeConcurrency: -1}, {ProbeURL: "file:///tmp"}} {
		if _, err := normalizeWarmOptions(invalid); err == nil {
			t.Errorf("accepted invalid options %#v", invalid)
		}
	}
}

func TestIsWarmLeafNamesAndTypes(t *testing.T) {
	p := testWarmProxy{name: "netivri-leaf", typ: C.Socks5}
	if !isWarmLeaf(p) {
		t.Fatal("prefixed leaf skipped")
	}
	for _, name := range []string{"leaf"} {
		if isWarmLeaf(testWarmProxy{name: name}) {
			t.Errorf("probed proxy named %q", name)
		}
	}
	for _, typ := range []C.AdapterType{C.PassRule, C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance, C.Direct, C.Dns} {
		if isWarmLeaf(testWarmProxy{name: "netivri-group", typ: typ}) {
			t.Errorf("probed non-leaf type %v", typ)
		}
	}
}

func TestProbeResultAndSnapshot(t *testing.T) {
	clock := testWarmClock{now: time.Unix(42, 0)}
	calls := 0
	w := &warmer{options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: "https://probe.invalid/"}, clock: clock}
	p := testWarmProxy{name: "netivri-a"}
	w.proxies = []C.Proxy{p}
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		calls++
		if calls == 1 {
			return 17, nil
		}
		return 99, net.ErrClosed
	}
	w.probeAll(context.Background())
	w.probeAll(context.Background())
	s := w.Stats()
	if s.Probes != 2 || s.ProbeFailures != 1 || len(s.Nodes) != 1 || s.Nodes[0].Alive || s.Nodes[0].Delay != 17*time.Millisecond || !s.Nodes[0].CheckedAt.Equal(clock.now) {
		t.Fatalf("unexpected stats: %#v", s)
	}
	s.Nodes[0].Name = "changed"
	if w.Stats().Nodes[0].Name != p.Name() {
		t.Fatal("Stats returned aliased snapshot")
	}
}

func TestRunLoopAndClose(t *testing.T) {
	ticks := make(chan time.Time)
	clock := runWarmClock{testWarmClock{now: time.Unix(100, 0)}, &runWarmTicker{ticks: ticks}}
	w := &warmer{done: make(chan struct{}), options: WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: clock}
	calls := make(chan error, 3)
	var invocations int
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-run", typ: C.Socks5}}
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		invocations++
		if invocations == 1 {
			calls <- net.ErrClosed
			return 0, net.ErrClosed
		}
		calls <- nil
		return 23, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
	t.Cleanup(w.Close)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial probe did not run")
	}
	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("ticker send timed out")
	}
	select {
	case err := <-calls:
		if err != nil {
			t.Fatalf("second probe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ticker did not trigger probe")
	}
	w.Close()
	w.Close()
	select {
	case <-w.done:
	default:
		t.Fatal("Close returned before run loop stopped")
	}
}

func TestProbeBackoffIntervalSchedule(t *testing.T) {
	base := 45 * time.Second
	for _, c := range []struct {
		rounds int
		want   time.Duration
	}{
		{0, base}, {1, base}, {2, 2 * base}, {3, 4 * base}, {4, probeDownMaxInterval}, {100, probeDownMaxInterval},
	} {
		if got := probeBackoffInterval(base, c.rounds); got != c.want {
			t.Errorf("probeBackoffInterval(45s, %d) = %v, want %v", c.rounds, got, c.want)
		}
	}
	// A non-default base is capped too.
	if got := probeBackoffInterval(4*time.Minute, 3); got != probeDownMaxInterval {
		t.Errorf("probeBackoffInterval(4m, 3) = %v, want %v", got, probeDownMaxInterval)
	}
}

// A node that stays down is probed at 45 s, 90 s, 3 m and then every
// 10 minutes: with rounds every 45 s it is probed in rounds 1, 2, 4, 8
// and 22 only (round 8 probes at t=315 s, round 22 at t=945 s >= 915 s).
func TestProbeBackoffSkipsDownNodeInRounds(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-dead"}}
	w.down = func(string) bool { return true }
	round := 0
	var probed []int
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		probed = append(probed, round)
		return 0, net.ErrClosed
	}
	for i := 0; i < 22; i++ {
		round = i + 1
		w.probeAll(context.Background())
		w.clock = testWarmClock{now: time.Unix(1, 0).Add(time.Duration(i+1) * 45 * time.Second)}
	}
	want := []int{1, 2, 4, 8, 22}
	if len(probed) != len(want) {
		t.Fatalf("down node probed in rounds %v, want %v", probed, want)
	}
	for i, r := range want {
		if probed[i] != r {
			t.Fatalf("down node probed in rounds %v, want %v", probed, want)
		}
	}
}

// A node that is not down any more (for example a successful dial
// cleared the health counters) is probed every round again.
func TestProbeBackoffResetsOnRecovery(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-flap"}}
	down := true
	w.down = func(string) bool { return down }
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		if down {
			return 0, net.ErrClosed
		}
		return 10, nil
	}
	for i := 0; i < 3; i++ { // rounds 1 and 2 probe, round 3 is skipped
		w.probeAll(context.Background())
		w.clock = testWarmClock{now: time.Unix(1, 0).Add(time.Duration(i+1) * 45 * time.Second)}
	}
	down = false             // recovery from outside the warmer (a real dial)
	for i := 0; i < 4; i++ { // rounds 4-7 probe every round again
		w.probeAll(context.Background())
		w.clock = testWarmClock{now: time.Unix(1, 0).Add(time.Duration(i+4) * 45 * time.Second)}
	}
	s := w.Stats()
	if s.Probes != 6 || s.ProbeFailures != 2 {
		t.Fatalf("recovered node probed %d times (failures %d) in 7 rounds, want 6 and 2", s.Probes, s.ProbeFailures)
	}
}

// A successful probe also resets the schedule: the node keeps the base
// interval even while the Down hook still reports it down.
func TestProbeBackoffResetsOnProbeSuccess(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-back"}}
	w.down = func(string) bool { return true }
	calls := 0
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		calls++
		if calls <= 2 {
			return 0, net.ErrClosed
		}
		return 10, nil
	}
	for i := 0; i < 7; i++ {
		w.probeAll(context.Background())
		w.clock = testWarmClock{now: time.Unix(1, 0).Add(time.Duration(i+1) * 45 * time.Second)}
	}
	// Rounds 1 and 2 fail (next due 45 s, then 90 s later), round 3 is
	// skipped, round 4 probes and succeeds (reset), rounds 5-7 probe
	// every round again.
	if calls != 6 {
		t.Fatalf("probed %d times in 7 rounds, want 6", calls)
	}
	s := w.Stats()
	if s.Probes != 6 || s.ProbeFailures != 2 || !s.Nodes[0].Alive {
		t.Fatalf("stats: probed=%d failures=%d alive=%v", s.Probes, s.ProbeFailures, s.Nodes[0].Alive)
	}
}

func TestOnProbeReportsEveryResult(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{
		testWarmProxy{name: "netivri-ok", typ: C.Socks5},
		testWarmProxy{name: "netivri-bad", typ: C.Socks5},
	}
	type call struct {
		name  string
		alive bool
		delay time.Duration
	}
	results := make(chan call, 2)
	w.probe = func(_ context.Context, p C.Proxy, _ string) (uint16, error) {
		if p.Name() == "netivri-ok" {
			return 12, nil
		}
		return 0, net.ErrClosed
	}
	w.options.OnProbe = func(name string, alive bool, delay time.Duration) {
		results <- call{name: name, alive: alive, delay: delay}
	}
	w.probeAll(context.Background())
	close(results)
	seen := map[string]call{}
	for c := range results {
		seen[c.name] = c
	}
	if len(seen) != 2 {
		t.Fatalf("OnProbe called for %d of 2 nodes", len(seen))
	}
	if ok := seen["netivri-ok"]; !ok.alive || ok.delay != 12*time.Millisecond {
		t.Fatalf("OnProbe for ok node: %#v", ok)
	}
	if bad := seen["netivri-bad"]; bad.alive || bad.delay != 0 {
		t.Fatalf("OnProbe for failed node: %#v", bad)
	}
}

// ProbeConcurrency bounds the probes of one round: with a limit of 1
// the second probe cannot start before the first is released.
func TestProbeAllRespectsProbeConcurrency(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/", ProbeConcurrency: 1}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{
		testWarmProxy{name: "netivri-c1", typ: C.Socks5},
		testWarmProxy{name: "netivri-c2", typ: C.Socks5},
	}
	var inFlight, maxInFlight int32
	started := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	release := make(chan struct{})
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&inFlight, -1)
		finished <- struct{}{}
		return 5, nil
	}
	go w.probeAll(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first probe never started")
	}
	// With a limit of 1 the second probe must not enter.
	select {
	case <-started:
		close(release)
		t.Fatal("second probe ran despite ProbeConcurrency=1")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("a probe never finished")
		}
	}
	if got := atomic.LoadInt32(&maxInFlight); got != 1 {
		t.Fatalf("max concurrent probes = %d, want 1", got)
	}
}

// A shutdown during a round neither schedules the remaining probes nor
// records the interrupted one as a node failure.
func TestProbeAllCancellationRecordsNothing(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeTimeout: time.Minute, ProbeURL: "http://probe.invalid/", ProbeConcurrency: 1}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.options.OnProbe = func(string, bool, time.Duration) {
		t.Error("OnProbe called for a probe interrupted by shutdown")
	}
	w.proxies = []C.Proxy{
		testWarmProxy{name: "netivri-k1", typ: C.Socks5},
		testWarmProxy{name: "netivri-k2", typ: C.Socks5},
	}
	started := make(chan struct{}, 2)
	w.probe = func(ctx context.Context, _ C.Proxy, _ string) (uint16, error) {
		started <- struct{}{}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.probeAll(ctx)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("first probe never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("probeAll did not return after cancellation")
	}
	if len(started) != 0 {
		t.Fatal("a second probe was scheduled after cancellation")
	}
	s := w.Stats()
	if s.Probes != 0 || s.ProbeFailures != 0 || len(s.Nodes) != 0 {
		t.Fatalf("interrupted probe was recorded: probes=%d failures=%d nodes=%d", s.Probes, s.ProbeFailures, len(s.Nodes))
	}
}

type runWarmClock struct {
	testWarmClock
	ticker *runWarmTicker
}

func (c runWarmClock) NewTicker(time.Duration) warmTicker { return c.ticker }

type runWarmTicker struct{ ticks chan time.Time }

func (t *runWarmTicker) Chan() <-chan time.Time { return t.ticks }
func (*runWarmTicker) Stop()                    {}

type testWarmClock struct{ now time.Time }

func (c testWarmClock) Now() time.Time                   { return c.now }
func (testWarmClock) NewTicker(time.Duration) warmTicker { return nil }

type testWarmProxy struct {
	name string
	typ  C.AdapterType
}

func (p testWarmProxy) Adapter() C.ProxyAdapter                                { return nil }
func (p testWarmProxy) Name() string                                           { return p.name }
func (p testWarmProxy) Type() C.AdapterType                                    { return p.typ }
func (testWarmProxy) Addr() string                                             { return "" }
func (testWarmProxy) SupportUDP() bool                                         { return false }
func (testWarmProxy) ProxyInfo() C.ProxyInfo                                   { return C.ProxyInfo{} }
func (testWarmProxy) MarshalJSON() ([]byte, error)                             { return nil, nil }
func (testWarmProxy) DialContext(context.Context, *C.Metadata) (C.Conn, error) { return nil, nil }
func (testWarmProxy) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	return nil, nil
}
func (testWarmProxy) SupportUOT() bool                             { return false }
func (testWarmProxy) IsL3Protocol(*C.Metadata) bool                { return false }
func (testWarmProxy) Unwrap(*C.Metadata, bool) C.Proxy             { return nil }
func (testWarmProxy) Close() error                                 { return nil }
func (testWarmProxy) AliveForTestUrl(string) bool                  { return false }
func (testWarmProxy) DelayHistory() []C.DelayHistory               { return nil }
func (testWarmProxy) ExtraDelayHistories() map[string]C.ProxyState { return nil }
func (testWarmProxy) LastDelayForTestUrl(string) uint16            { return 0 }
func (testWarmProxy) URLTest(context.Context, string, utils.IntRanges[uint16]) (uint16, error) {
	return 0, nil
}
