package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/proxyclient"
	C "github.com/metacubex/mihomo/constant"
)

func TestPoolPrecedenceAndOpaquePoolKeys(t *testing.T) {
	o := Options{
		RoutePre:     map[string]string{"r": "socks5://legacy.invalid:1080"},
		RoutePrePool: map[string][]string{"r": {"socks5://route.invalid:1080", "none"}},
		NodePrePool:  map[string][][]string{"r": {nil}},
	}
	if got := o.nodePrePool("r", 0); len(got) != 0 {
		t.Fatal("node-level empty pool did not override route pool")
	}
	delete(o.NodePrePool, "r")
	if got := o.nodePrePool("r", 0); len(got) != 1 || got[0] != "socks5://route.invalid:1080" {
		t.Fatal("route pool did not override scalar")
	}
	a := hopPoolKey("socks5://first:fake@socks.invalid:1080")
	b := hopPoolKey("socks5://second:fake@socks.invalid:1080")
	if a == "" || a != b || len(a) != 64 {
		t.Fatal("credentials affected opaque hop identity")
	}
	zoneA := hopPoolKey("socks5://u:p@[2001:db8::1%25zone]:1080")
	zoneB := hopPoolKey("socks5://changed:credentials@[2001:db8::1%25zone]:1080")
	if !strings.HasPrefix(zoneA, "u:") || zoneA != zoneB {
		t.Fatal("valid zoned IPv6 identity did not use credential-independent fallback")
	}
}

func TestNativeStageMappingDoesNotAttributeUnknownOrCapacityFailures(t *testing.T) {
	cases := []struct {
		name       string
		stage      proxyclient.Stage
		targetSent bool
		capacity   error
		want       hopdial.Stage
		wantTarget bool
		wantSent   bool
	}{
		{"connect", proxyclient.StageConnect, false, nil, hopdial.Connect, false, false},
		{"handshake-before-target", proxyclient.StageHandshake, false, nil, hopdial.Handshake, false, false},
		{"handshake-after-target", proxyclient.StageHandshake, true, nil, hopdial.Handshake, false, true},
		{"refusal", proxyclient.StageRefused, true, nil, hopdial.Refusal, false, true},
		{"target", proxyclient.StageTarget, true, nil, hopdial.Target, true, true},
		{"other", proxyclient.StageOther, true, nil, hopdial.Unknown, false, true},
		{"capacity-through-wrapper", proxyclient.StageConnect, false, errors.New("too many open files"), hopdial.Unknown, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("native proxy failure")
			if tc.capacity != nil {
				cause = fmt.Errorf("wrapped proxy error: %w", tc.capacity)
			}
			err := stageProxyError(context.Background(), tc.stage, cause, tc.targetSent)
			var staged *hopdial.StageError
			if !errors.As(err, &staged) || staged.Stage != tc.want || staged.NextUnreachable != tc.wantTarget || staged.TargetSent != tc.wantSent {
				t.Fatalf("mapped failure did not preserve its typed stage and target-sent evidence")
			}
		})
	}
	if err := stageProxyError(context.Background(), proxyclient.StageConnect, context.Canceled, false); func() bool {
		var staged *hopdial.StageError
		return !errors.As(err, &staged) || staged.Stage != hopdial.Unknown
	}() {
		t.Fatal("cancellation was classified as a hop failure")
	}
}

