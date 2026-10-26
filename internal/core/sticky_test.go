package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/buf"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

// fakeConn is a minimal C.Conn for the fake members.
type fakeConn struct{ chains C.Chain }

func (c *fakeConn) Read(b []byte) (int, error)       { return 0, io.EOF }
func (c *fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeConn) ReadBuffer(*buf.Buffer) error     { return io.EOF }
func (c *fakeConn) WriteBuffer(*buf.Buffer) error    { return nil }
func (c *fakeConn) Chains() C.Chain                  { return c.chains }
func (c *fakeConn) ProviderChains() C.Chain          { return nil }
func (c *fakeConn) RemoteDestination() string        { return "" }
func (c *fakeConn) AppendToChains(a C.ProxyAdapter)  { c.chains = append(c.chains, a.Name()) }

var _ C.Conn = (*fakeConn)(nil)

type fakeAddr struct{}

func (fakeAddr) Network() string { return "tcp" }
func (fakeAddr) String() string  { return "127.0.0.1:0" }

// fakeMember is a test double for a leaf proxy.
type fakeMember struct {
	name  string
	udp   bool
	err   error
	dials atomic.Int32
}

func (f *fakeMember) Name() string { return f.name }

func (f *fakeMember) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	f.dials.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &fakeConn{}, nil
}

func (f *fakeMember) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return nil, errors.New("fakeMember: packet connections not supported")
}

func (f *fakeMember) SupportUDP() bool              { return f.udp }
func (f *fakeMember) IsL3Protocol(*C.Metadata) bool { return false }

type dialSample struct {
	label string
	ok    bool
	took  time.Duration
}

type probeSample struct {
	label string
	alive bool
	delay time.Duration
}

type throughputSample struct {
	label  string
	bytes  int64
	window time.Duration
}

// recordedObserver collects full observer calls for assertions.
type recordedObserver struct {
	mu             sync.Mutex
	probes         []probeSample
	dials          []dialSample
	throughputs    []throughputSample
	accountDials   []accountDialSample
	accountTraffic []accountTrafficSample
}

type accountDialSample struct {
	login string
	label string
	ok    bool
	took  time.Duration
}

type accountTrafficSample struct {
	login  string
	label  string
	up     int64
	down   int64
	window time.Duration
}

func (o *recordedObserver) ObserveProbe(label string, alive bool, delay time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.probes = append(o.probes, probeSample{label: label, alive: alive, delay: delay})
}

func (o *recordedObserver) ObserveDial(label string, ok bool, took time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dials = append(o.dials, dialSample{label: label, ok: ok, took: took})
}

func (o *recordedObserver) ObserveThroughput(label string, bytes int64, window time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.throughputs = append(o.throughputs, throughputSample{label: label, bytes: bytes, window: window})
}

func (o *recordedObserver) ObserveAccountDial(login, label string, ok bool, took time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accountDials = append(o.accountDials, accountDialSample{login: login, label: label, ok: ok, took: took})
}

func (o *recordedObserver) ObserveAccountThroughput(login, label string, up, down int64, window time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accountTraffic = append(o.accountTraffic, accountTrafficSample{login: login, label: label, up: up, down: down, window: window})
}

var _ AccountObserver = (*recordedObserver)(nil)

func (o *recordedObserver) dialSamples() []dialSample {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]dialSample(nil), o.dials...)
}

func (o *recordedObserver) throughputSamples() []throughputSample {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]throughputSample(nil), o.throughputs...)
}

func (o *recordedObserver) accountDialSamples() []accountDialSample {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]accountDialSample(nil), o.accountDials...)
}

func (o *recordedObserver) accountTrafficSamples() []accountTrafficSample {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]accountTrafficSample(nil), o.accountTraffic...)
}

func newTestGroup(t *testing.T, members []*fakeMember, observer Observer) (*stickyGroup, *affinityTable, *healthTable) {
	t.Helper()
	dialMembers := make([]member, len(members))
	labels := make([]string, len(members))
	for i, m := range members {
		dialMembers[i] = m
		labels[i] = m.name
	}
	table := newAffinityTable(nil, 0)
	health := newHealthTable()
	return newStickyGroupMembers("netivri-group-0", dialMembers, labels, table, health, observer), table, health
}

