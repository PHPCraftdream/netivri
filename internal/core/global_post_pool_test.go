package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

// A one-hop global list is the legacy scalar: the prepared mappings stay
// identical, for the default route alone, with a default node and behind
// account routes, in generation 0 and later ones.
func TestGlobalPostPoolOneHopIsByteIdenticalToScalar(t *testing.T) {
	const hop = "socks5://post.example:1080"
	wantPost := func(name string, node bool) map[string]any {
		m := map[string]any{"name": name, "type": "socks5", "server": "post.example", "port": "1080"}
		if node {
			m["dialer-proxy"] = nodeName
		}
		return m
	}
	for _, gen := range []int{0, 3} {
		name := globalPostProxyName(gen)
		cases := []struct {
			label       string
			options     Options
			wantProxies []map[string]any
			wantRules   []string
		}{
			{
				label:       "default route only",
				options:     Options{},
				wantProxies: []map[string]any{wantPost(name, false)},
				wantRules:   []string{"MATCH," + name},
			},
			{
				label:   "direct accounts keep the fallback",
				options: Options{AccountRoute: map[string]string{"alice": "d"}, RoutePre: map[string]string{"d": "none"}, RoutePost: map[string]string{"d": "none"}},
				wantProxies: []map[string]any{
					wantPost(name, false),
				},
				wantRules: []string{"IN-USER,alice,DIRECT", "MATCH," + name},
			},
		}
		for _, tc := range cases {
			t.Run(tc.label, func(t *testing.T) {
				scalar, pooled := tc.options, tc.options
				scalar.GlobalPost = hop
				pooled.GlobalPostPool = []string{hop}
				pooledWithScalar := pooled
				pooledWithScalar.GlobalPost = "socks5://ignored.example:1080"
				for label, o := range map[string]Options{"pool": pooled, "pool overrides scalar": pooledWithScalar} {
					wantP, wantG, wantR, err := prepareRoutesGen(scalar, gen)
					if err != nil {
						t.Fatal(err)
					}
					gotP, gotG, gotR, err := prepareRoutesGen(o, gen)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(gotP, wantP) || !reflect.DeepEqual(gotG, wantG) || !reflect.DeepEqual(gotR, wantR) {
						t.Fatalf("%s: one-hop pool output differs from the scalar output", label)
					}
					if !reflect.DeepEqual(gotP, tc.wantProxies) || len(gotG) != 0 || !reflect.DeepEqual(gotR, tc.wantRules) {
						t.Fatalf("%s: frozen scalar mapping changed: proxies=%v groups=%v rules=%v", label, gotP, gotG, gotR)
					}
				}
			})
		}

		t.Run("default node", func(t *testing.T) {
			scalar := Options{Node: socks5Node(), GlobalPost: hop}
			pooled := Options{Node: socks5Node(), GlobalPostPool: []string{hop}}
			wantP, wantG, wantR, err := prepareRoutesGen(scalar, gen)
			if err != nil {
				t.Fatal(err)
			}
			gotP, gotG, gotR, err := prepareRoutesGen(pooled, gen)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotP, wantP) || !reflect.DeepEqual(gotG, wantG) || !reflect.DeepEqual(gotR, wantR) {
				t.Fatal("one-hop pool behind a default node differs from the scalar output")
			}
			if last := gotP[len(gotP)-1]; !reflect.DeepEqual(last, wantPost(name, true)) {
				t.Fatalf("global post behind the node = %v", last)
			}
		})
	}
}

// A list without a real hop is no global post at all.
func TestGlobalPostPoolWithoutHopsAddsNothing(t *testing.T) {
	for _, list := range [][]string{{}, {"none"}, {"NoNe"}} {
		o := Options{GlobalPostPool: list, GlobalPost: "socks5://ignored.example:1080"}
		proxies, groups, rules, err := prepareRoutesGen(o, 0)
		if err != nil || proxies != nil || groups != nil || rules != nil {
			t.Fatalf("pool %v: proxies=%v groups=%v rules=%v err=%v, want nothing", list, proxies, groups, rules, err)
		}
		if o.globalPostActive() {
			t.Fatalf("pool %v is active", list)
		}
	}
}