func TestPrePoolUsesNativeFailoverAndTypedHopFailure(t *testing.T) {
	first := newNativeLoopbackProxy(t, "socks5")
	second := newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(preHopPool, []string{first.uri, second.uri}, 0)
	now := time.Unix(100, 0)
	seed := [2]uint64{3, 4}
	chooser := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	order := chooser.Order(now)
	if len(order) != 2 {
		t.Fatalf("pool order has %d candidates, want two", len(order))
	}
	firstIndex := 0
	if spec.keys[1] == order[0] {
		firstIndex = 1
	}
	servers := []*nativeLoopbackProxy{first, second}
	servers[firstIndex].mode.Store(nativeProxyBadGreeting)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	a := newHopPoolAdapter("pre", preHopPool, spec, p, nil, nil, func() time.Time { return now })
	var mutations int
	a.mutate = func(_ string, change func()) { mutations++; change() }
	conn, err := a.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err != nil || conn == nil {
		t.Fatalf("native pre failover failed: %v", err)
	}
	if len(conn.Chains()) != 1 || conn.Chains()[0] != "pre" {
		t.Fatalf("connection chain = %v", conn.Chains())
	}
	_ = conn.Close()
	if mutations != 2 {
		t.Fatalf("health mutations=%d, want one handshake failure and one success", mutations)
	}
	if servers[0].requests.Load() != 1 || servers[1].requests.Load() != 1 {
		t.Fatalf("native proxy attempts = %d/%d, want one each", servers[0].requests.Load(), servers[1].requests.Load())
	}

	broken := newNativeLoopbackProxy(t, "socks5")
	other := newNativeLoopbackProxy(t, "socks5")
	broken.mode.Store(nativeProxyBadGreeting)
	other.mode.Store(nativeProxyBadGreeting)
	unknownSpec := newHopPoolSpec(preHopPool, []string{broken.uri, other.uri}, 0)
	unknown := newHopPoolAdapter("typed-failure", preHopPool, unknownSpec, pool.New(unknownSpec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(1, 2)}), nil, nil, func() time.Time { return now })
	_, err = unknown.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "bad greeting") {
		t.Fatalf("native handshake failure was not safely sanitized: %v", err)
	}
}

type testSiteBinder struct{ values map[string]string }

func (b *testSiteBinder) Get(login, site string) (string, bool) {
	value, ok := b.values[login+"\x00"+site]
	return value, ok
}
func (b *testSiteBinder) Bind(login, site, key string) { b.values[login+"\x00"+site] = key }
func (b *testSiteBinder) Unbind(login, site string)    { delete(b.values, login+"\x00"+site) }
func (b *testSiteBinder) Forget(key string) int {
	n := 0
	for entry, value := range b.values {
		if value == key {
			delete(b.values, entry)
			n++
		}
	}
	return n
}

func TestPostPoolBindsAndReusesTheNativeSiteChoice(t *testing.T) {
	firstProxy, secondProxy := newNativeLoopbackProxy(t, "http"), newNativeLoopbackProxy(t, "http")
	spec := newHopPoolSpec(postHopPool, []string{firstProxy.uri, secondProxy.uri}, 0)
	now := time.Unix(200, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(3, 4)})
	binder := &testSiteBinder{values: make(map[string]string)}
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	first := newHopPoolAdapter("post-1", postHopPool, spec, p, binder, carrier, func() time.Time { return now })
	setNativeCarrier(first, nil, true)
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443}
	conn, err := first.DialContext(context.Background(), metadata)
	if err != nil {
		t.Fatalf("native post pool dial: %v", err)
	}
	_ = conn.Close()
	bound, ok := binder.Get("alice", "example.test")
	if !ok {
		t.Fatal("successful post dial did not bind site")
	}
	servers := map[string]*nativeLoopbackProxy{hopPoolKey(firstProxy.uri): firstProxy, hopPoolKey(secondProxy.uri): secondProxy}
	beforeBound, beforeOther := servers[bound].requests.Load(), int32(0)
	other := spec.keys[0]
	if other == bound {
		other = spec.keys[1]
	}
	beforeOther = servers[other].requests.Load()
	second := newHopPoolAdapter("post-2", postHopPool, spec, p, binder, carrier, func() time.Time { return now })
	setNativeCarrier(second, nil, true)
	conn, err = second.DialContext(context.Background(), metadata)
	if err != nil {
		t.Fatalf("bound native post dial: %v", err)
	}
	_ = conn.Close()
	if got := servers[bound].requests.Load(); got != beforeBound+1 {
		t.Fatal("bound post hop was not reused across node adapters")
	}
	if got := servers[other].requests.Load(); got != beforeOther {
		t.Fatal("post binding changed to another hop")
	}
}