func dialMetadata(login, host string) *C.Metadata {
	return &C.Metadata{InUser: login, Host: host, DstPort: 443}
}

func TestStickyGroupDialSuccess(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	obs := &recordedObserver{}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, obs)

	// Seed delays so the choice is deterministic: b is best.
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	md := dialMetadata("alice", "www.example.com")
	conn, err := g.DialContext(context.Background(), md)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	if b.dials.Load() != 1 || a.dials.Load() != 0 {
		t.Fatalf("dials: a=%d b=%d, want b dialed", a.dials.Load(), b.dials.Load())
	}
	if conn == nil {
		t.Fatal("no connection returned")
	}
	dials := obs.dialSamples()
	if len(dials) != 1 || dials[0].label != "b" || !dials[0].ok || dials[0].took < 0 {
		t.Fatalf("observer dials = %+v, want one ok call for b", dials)
	}
	// The same site uses the same binding again.
	if _, err := g.DialContext(context.Background(), md); err != nil {
		t.Fatalf("second DialContext: %v", err)
	}
	if b.dials.Load() != 2 {
		t.Fatalf("binding moved: b dials = %d, want 2", b.dials.Load())
	}
	if table.count() != 1 {
		t.Fatalf("bindings = %d, want 1", table.count())
	}
	if table.rebindCount() != 0 {
		t.Fatalf("rebinds = %d, want 0", table.rebindCount())
	}
}

func TestStickyGroupDialFailureWithoutDown(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b", err: errors.New("refused")}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, nil)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	wantErr := errors.New("upstream refused")
	b.err = wantErr
	_, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want the member error unchanged", err)
	}
	if a.dials.Load() != 0 {
		t.Fatalf("a was dialed without a rebind: %d", a.dials.Load())
	}
}

func TestStickyGroupRetryOnceAfterDown(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	obs := &recordedObserver{}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, obs)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	// b is the best node: bind the site to it before it starts failing.
	if index, _ := g.choose("alice", "example.com"); index != 1 {
		t.Fatalf("site bound to member %d, want b (1)", index)
	}

	// Two prior dial failures: this failing dial is the third consecutive
	// one, which makes b down and triggers the single retry on a.
	health.recordDial("b", false, 0)
	health.recordDial("b", false, 0)
	b.err = errors.New("refused")

	conn, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com"))
	if err != nil {
		t.Fatalf("retry dial failed: %v", err)
	}
	if a.dials.Load() != 1 {
		t.Fatalf("a dials = %d, want 1 (one retry)", a.dials.Load())
	}
	if b.dials.Load() != 1 {
		t.Fatalf("b dials = %d, want 1", b.dials.Load())
	}
	if table.rebindCount() != 1 {
		t.Fatalf("rebinds = %d, want 1", table.rebindCount())
	}
	dials := obs.dialSamples()
	if len(dials) != 2 || dials[0].label != "b" || dials[0].ok || dials[1].label != "a" || !dials[1].ok {
		t.Fatalf("observer dials = %+v, want failed b then ok a", dials)
	}
	_ = conn
}

func TestStickyGroupRetryExhaustedReturnsError(t *testing.T) {
	a := &fakeMember{name: "a", err: errors.New("a down")}
	b := &fakeMember{name: "b"}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, nil)

	// b has the best delay, but its failing dial is its third consecutive
	// failure; the only remaining member a fails too, so the retry error is
	// returned.
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	if index, _ := g.choose("alice", "example.com"); index != 1 {
		t.Fatalf("site bound to member %d, want b (1)", index)
	}
	health.recordDial("b", false, 0)
	health.recordDial("b", false, 0)
	b.err = errors.New("b refused")

	_, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com"))
	if err == nil {
		t.Fatal("the second failure must be returned")
	}
	if err.Error() != "a down" {
		t.Fatalf("error = %v, want the retry member's error", err)
	}
}