func TestGlobalPostPoolSeveralHopsBuildsPlaceholderGroup(t *testing.T) {
	hops := []string{"socks5://one.example:1080", "http://two.example:8080"}
	for _, gen := range []int{0, 2} {
		name := globalPostProxyName(gen)
		wantGroup := map[string]any{"name": name, "type": "select", "proxies": []string{"DIRECT"}}

		proxies, groups, rules, err := prepareRoutesGen(Options{GlobalPostPool: hops}, gen)
		if err != nil {
			t.Fatal(err)
		}
		if len(proxies) != 0 || !reflect.DeepEqual(groups, []map[string]any{wantGroup}) || !reflect.DeepEqual(rules, []string{"MATCH," + name}) {
			t.Fatalf("default route only: proxies=%v groups=%v rules=%v", proxies, groups, rules)
		}

		accounts := Options{
			GlobalPostPool: hops,
			AccountRoute:   map[string]string{"alice": "d"},
			RoutePre:       map[string]string{"d": "none"},
			RoutePost:      map[string]string{"d": "none"},
		}
		proxies, groups, rules, err = prepareRoutesGen(accounts, gen)
		if err != nil {
			t.Fatal(err)
		}
		if len(proxies) != 0 || !reflect.DeepEqual(groups, []map[string]any{wantGroup}) ||
			!reflect.DeepEqual(rules, []string{"IN-USER,alice,DIRECT", "MATCH," + name}) {
			t.Fatalf("with accounts: proxies=%v groups=%v rules=%v", proxies, groups, rules)
		}
	}
}

func TestGlobalPostPoolValidation(t *testing.T) {
	hops := []string{"socks5://one.example:1080", "http://two.example:8080"}
	if _, _, _, err := prepareRoutesGen(Options{Node: socks5Node(), GlobalPostPool: hops}, 0); err == nil {
		t.Error("a global pool of several hops with a default node was accepted")
	}
	if _, _, _, err := prepareRoutesGen(Options{Node: socks5Node(), GlobalPostPool: hops[:1]}, 0); err != nil {
		t.Errorf("a one-hop global pool with a default node: %v", err)
	}
	if _, _, _, err := prepareRoutesGen(Options{GlobalPostPool: []string{"socks5://one.example:1080", "ftp://bad.example:21"}}, 0); err == nil {
		t.Error("an invalid hop in the global pool was accepted")
	}
	// An unreachable invalid scalar stays ignored when a pool overrides it.
	if _, _, _, err := prepareRoutesGen(Options{GlobalPostPool: hops, GlobalPost: "ftp://bad.example:21"}, 0); err != nil {
		t.Errorf("the scalar was validated although the pool overrides it: %v", err)
	}
}

func TestGlobalPostPoolPrefersPoolOverScalar(t *testing.T) {
	cases := []struct {
		o    Options
		want []string
	}{
		{Options{}, nil},
		{Options{GlobalPost: "socks5://a.example:1080"}, []string{"socks5://a.example:1080"}},
		{Options{GlobalPost: "none"}, nil},
		{Options{GlobalPost: "socks5://a.example:1080", GlobalPostPool: []string{}}, nil},
		{Options{GlobalPost: "socks5://a.example:1080", GlobalPostPool: []string{"http://b.example:8080", ""}}, []string{"http://b.example:8080"}},
	}
	for index, tc := range cases {
		if got := tc.o.globalPostPool(); (len(got) != 0 || len(tc.want) != 0) && !reflect.DeepEqual(got, tc.want) || tc.o.globalPostActive() != (len(tc.want) != 0) {
			t.Errorf("case %d: pool = %v, want %v", index, got, tc.want)
		}
	}
}

func globalPoolOptions(hops ...string) Options { return Options{GlobalPostPool: hops} }