func TestPostHopQuarantineRebindsAcrossNodeAdapters(t *testing.T) {
	firstProxy, secondProxy := newNativeLoopbackProxy(t, "http"), newNativeLoopbackProxy(t, "http")
	spec := newHopPoolSpec(postHopPool, []string{"http://first-user:private-secret@" + firstProxy.listener.Addr().String(), "http://second-user:other-secret@" + secondProxy.listener.Addr().String()}, 0)
	now := time.Unix(400, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(7, 11)})
	for range 3 {
		p.RecordHard(spec.keys[0])
	}
	binder := &testSiteBinder{values: map[string]string{"alice\x00example.test": spec.keys[0]}}
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	first := newHopPoolAdapter("post-node-one", postHopPool, spec, p, binder, carrier, func() time.Time { return now })
	setNativeCarrier(first, nil, true)
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443}
	conn, err := first.DialContext(context.Background(), metadata)
	if err != nil {
		t.Fatalf("healthy native replacement dial failed: %v", err)
	}
	_ = conn.Close()
	bound, ok := binder.Get("alice", "example.test")
	if !ok || bound != spec.keys[1] {
		t.Fatal("first adapter did not replace the quarantined binding with the healthy hop")
	}
	servers := map[string]*nativeLoopbackProxy{spec.keys[0]: firstProxy, spec.keys[1]: secondProxy}
	before := servers[bound].requests.Load()
	second := newHopPoolAdapter("post-node-two", postHopPool, spec, p, binder, carrier, func() time.Time { return now })
	setNativeCarrier(second, nil, true)
	conn, err = second.DialContext(context.Background(), metadata)
	if err != nil {
		t.Fatalf("rebound native dial failed: %v", err)
	}
	_ = conn.Close()
	if got := servers[bound].requests.Load(); got != before+1 {
		t.Fatal("second node adapter did not reuse the shared site binding")
	}
	if got := servers[spec.keys[0]].requests.Load(); got != 0 {
		t.Fatalf("quarantined post endpoint received %d requests", got)
	}
	if got := poolState(p, spec.keys[0]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatal("shared quarantine state changed")
	}
}

func TestPostHopAllQuarantinedTriesOneNativeMemberAndDoesNotBypass(t *testing.T) {
	first, second := newNativeLoopbackProxy(t, "http"), newNativeLoopbackProxy(t, "http")
	first.status.Store(407)
	second.status.Store(407)
	spec := newHopPoolSpec(postHopPool, []string{
		"http://private-user:private-secret@" + first.listener.Addr().String(),
		"http://other-user:other-secret@" + second.listener.Addr().String(),
	}, 0)
	now := time.Unix(500, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(17, 19)})
	for _, key := range spec.keys {
		for range 3 {
			p.RecordHard(key)
		}
	}
	adapter := newHopPoolAdapter("post-all-quarantined", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "other-secret") {
		t.Fatal("all-quarantined native post dial did not return a sanitized hop-pool failure")
	}
	attempts := first.requests.Load() + second.requests.Load()
	if attempts != 1 {
		t.Fatalf("all-quarantined native post pool attempted %d endpoints, want exactly one", attempts)
	}
}

func TestPostHopDeadCarrierFailureDoesNotChangeHopHealth(t *testing.T) {
	first, second := newNativeLoopbackProxy(t, "http"), newNativeLoopbackProxy(t, "http")
	spec := newHopPoolSpec(postHopPool, []string{first.uri, second.uri}, 0)
	now := time.Unix(600, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(23, 29)})
	adapter := newHopPoolAdapter("post-dead-carrier", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || !errors.As(err, new(*poolCarrierError)) {
		t.Fatal("missing carrier was not preserved as a native carrier failure")
	}
	for _, key := range spec.keys {
		if got := poolState(p, key); got.HardFailures != 0 || got.Sand != 0 || got.State != pool.Healthy {
			t.Fatal("missing carrier changed post-hop health")
		}
	}
}