func TestStickyGroupHealthyBindingNeverReplaced(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, nil)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	md := dialMetadata("alice", "www.example.com")

	if _, err := g.DialContext(context.Background(), md); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	// a became much faster: the healthy binding still stays.
	health.recordProbe("a", true, 5*time.Millisecond)
	if _, err := g.DialContext(context.Background(), md); err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if a.dials.Load() != 0 {
		t.Fatalf("the binding moved to the faster node: a dials = %d", a.dials.Load())
	}
	if b.dials.Load() != 2 {
		t.Fatalf("b dials = %d, want 2", b.dials.Load())
	}
}

func TestStickyGroupIndependentAccountsAndSites(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	for _, login := range []string{"alice", "bob"} {
		for _, host := range []string{"www.example.com", "sub.www.example.com"} {
			if _, err := g.DialContext(context.Background(), dialMetadata(login, host)); err != nil {
				t.Fatalf("%s %s: %v", login, host, err)
			}
		}
	}
	// One binding per account: the subdomain maps to the same site.
	if table.count() != 2 {
		t.Fatalf("bindings = %d, want 2", table.count())
	}
	if _, ok := table.peek(affinityKey{login: "alice", site: "example.com"}); !ok {
		t.Fatal("binding missing for alice")
	}
	if _, ok := table.peek(affinityKey{login: "bob", site: "example.com"}); !ok {
		t.Fatal("binding missing for bob")
	}
}

func TestStickyGroupUnwrapWithoutRealProxies(t *testing.T) {
	a := &fakeMember{name: "a"}
	g, _, _ := newTestGroup(t, []*fakeMember{a}, nil)
	if g.Unwrap(nil, false) != nil {
		t.Fatal("Unwrap without real member proxies must return nil")
	}
}

func TestStickyGroupSupportUDPAndMarshal(t *testing.T) {
	a := &fakeMember{name: "a", udp: true}
	b := &fakeMember{name: "b"}
	g, _, _ := newTestGroup(t, []*fakeMember{a, b}, nil)
	if !g.SupportUDP() {
		t.Fatal("SupportUDP = false, want the members' support")
	}
	data, err := g.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	// The JSON must identify the group type without leaking anything else.
	for _, want := range []string{`"type":"Selector"`, `"now":"a"`, `"all":["a","b"]`} {
		if !containsString(string(data), want) {
			t.Fatalf("MarshalJSON = %s, missing %s", data, want)
		}
	}
}

func TestStickyGroupListenPacketChoosesBound(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	md := dialMetadata("alice", "")
	md.NetWork = C.UDP
	md.DstIP = netip.MustParseAddr("10.0.0.1")
	if _, err := g.ListenPacketContext(context.Background(), md); err == nil {
		t.Fatal("fake members do not support packet connections")
	}
	// The choice used the binding of (login, destination IP).
	if member, ok := table.peek(affinityKey{login: "alice", site: "10.0.0.1"}); !ok || member != 1 {
		t.Fatalf("binding for the UDP destination IP = %d, %v, want 1", member, ok)
	}
}

// A binding hit must allocate nothing: no health views, no closures on the
// hot path.
func TestStickyGroupChooseNoAllocOnHit(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	const login, site = "alice", "example.com" // the site key is computed outside the measured call
	if _, rebound := g.choose(login, site); rebound {
		t.Fatal("the first choose must not rebind")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = g.choose(login, site) }); allocs != 0 {
		t.Fatalf("choose on a healthy binding allocates %v times, want 0", allocs)
	}
}

// Unwrap and IsL3Protocol are display/non-dial paths: they must never create
// a binding.
func TestStickyGroupPeekDoesNotCreateBindings(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	// Slots for the real proxies make Unwrap take its peek path; the entries
	// themselves are never read (nil C.Proxy values).
	g.proxies = make([]C.Proxy, 2)

	md := dialMetadata("alice", "www.example.com")
	if g.IsL3Protocol(md) {
		t.Fatal("fake members are not L3")
	}
	_ = g.Unwrap(md, false)
	if table.count() != 0 {
		t.Fatalf("display paths created a binding before any dial: %d, want 0", table.count())
	}
	if _, err := g.DialContext(context.Background(), md); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if table.count() != 1 {
		t.Fatalf("bindings = %d, want 1", table.count())
	}
	_ = g.IsL3Protocol(md)
	_ = g.Unwrap(md, false)
	if table.count() != 1 {
		t.Fatalf("display paths created bindings: %d, want 1", table.count())
	}
}