func TestGlobalPostPoolLifecycleInstallsAdapterAndKeepsHealthAcrossGenerations(t *testing.T) {
	a, b, c := "http://a.invalid:8080", "http://b.invalid:8080", "http://c.invalid:8080"
	keyA, keyB, keyC := hopPoolKey(a), hopPoolKey(b), hopPoolKey(c)
	binder := &testSiteBinder{values: map[string]string{
		anonymousBindLogin + "\x00a.example": keyA,
		anonymousBindLogin + "\x00b.example": keyB,
	}}
	m := newHopPoolLifecycle(nil)
	install := func(gen int, hops ...string) (map[string]C.Proxy, []string) {
		t.Helper()
		o := globalPoolOptions(hops...)
		o.SiteBinder = binder
		updated, names, err := m.installGen(o, nil, make(map[string]C.Proxy), nil, gen)
		if err != nil {
			t.Fatal(err)
		}
		m.completeInstall(nil)
		return updated, names
	}

	updated, names := install(1, a, b)
	adapterName := globalPostProxyName(1)
	if got := updated[adapterName]; got == nil || got.Type() != C.Selector {
		t.Fatalf("global post adapter %q was not installed as a pool adapter", adapterName)
	}
	if !reflect.DeepEqual(names, []string{adapterName}) {
		t.Fatalf("installed names = %v", names)
	}
	logical := hopPoolLogicalKey{role: postHopPool, route: GlobalPoolRoute, node: -1}
	use := m.uses[logical]
	if use == nil || !use.direct || use.carrierMember != nil || use.adapterName != adapterName || use.pool == nil {
		t.Fatalf("global pool use = %+v", use)
	}
	snapshot := m.snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("snapshot has %d entries, want 2", len(snapshot))
	}
	for _, entry := range snapshot {
		if entry.RouteKey != GlobalPoolRoute || entry.Role != "post" {
			t.Fatalf("snapshot entry %+v is not the global post pool", entry)
		}
	}
	for _, targets := range m.targets[use.pool] {
		if len(targets) != 1 || !targets[0].direct || targets[0].uri == "" {
			t.Fatal("global pool probe target is not native and direct")
		}
	}

	// Quarantine B, then reorder and add C: B stays quarantined, bindings stay.
	for range 3 {
		use.pool.RecordHard(keyB)
	}
	if poolState(use.pool, keyB).State != pool.Quarantined {
		t.Fatal("setup: B was not quarantined")
	}
	install(2, b, a, c)
	next := m.uses[logical]
	if next == nil || next.adapterName != globalPostProxyName(2) {
		t.Fatalf("generation 2 use = %+v", next)
	}
	if got := poolState(next.pool, keyB); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("reordered pool lost the health of B: %+v", got)
	}
	if poolState(next.pool, keyC).State != pool.Healthy {
		t.Fatal("added hop C is not healthy")
	}
	if len(binder.values) != 2 {
		t.Fatalf("reorder dropped bindings: %v", binder.values)
	}

	// Removing A forgets its bindings, nothing else.
	install(3, b, c)
	if _, ok := binder.Get(anonymousBindLogin, "a.example"); ok {
		t.Fatal("binding of the removed hop survived")
	}
	if got, ok := binder.Get(anonymousBindLogin, "b.example"); !ok || got != keyB {
		t.Fatalf("binding of the kept hop = %q, %v", got, ok)
	}
	if len(m.snapshot()) != 2 {
		t.Fatalf("snapshot after removal = %d entries, want 2", len(m.snapshot()))
	}

	// One hop left: the pool adapter is gone (the legacy scalar proxy takes over).
	updated, names = install(4, b)
	if len(names) != 0 || updated[globalPostProxyName(4)] != nil || len(m.uses) != 0 {
		t.Fatalf("a one-hop global list kept a pool: names=%v uses=%d", names, len(m.uses))
	}
}

func TestGlobalPostPoolPostHopKeysFollowTheList(t *testing.T) {
	a, b := "http://a.invalid:8080", "http://b.invalid:8080"
	keys := postHopKeys(globalPoolOptions(a, b))
	if !keys[hopPoolKey(a)] || !keys[hopPoolKey(b)] || len(keys) != 2 {
		t.Fatalf("global pool post keys = %v", keys)
	}
}