func TestPostHopCarrierConnectErrorSurvivesPoolFailure(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{
		"http://user:password@one.invalid:8080",
		"http://user:password@two.invalid:8080",
	}, 0)
	now := time.Unix(700, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(31, 37)})
	carrierErr := errors.New("carrier sentinel")
	groupHealth := newHealthTable()
	groupHealth.register([]string{"node"})
	carrierHealth := groupHealth.health("node")
	carrierHealth.recordProbe(true, time.Millisecond)
	adapter := newHopPoolAdapter("post-carrier-error", postHopPool, spec, p, nil, carrierHealth, func() time.Time { return now })
	carrier := &nativeFailingCarrier{name: "node", err: carrierErr}
	setNativeCarrier(adapter, carrier, false)
	for _, hop := range adapter.hops {
		hop.nodeAddress = "node.invalid:443"
	}
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || !errors.Is(err, carrierErr) {
		t.Fatal("carrier connect error was not preserved outside hop-pool classification")
	}
	var carrierError *poolCarrierError
	if !errors.As(err, &carrierError) {
		t.Fatal("carrier connect error lost its carrier classification")
	}
	if strings.Contains(err.Error(), "password") {
		t.Fatal("carrier error leaked hop credentials")
	}
	for _, key := range spec.keys {
		if got := poolState(p, key); got.HardFailures != 0 || got.Sand != 0 || got.State != pool.Healthy {
			t.Fatalf("carrier error changed hop health: %#v", got)
		}
	}
}

func TestPostHopHealthCountsStayBoundedWithNativeCarrier(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "http")
	proxy.status.Store(407)
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
	now := time.Unix(800, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	adapter := newHopPoolAdapter("post-bounded-health", postHopPool, spec, p, nil, carrier, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)
	for range 9 {
		_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
		if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("native post failure did not remain sanitized")
		}
	}
	// Five soft refusals in a row quarantine the hop once; refusals while it is
	// quarantined (it is the only hop, so it is still tried) change nothing.
	got := poolState(p, spec.keys[0])
	if got.HardFailures != 3 || got.Sand <= 0 || got.Trips != 1 || got.State != pool.Quarantined || !got.NextProbe.Equal(now.Add(30*time.Second)) {
		t.Fatalf("repeated refusals: hard=%d sand=%v trips=%d state=%s next=%v, want one soft-streak trip", got.HardFailures, got.Sand, got.Trips, got.State, got.NextProbe)
	}
	if attempts := proxy.targets.Load(); attempts != 9 {
		t.Fatalf("native proxy attempts = %d, want one per request", attempts)
	}
}

func TestNativePrePoolSendsOriginalTargetWithoutMutatingCallerMetadata(t *testing.T) {
	first, second := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	now, seed := time.Unix(300, 0), [2]uint64{13, 21}
	spec := newHopPoolSpec(preHopPool, []string{first.uri, second.uri}, 0)
	index := nativeFirstIndex(spec, now, seed)
	servers := []*nativeLoopbackProxy{first, second}
	servers[index].mode.Store(nativeProxyBadGreeting)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	adapter := newHopPoolAdapter("metadata-test", preHopPool, spec, p, nil, nil, func() time.Time { return now })
	original := &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443}
	conn, err := adapter.DialContext(context.Background(), original)
	if err != nil || conn == nil {
		t.Fatalf("native pre pool did not fail over safely: %v", err)
	}
	_ = conn.Close()
	if original.Host != "www.example.test" || original.DstPort != 443 {
		t.Fatal("native hop dialing mutated caller metadata")
	}
	if servers[index].targets.Load() != 0 || servers[1-index].targets.Load() != 1 {
		t.Fatal("native pre handshake retry sent unexpected target requests")
	}
}