// Concurrent first choices of one new key must all land on the same member:
// acquire serialises the pick per shard, so only one binding is installed.
func TestStickyGroupConcurrentChooseSameMember(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	const workers = 8
	start := make(chan struct{})
	results := make(chan int, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			member, _ := g.choose("alice", "example.com")
			results <- member
		}()
	}
	close(start)
	first := <-results
	for i := 1; i < workers; i++ {
		if member := <-results; member != first {
			t.Fatalf("concurrent choose returned different members: %d and %d", first, member)
		}
	}
	if first != 1 {
		t.Fatalf("member = %d, want the best node 1", first)
	}
	if table.rebindCount() != 0 {
		t.Fatalf("rebinds = %d, want 0", table.rebindCount())
	}
	if table.count() != 1 {
		t.Fatalf("bindings = %d, want 1", table.count())
	}
}

// Concurrent acquires on a key whose node went down must count exactly one
// rebind: the shard lock serialises them and the first caller replaces the
// binding.
func TestStickyGroupConcurrentRebindOnce(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)
	if member, rebound := g.choose("alice", "example.com"); member != 1 || rebound {
		t.Fatalf("choose = %d, %v, want the best node 1", member, rebound)
	}
	health.recordDial("b", false, 0)
	health.recordDial("b", false, 0)
	health.recordDial("b", false, 0)

	const workers = 8
	start := make(chan struct{})
	results := make(chan int, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			member, _ := g.choose("alice", "example.com")
			results <- member
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if member := <-results; member != 0 {
			t.Fatalf("choose returned member %d, want the healthy member 0", member)
		}
	}
	if got := table.rebindCount(); got != 1 {
		t.Fatalf("rebinds = %d, want exactly 1", got)
	}
}

// AffinityStats through a minimal Instance.
func TestInstanceAffinityStats(t *testing.T) {
	empty := &Instance{}
	if got := empty.AffinityStats(); got != (AffinityStats{}) {
		t.Fatalf("AffinityStats without a table = %+v, want the zero value", got)
	}
	table := newAffinityTable(nil, 0)
	table.acquire(
		affinityKey{login: "alice", site: "example.com"},
		func(int) bool { return false },
		func() (int, bool) { return 0, false },
	)
	table.rebinds.Add(3)
	inst := instanceWith(&routingState{table: table})
	got := inst.AffinityStats()
	if got.Bindings != 1 || got.Rebinds != 3 {
		t.Fatalf("AffinityStats = %+v, want {1 3}", got)
	}
}

