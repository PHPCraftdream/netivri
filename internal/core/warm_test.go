package core

import (
	"context"
	"net"
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
	for _, invalid := range []WarmOptions{{ProbeInterval: -1}, {ProbeTimeout: -1}, {PoolTTL: -1}, {PoolSize: -1}, {ProbeURL: "file:///tmp"}} {
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