// Connections without a login bind their sites under one fixed key; a login
// keeps a binding of its own.
func TestPostPoolBindsAnonymousConnectionsUnderFixedKey(t *testing.T) {
	first, second := newNativeLoopbackProxy(t, "http"), newNativeLoopbackProxy(t, "http")
	spec := newHopPoolSpec(postHopPool, []string{first.uri, second.uri}, 0)
	now := time.Unix(300, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(5, 6)})
	binder := &testSiteBinder{values: make(map[string]string)}
	adapter := newHopPoolAdapter("global-post", postHopPool, spec, p, binder, nil, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)

	anonymous := &C.Metadata{NetWork: C.TCP, Host: "www.example.test", DstPort: 443}
	conn, err := adapter.DialContext(context.Background(), anonymous)
	if err != nil {
		t.Fatalf("anonymous dial: %v", err)
	}
	_ = conn.Close()
	bound, ok := binder.Get(anonymousBindLogin, "example.test")
	if !ok {
		t.Fatal("an anonymous connection did not bind its site")
	}
	if _, ok := binder.Get("", "example.test"); ok {
		t.Fatal("the empty login was used as a binding key")
	}
	if _, ok := binder.Get("alice", "example.test"); ok {
		t.Fatal("an anonymous binding leaked to a login")
	}

	byKey := map[string]*nativeLoopbackProxy{hopPoolKey(first.uri): first, hopPoolKey(second.uri): second}
	before := byKey[bound].requests.Load()
	for range 3 {
		conn, err = adapter.DialContext(context.Background(), anonymous)
		if err != nil {
			t.Fatalf("bound anonymous dial: %v", err)
		}
		_ = conn.Close()
	}
	if got := byKey[bound].requests.Load(); got != before+3 {
		t.Fatalf("the bound hop got %d new requests, want 3", got-before)
	}
}

func TestBindLogin(t *testing.T) {
	if bindLogin("") != anonymousBindLogin || bindLogin("alice") != "alice" {
		t.Fatal("bindLogin does not map the empty login to the anonymous key")
	}
	if validAccountLogin(anonymousBindLogin) {
		t.Fatal("the anonymous binding key is a valid account login")
	}
}

func TestMatchProbeFilter(t *testing.T) {
	if matchProbeFilter(Options{}, 0) != nil {
		t.Error("no global post and no node must not filter the probes")
	}
	node := matchProbeFilter(Options{Node: socks5Node()}, 0)
	if node == nil || !node(nodeName) || node(globalPostProxyName(0)) {
		t.Error("a default node alone probes only the node")
	}
	global := matchProbeFilter(Options{Node: socks5Node(), GlobalPostPool: []string{"socks5://a.example:1080"}}, 2)
	if global == nil || !global(globalPostProxyName(2)) || global(globalPostProxyName(0)) || global(nodeName) {
		t.Error("a global post probes only its own generation")
	}
}

func TestHasNodeRoute(t *testing.T) {
	o := Options{AccountRoute: map[string]string{"a": "direct", "b": "routed"}, RouteNodes: map[string][]map[string]any{"routed": {socks5Node()}, "unused": {socks5Node()}}}
	if !hasNodeRoute(o) {
		t.Fatal("a node-backed account route was not found")
	}
	delete(o.AccountRoute, "b")
	if hasNodeRoute(o) {
		t.Fatal("an unreferenced route with nodes counts as node-backed")
	}
}

// An instance in rule mode without node state refuses what it cannot take and
// leaves the servers alone.
func TestReconfigureDirectRefusesNodeRoutesAndEmptyRules(t *testing.T) {
	p := registerUnitServerProtocol(t)
	s := &serverSet{broker: newSocketBroker()}
	spec := unitServerSpec("edge", p.name, 1080)
	tx, err := s.prepare([]servers.Spec{spec}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare running server:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit running server:", err)
	}
	inst := &Instance{route: routeDirect, servers: s}
	inst.routing.Store(&routingState{rule: true, hopPools: newHopPoolLifecycle(nil)})
	lifecycleMu.Lock()
	previous := current
	current = inst
	lifecycleMu.Unlock()
	t.Cleanup(func() {
		lifecycleMu.Lock()
		current = previous
		lifecycleMu.Unlock()
		s.close()
	})
	starts := p.starts.Load()
	policy := s.policy.Load()

	nodeRoute := Options{
		Servers:      []servers.Spec{spec},
		AccountRoute: map[string]string{"alice": "r"},
		RouteNodes:   map[string][]map[string]any{"r": {testNode("192.0.2.1")}},
	}
	if err := inst.Reconfigure(nodeRoute); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("a node-backed route on a node-less instance: %v, want ErrReconfigureUnsupported", err)
	}
	if err := inst.Reconfigure(Options{Servers: []servers.Spec{spec}, GlobalPostPool: []string{"none"}}); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("an instance left without any rule: %v, want ErrReconfigureUnsupported", err)
	}
	if s.policy.Load() != policy || p.starts.Load() != starts {
		t.Error("a refused Reconfigure touched the servers")
	}
}