func TestThroughputPollerCollectsAndPrunes(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a", "netivri-r0-1": "b"}, nil)

	base := time.Unix(0, 0)
	before := base.Add(-time.Minute)
	// Existing before the first tick: baselined without counting.
	trackerA := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0", "netivri-group-0"}, Start: before}}
	trackerB := &fakeTracker{id: "t2", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-1", "netivri-group-0"}, Start: before}}
	trackerOther := &fakeTracker{id: "t3", info: &statistic.TrackerInfo{Chain: C.Chain{"DIRECT"}, Start: before}}
	source := &fakeTrackerSource{trackers: []throughputTracker{trackerA, trackerB, trackerOther}}
	poller.source = source

	trackerA.info.UploadTotal.Add(1 << 20)
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 0 {
		t.Fatalf("the baselining read reported movement: %+v", obs.throughputSamples())
	}

	// Second read: 2 MiB moved on a, nothing on b; the window is the real
	// elapsed time.
	trackerA.info.UploadTotal.Add(2 << 20)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)
	samples := obs.throughputSamples()
	if len(samples) != 1 || samples[0].label != "a" || samples[0].bytes != 2<<20 || samples[0].window != 5*time.Second {
		t.Fatalf("throughput samples = %+v, want one {a, 2 MiB, 5s}", samples)
	}
	if health.entries["a"].throughput <= 0 {
		t.Fatalf("throughput EWMA not fed: %v", health.entries["a"].throughput)
	}

	// The tracker of a disappears: its id is pruned, no leak, no sample.
	trackerA.info.UploadTotal.Add(2 << 20)
	source.trackers = []throughputTracker{trackerB, trackerOther}
	poller.collectAt(base.Add(10*time.Second), base.Add(15*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 1 {
		t.Fatalf("throughput samples after prune = %+v, want no new samples", obs.throughputSamples())
	}
	// Only b is left: the DIRECT tracker is never tracked (no member in its
	// chain) and a's id was pruned.
	if len(poller.last) != 1 {
		t.Fatalf("tracker ids kept = %d, want 1", len(poller.last))
	}
	if _, ok := poller.last["t2"]; !ok {
		t.Fatalf("the tracker of b was pruned: %+v", poller.last)
	}
}

// A tracker first seen at this tick whose connection started after the
// previous tick counts its whole current totals: its first window would
// otherwise never be counted.
func TestThroughputPollerCountsFirstWindowOfNewTracker(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	base := time.Unix(0, 0)
	tracker := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0"}, Start: base.Add(time.Second)}}
	tracker.info.UploadTotal.Add(2 << 20)
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{tracker}}
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)

	samples := obs.throughputSamples()
	if len(samples) != 1 || samples[0].label != "a" || samples[0].bytes != 2<<20 || samples[0].window != 5*time.Second {
		t.Fatalf("throughput samples = %+v, want one {a, 2 MiB, 5s}", samples)
	}
}

// A tracker that already existed before the poller's first tick is only
// baselined, even though it already moved bytes.
func TestThroughputPollerBaselinesPreExistingTrackers(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	base := time.Unix(0, 0)
	tracker := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0"}, Start: base.Add(-time.Hour)}}
	tracker.info.DownloadTotal.Add(2 << 20)
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{tracker}}
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 0 {
		t.Fatalf("pre-existing tracker was counted: %+v", obs.throughputSamples())
	}
}

func TestThroughputPollerIgnoresSmallWindows(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	tracker := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0"}, Start: time.Unix(0, 0)}}
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{tracker}}
	base := time.Unix(0, 0)
	tracker.info.DownloadTotal.Add(8 << 10) // below 16 KiB
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	tracker.info.DownloadTotal.Add(8 << 10)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 0 {
		t.Fatalf("small windows were reported: %+v", obs.throughputSamples())
	}
}

func TestPollerStopIsIdempotentAndNilSafe(t *testing.T) {
	var nilPoller *throughputPoller
	nilPoller.stop()

	health := newHealthTable()
	poller := newThroughputPoller(&fakeTrackerSource{}, health, nil, map[string]string{}, realStickyClock{})
	poller.start()
	poller.stop()
	poller.stop() // idempotent
	select {
	case <-poller.done:
	default:
		t.Fatal("stop did not wait for the poller goroutine")
	}
}

func TestStickyGroupConcurrentUse(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	obs := &recordedObserver{}
	g, table, health := newTestGroup(t, []*fakeMember{a, b}, obs)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			login := "user" + string(rune('0'+worker))
			for i := 0; i < 50; i++ {
				md := dialMetadata(login, "site"+string(rune('a'+i%3))+".example")
				if _, err := g.DialContext(context.Background(), md); err != nil {
					t.Errorf("dial: %v", err)
					return
				}
				health.recordProbe("a", true, 50*time.Millisecond)
				health.recordProbe("b", false, 0)
				table.count()
				table.rebindCount()
			}
		}(worker)
	}
	wg.Wait()
}

// fakeTracker is a minimal statistic tracker for the poller tests.
type fakeTracker struct {
	id   string
	info *statistic.TrackerInfo
}

func (t *fakeTracker) ID() string                   { return t.id }
func (t *fakeTracker) Info() *statistic.TrackerInfo { return t.info }

type fakeTrackerSource struct {
	trackers []throughputTracker
}

func (s *fakeTrackerSource) Range(f func(tracker throughputTracker) bool) {
	for _, tracker := range s.trackers {
		if !f(tracker) {
			return
		}
	}
}
