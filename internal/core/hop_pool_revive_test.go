package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

type reviveTestProxy struct {
	testWarmProxy
	dial func(context.Context, *C.Metadata) (C.Conn, error)
}

func (p *reviveTestProxy) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	return p.dial(ctx, metadata)
}

type reviveTestTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
}

func (t *reviveTestTicker) Chan() <-chan time.Time { return t.ticks }
func (t *reviveTestTicker) Stop()                  { close(t.stopped) }

type reviveTestWarmClock struct {
	now    time.Time
	ticker *reviveTestTicker
}

func (c reviveTestWarmClock) Now() time.Time                     { return c.now }
func (c reviveTestWarmClock) NewTicker(time.Duration) warmTicker { return c.ticker }

func TestHopLifecycleReviverRoundUsesPoolProberForRecoveryAndRetrip(t *testing.T) {
	const route = "revive-route"
	uriA, uriB := "socks5://a.invalid:1080", "socks5://b.invalid:1080"
	o := Options{
		AccountRoute: map[string]string{"alice": route},
		RouteNodes:   map[string][]map[string]any{route: {{"type": "socks5", "server": "node.invalid", "port": 443}}},
		NodePrePool:  map[string][][]string{route: {{uriA, uriB}}},
	}
	plans, err := buildRoutePlansGen(o, 0)
	if err != nil {
		t.Fatal(err)
	}
	mappings, _, _, err := prepareRoutesGen(o, 0)
	if err != nil {
		t.Fatal(err)
	}
	proxies := make(map[string]C.Proxy, len(mappings))
	var recoveryProxy *reviveTestProxy
	for _, mapping := range mappings {
		name := mapping["name"].(string)
		proxy := &reviveTestProxy{testWarmProxy: testWarmProxy{name: name, typ: C.Socks5}}
		proxy.dial = func(context.Context, *C.Metadata) (C.Conn, error) { return &fakeConn{}, nil }
		proxies[name] = proxy
		if name == newHopPoolSpec(preHopPool, []string{uriA, uriB}, 0).entryNames[0] {
			recoveryProxy = proxy
		}
	}
	if recoveryProxy == nil {
		t.Fatal("raw pre-hop mapping did not provide the recovery proxy")
	}
	health := newHealthTable()
	health.registerMembers(healthMembersForPlans(plans))
	now := time.Unix(2000, 0)
	m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	if _, _, err := m.installGen(o, plans, proxies, health, 0); err != nil {
		t.Fatal(err)
	}
	logical := hopPoolLogicalKey{role: preHopPool, route: route, node: 0}
	use := m.uses[logical]
	if use == nil {
		t.Fatal("pre-hop pool use was not installed")
	}
	p := use.pool
	key := hopPoolKey(uriA)
	p.Restore([]hoppool.Entry{{Key: key, Trips: 1}}, now)
	reviver, err := m.newReviver(WarmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reviver.Round(context.Background()); err != nil {
		t.Fatalf("successful recovery round: %v", err)
	}
	if got := poolState(p, key); got.State != hoppool.Healthy || got.Trips != 0 {
		t.Fatalf("successful lifecycle probe did not revive the hop: %#v", got)
	}
	ordered := p.Order(now)
	found := false
	for _, orderedKey := range ordered {
		if orderedKey == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("recovered hop %q missing from pool order: %v", key, ordered)
	}

	p.Restore([]hoppool.Entry{{Key: key, Trips: 1}}, now)
	recoveryProxy.dial = func(context.Context, *C.Metadata) (C.Conn, error) {
		return nil, errors.New("a.invalid:1080 connect error: connection refused")
	}
	if err := reviver.Round(context.Background()); err != nil {
		t.Fatalf("hard-failure recovery round: %v", err)
	}
	got := poolState(p, key)
	if got.State != hoppool.Quarantined || got.Trips != 2 {
		t.Fatalf("failed lifecycle probe did not double the trip: %#v", got)
	}
	if !got.NextProbe.Equal(now.Add(60 * time.Second)) {
		t.Fatalf("second-trip backoff deadline = %s, want %s", got.NextProbe, now.Add(60*time.Second))
	}
}

func TestWarmerCloseWaitsForLifecycleRevivalRound(t *testing.T) {
	const route = "shutdown-route"
	uriA, uriB := "socks5://shutdown-a.invalid:1080", "socks5://shutdown-b.invalid:1080"
	o := Options{
		AccountRoute: map[string]string{"alice": route},
		RouteNodes:   map[string][]map[string]any{route: {{"type": "socks5", "server": "shutdown-node.invalid", "port": 443}}},
		NodePrePool:  map[string][][]string{route: {{uriA, uriB}}},
	}
	plans, err := buildRoutePlansGen(o, 0)
	if err != nil {
		t.Fatal(err)
	}
	mappings, _, _, err := prepareRoutesGen(o, 0)
	if err != nil {
		t.Fatal(err)
	}
	entered, exited := make(chan struct{}), make(chan struct{})
	var active atomic.Int32
	proxies := make(map[string]C.Proxy, len(mappings))
	var blockingProxy *reviveTestProxy
	for _, mapping := range mappings {
		name := mapping["name"].(string)
		proxy := &reviveTestProxy{testWarmProxy: testWarmProxy{name: name, typ: C.Socks5}}
		proxy.dial = func(context.Context, *C.Metadata) (C.Conn, error) { return &fakeConn{}, nil }
		proxies[name] = proxy
		if name == newHopPoolSpec(preHopPool, []string{uriA, uriB}, 0).entryNames[0] {
			blockingProxy = proxy
		}
	}
	if blockingProxy == nil {
		t.Fatal("raw pre-hop mapping did not provide the blocking proxy")
	}
	health := newHealthTable()
	health.registerMembers(healthMembersForPlans(plans))
	var now = time.Unix(2000, 0)
	m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	if _, _, err := m.installGen(o, plans, proxies, health, 0); err != nil {
		t.Fatal(err)
	}
	use := m.uses[hopPoolLogicalKey{role: preHopPool, route: route, node: 0}]
	if use == nil {
		t.Fatal("pre-hop pool use was not installed")
	}
	use.pool.Restore([]hoppool.Entry{{Key: hopPoolKey(uriA), Trips: 1}}, now)
	reviver, err := m.newReviver(WarmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	blockingProxy.dial = func(ctx context.Context, _ *C.Metadata) (C.Conn, error) {
		active.Add(1)
		defer active.Add(-1)
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ticker := &reviveTestTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
	w := &warmer{
		cancel: cancel, done: make(chan struct{}), kick: make(chan struct{}, 1),
		clock:   reviveTestWarmClock{now: now, ticker: ticker},
		options: WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, hopRevive: reviver.Round},
	}
	t.Cleanup(w.Close)
	go w.run(ctx)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		w.Close()
		t.Fatal("warmer did not start its lifecycle revival round")
	}
	w.Close()
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before lifecycle probe exited")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("Close returned before warmer run exited")
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("warmer run did not stop its ticker")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active lifecycle probe count after Close = %d", got)
	}
}
