package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

func TestHopLifecycleTransitionsAndSafeObservability(t *testing.T) {
	now := time.Unix(100, 0)
	var events []HopEvent
	m := newHopPoolLifecycleWithClock(func(event HopEvent) { events = append(events, event) }, func() time.Time { return now })
	spec := newHopPoolSpec(preHopPool, []string{"socks5://user:secret@hop.invalid:1080", "http://other.invalid:8080"}, 1)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	logical := hopPoolLogicalKey{role: preHopPool, route: "route-secret", node: 0}
	m.uses[logical] = &hopPoolUse{
		logical: logical, role: preHopPool, route: logical.route, spec: spec, pool: p,
		members: map[string]member{spec.keys[0]: &fakeMember{name: "entry"}},
	}
	for i := 0; i < 3; i++ {
		m.mutate(p, preHopPool, logical.route, spec.keys[0], func() { p.RecordHard(spec.keys[0]) })
	}
	if len(events) != 1 || events[0].Kind != HopEventQuarantined || events[0].Trips != 1 {
		t.Fatal("quarantine transition was not emitted exactly once")
	}
	if events[0].RouteKey != logical.route {
		t.Fatal("opaque caller route key was changed")
	}
	now = now.Add(30 * time.Second)
	claim := p.DueProbes(now)[0]
	accepted := false
	m.mutate(p, preHopPool, logical.route, spec.keys[0], func() {
		accepted = p.ReportProbe(claim, pool.ProbeSuccess)
	})
	if !accepted {
		t.Fatal("leased probe was not accepted")
	}
	if len(events) != 2 || events[1].Kind != HopEventRevived {
		t.Fatal("revival transition missing")
	}
	if strings.Contains(events[0].Key, "hop.invalid") || strings.Contains(events[0].Key, "secret") {
		t.Fatal("event leaked URL or credentials")
	}
	for _, snapshot := range m.snapshot() {
		if strings.Contains(snapshot.Key+snapshot.PoolIdentity, "hop.invalid") || strings.Contains(snapshot.RouteKey, "user") {
			t.Fatal("snapshot leaked URL or credentials")
		}
	}
	for _, state := range m.export() {
		for _, entry := range state.Entries {
			if strings.Contains(entry.Key, "hop.invalid") || strings.Contains(entry.Key, "secret") {
				t.Fatal("export leaked URL or credentials")
			}
		}
	}
}

func TestHopLifecycleRetainsPoolAndDropsRemovedKeys(t *testing.T) {
	now := time.Unix(900, 0)
	m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	oldSpec := newHopPoolSpec(postHopPool, []string{"http://one.invalid:80", "http://two.invalid:80"}, 0)
	p := pool.New(oldSpec.keys, pool.Options{Clock: func() time.Time { return now }})
	p.RecordSoft(oldSpec.keys[0])
	p.RecordHard(oldSpec.keys[0])
	p.RecordHard(oldSpec.keys[0])
	for range 3 {
		p.RecordHard(oldSpec.keys[1])
	}
	m.pools[poolIdentity(postHopPool, oldSpec.keys)] = p
	logical := hopPoolLogicalKey{role: postHopPool, route: "r", node: 0}
	m.uses[logical] = &hopPoolUse{logical: logical, role: postHopPool, route: "r", spec: oldSpec, pool: p}
	newSpec := newHopPoolSpec(postHopPool, []string{"http://one.invalid:80", "http://three.invalid:80"}, 1)
	assigned := m.assignPoolsLocked([]*hopPoolUse{{logical: logical, role: postHopPool, route: "r", spec: newSpec}})
	fork := assigned[poolIdentity(postHopPool, newSpec.keys)]
	if fork == p {
		t.Fatal("changed route pool reused the predecessor pointer")
	}
	states := fork.Snapshot()
	keys := []string{states[0].Key, states[1].Key}
	if len(keys) != 2 || keys[0] != newSpec.keys[0] || keys[1] != newSpec.keys[1] {
		t.Fatal("fork did not replace removed keys")
	}
	if len(m.pools) != 1 {
		t.Fatal("stale pool identity retained")
	}
	if got := poolState(fork, newSpec.keys[0]); got.HardFailures != 2 || got.Sand <= 0 {
		t.Fatalf("fork did not carry shared health: %#v", got)
	}
	if got := poolState(fork, newSpec.keys[1]); got.State != pool.Healthy || got.Trips != 0 {
		t.Fatalf("new key inherited removed-key quarantine: %#v", got)
	}
	for _, state := range fork.Snapshot() {
		if state.Key == oldSpec.keys[1] {
			t.Fatal("removed key remained in the fork")
		}
	}
	if got := poolState(p, oldSpec.keys[0]); got.HardFailures != 2 || got.Sand <= 0 {
		t.Fatalf("fork changed predecessor health: %#v", got)
	}
	if got := poolState(p, oldSpec.keys[1]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("fork changed predecessor quarantine: %#v", got)
	}
	oldKeys := p.Snapshot()
	if len(oldKeys) != 2 || oldKeys[0].Key != oldSpec.keys[0] || oldKeys[1].Key != oldSpec.keys[1] {
		t.Fatal("fork changed predecessor membership")
	}
}

func TestHopLifecycleForksHealthIntoSplitPools(t *testing.T) {
	now := time.Unix(1200, 0)
	m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	oldSpec := newHopPoolSpec(preHopPool, []string{"socks5://one.invalid:1080", "socks5://two.invalid:1080"}, 0)
	p := pool.New(oldSpec.keys, pool.Options{Clock: func() time.Time { return now }})
	p.RecordSoft(oldSpec.keys[0])
	p.RecordHard(oldSpec.keys[0])
	p.RecordHard(oldSpec.keys[0])
	for range 3 {
		p.RecordHard(oldSpec.keys[1])
	}
	m.pools[poolIdentity(preHopPool, oldSpec.keys)] = p
	logical := hopPoolLogicalKey{role: preHopPool, route: "r", node: 0}
	otherLogical := hopPoolLogicalKey{role: preHopPool, route: "r", node: 1}
	m.uses[logical] = &hopPoolUse{logical: logical, role: preHopPool, route: "r", spec: oldSpec, pool: p}
	m.uses[otherLogical] = &hopPoolUse{logical: otherLogical, role: preHopPool, route: "r", spec: oldSpec, pool: p}
	splitSpecs := []*hopPoolSpec{
		newHopPoolSpec(preHopPool, []string{"socks5://one.invalid:1080", "socks5://three.invalid:1080"}, 1),
		newHopPoolSpec(preHopPool, []string{"socks5://two.invalid:1080", "socks5://four.invalid:1080"}, 1),
	}
	uses := []*hopPoolUse{
		{logical: logical, role: preHopPool, route: "r", spec: splitSpecs[0]},
		{logical: hopPoolLogicalKey{role: preHopPool, route: "r", node: 1}, role: preHopPool, route: "r", spec: splitSpecs[1]},
	}
	assigned := m.assignPoolsLocked(uses)
	first := assigned[poolIdentity(preHopPool, splitSpecs[0].keys)]
	second := assigned[poolIdentity(preHopPool, splitSpecs[1].keys)]
	if first == p || second == p || first == second {
		t.Fatal("split pools did not get independent forks")
	}
	if got := poolState(first, splitSpecs[0].keys[0]); got.HardFailures != 2 || got.Sand <= 0 {
		t.Fatalf("first split fork did not carry surviving health: %#v", got)
	}
	if got := poolState(second, splitSpecs[1].keys[0]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("second split fork did not carry quarantine: %#v", got)
	}
	if got := poolState(p, oldSpec.keys[0]); got.HardFailures != 2 || got.Sand <= 0 {
		t.Fatalf("split changed predecessor health: %#v", got)
	}
	if got := poolState(p, oldSpec.keys[1]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("split changed predecessor quarantine: %#v", got)
	}
	oldKeys := p.Snapshot()
	if len(oldKeys) != 2 || oldKeys[0].Key != oldSpec.keys[0] || oldKeys[1].Key != oldSpec.keys[1] {
		t.Fatal("split changed predecessor membership")
	}
}

type forgetTrackingBinder struct {
	*testSiteBinder
	forgotten []string
}

func (b *forgetTrackingBinder) Forget(key string) int {
	b.forgotten = append(b.forgotten, key)
	return b.testSiteBinder.Forget(key)
}

func TestHopLifecycleInstallForgetsOnlyGloballyRemovedPostHop(t *testing.T) {
	uriA, uriB, uriC, uriD := "http://a.invalid:8080", "http://b.invalid:8080", "http://c.invalid:8080", "http://d.invalid:8080"
	binder := &forgetTrackingBinder{testSiteBinder: &testSiteBinder{values: make(map[string]string)}}
	m := newHopPoolLifecycle(nil)
	install := func(uris []string) {
		o := Options{
			AccountRoute: map[string]string{"alice": "route"},
			RouteNodes:   map[string][]map[string]any{"route": {socks5Node()}},
			NodePostPool: map[string][][]string{"route": {uris}},
			SiteBinder:   binder,
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
		for _, mapping := range mappings {
			name := mapping["name"].(string)
			proxies[name] = testWarmProxy{name: name, typ: C.Socks5}
		}
		health := newHealthTable()
		health.registerMembers(healthMembersForPlans(plans))
		if _, _, err := m.installGen(o, plans, proxies, health, 0); err != nil {
			t.Fatal(err)
		}
		m.completeInstall(nil)
	}
	keyA, keyB := hopPoolKey(uriA), hopPoolKey(uriB)
	keyC, keyD := hopPoolKey(uriC), hopPoolKey(uriD)
	binder.values["alice\x00a.example"] = keyA
	binder.values["alice\x00b.example"] = keyB
	binder.values["alice\x00c.example"] = keyC
	install([]string{uriA, uriB, uriC})
	if len(binder.forgotten) != 0 {
		t.Fatalf("initial install forgot keys: %v", binder.forgotten)
	}
	install([]string{uriA, uriC, uriD})
	if !reflect.DeepEqual(binder.forgotten, []string{keyB}) {
		t.Fatalf("Forget calls = %v, want exactly removed B key %q", binder.forgotten, keyB)
	}
	for site, key := range map[string]string{"a.example": keyA, "c.example": keyC} {
		if got, ok := binder.Get("alice", site); !ok || got != key {
			t.Fatalf("retained %s binding = %q, %v; want %q", site, got, ok, key)
		}
	}
	if _, ok := binder.Get("alice", "b.example"); ok {
		t.Fatal("removed B binding survived install")
	}
	if _, ok := binder.Get("alice", "d.example"); ok {
		t.Fatal("new D key caused an unexpected binding")
	}
	if keyD == keyB {
		t.Fatal("new D identity unexpectedly matches removed B")
	}
}

func TestHopLifecycleCompletesBinderCleanupWithoutForgettingScalarPostKey(t *testing.T) {
	removed := hopPoolKey("http://removed.invalid:80")
	retained := hopPoolKey("http://retained.invalid:80")
	binder := &testSiteBinder{values: map[string]string{
		"alice\x00removed.example":  removed,
		"alice\x00retained.example": retained,
	}}
	options := Options{
		AccountRoute: map[string]string{"alice": "route"},
		RoutePost:    map[string]string{"route": "http://retained.invalid:80"},
	}
	if keys := postHopKeys(options); !keys[retained] || keys[removed] {
		t.Fatalf("post scalar keys = %v, want retained only", keys)
	}
	m := newHopPoolLifecycle(nil)
	m.binder = binder
	m.postKeys = map[string]bool{removed: true, retained: true}
	m.pendingForget = []string{removed}
	m.completeInstall(nil)
	if _, ok := binder.Get("alice", "removed.example"); ok {
		t.Fatal("removed post key binding was not forgotten")
	}
	if got, ok := binder.Get("alice", "retained.example"); !ok || got != retained {
		t.Fatal("scalar post key binding was forgotten")
	}
}

func TestHopLifecycleExportRestoreQuarantineAndRejectUnknownKey(t *testing.T) {
	now := time.Unix(1800, 0)
	spec := newHopPoolSpec(preHopPool, []string{"socks5://one.invalid:1080", "socks5://two.invalid:1080"}, 0)
	logical := hopPoolLogicalKey{role: preHopPool, route: "route", node: 0}
	makeLifecycle := func() (*hopPoolLifecycle, *pool.Pool) {
		m := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
		p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
		m.pools[poolIdentity(preHopPool, spec.keys)] = p
		m.uses[logical] = &hopPoolUse{logical: logical, role: preHopPool, route: logical.route, spec: spec, pool: p}
		return m, p
	}
	source, sourcePool := makeLifecycle()
	for range 3 {
		sourcePool.RecordHard(spec.keys[1])
	}
	states := source.export()
	if len(states) != 1 || len(states[0].Entries) != 1 || states[0].Entries[0].Key != spec.keys[1] {
		t.Fatalf("exported quarantine state = %#v", states)
	}
	target, targetPool := makeLifecycle()
	if err := target.restore(states); err != nil {
		t.Fatalf("restore known quarantine: %v", err)
	}
	if got := poolState(targetPool, spec.keys[1]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("restored quarantine = %#v", got)
	}
	claims := targetPool.DueProbes(now)
	if len(claims) != 1 || claims[0].Key() != spec.keys[1] {
		t.Fatalf("restored quarantine was not due now: %v", claims)
	}

	fresh, freshPool := makeLifecycle()
	invalid := []HopPoolState{{
		Role: states[0].Role, RouteKey: states[0].RouteKey, PoolIdentity: states[0].PoolIdentity,
		Entries: []pool.Entry{{Key: spec.keys[0], Trips: 1}, {Key: "unknown-opaque-key", Trips: 1}},
	}}
	if err := fresh.restore(invalid); err == nil {
		t.Fatal("restore accepted an unknown key")
	}
	if got := poolState(freshPool, spec.keys[0]); got.State != pool.Healthy || got.Trips != 0 {
		t.Fatalf("failed restore mutated known-key state: %#v", got)
	}
}

func TestHopLifecycleReviverNormalizesOptionsAndClampsTimeout(t *testing.T) {
	m := newHopPoolLifecycle(nil)
	if _, err := m.newReviver(WarmOptions{}); err != nil {
		t.Fatalf("newReviver rejected default options: %v", err)
	}
	large := WarmOptions{ProbeTimeout: 25 * time.Second}
	reviver, err := m.newReviver(large)
	if err != nil {
		t.Fatalf("newReviver rejected a timeout above the probe lease: %v", err)
	}
	if reviver == nil {
		t.Fatal("newReviver returned a nil reviver")
	}
}

func TestAvailableProbeTargetPrefersFreshPreAndExcludesStaleOrDownNodes(t *testing.T) {
	now := time.Unix(1500, 0)
	pre := hopProbeTarget{
		role: preHopPool, key: "pre", uri: "socks5://pre.invalid:1080",
		nodeAddress: "127.0.0.1:1", carrier: &nodeHealth{lastOK: now},
	}
	post := hopProbeTarget{
		role: postHopPool, key: "post", uri: "http://post.invalid:8080",
		nodeAddress: "127.0.0.1:2", carrierMember: &fakeMember{name: "node"},
		carrier: &nodeHealth{lastOK: now},
	}

	got, ok := availableProbeTarget([]hopProbeTarget{pre, post}, now)
	if !ok || got.key != "pre" {
		t.Fatalf("fresh pre carrier was not preferred in candidate order: key=%q available=%v", got.key, ok)
	}
	if _, ok := availableProbeTarget([]hopProbeTarget{post}, now.Add(time.Hour)); ok {
		t.Fatal("stale post carrier evidence made a probe target available")
	}

	downPost := post
	downPost.carrier = &nodeHealth{lastOK: now}
	downPost.carrier.downFlag.Store(true)
	if _, ok := availableProbeTarget([]hopProbeTarget{downPost}, now); ok {
		t.Fatal("down post node was selected as a live probe target")
	}

	fallback := pre
	fallback.carrier = nil
	got, ok = availableProbeTarget([]hopProbeTarget{fallback, post}, now)
	if !ok || got.key != "post" {
		t.Fatalf("pre fallback displaced a live post target: key=%q available=%v", got.key, ok)
	}
	got, ok = availableProbeTarget([]hopProbeTarget{fallback}, now)
	if !ok || got.key != "pre" {
		t.Fatalf("pre fallback was not available when no live carrier existed: key=%q available=%v", got.key, ok)
	}
}

func TestLifecyclePoolProbeUsesFreshPreCarrierBeforeLivePost(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "socks5")
	now := time.Unix(1600, 0)
	carrierHealth := &nodeHealth{lastOK: now}
	key := "shared-hop-key"
	p := pool.New([]string{key}, pool.Options{})
	lifecycle := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	lifecycle.targets[p] = map[string][]hopProbeTarget{
		key: {
			{
				role: preHopPool, key: key, uri: proxy.uri, nodeAddress: "127.0.0.1:1",
				carrier: &nodeHealth{lastOK: now}, hopAddress: hopAddress(proxy.uri), protocol: "socks5",
			},
			{
				role: postHopPool, key: key, uri: "socks5://unused.invalid:1080", nodeAddress: "127.0.0.1:2",
				carrier: carrierHealth, carrierMember: &nativeFailingCarrier{name: "live-node"},
			},
		},
	}
	prober := lifecyclePoolProber{
		lifecycle: lifecycle, pool: p,
		options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"},
	}
	outcome := prober.Probe(context.Background(), &mihomoPoolHop{key: key})
	if outcome != pool.ProbeSuccess || proxy.requests.Load() != 1 {
		t.Fatalf("probe did not use the live pre target: outcome=%v requests=%d", outcome, proxy.requests.Load())
	}
}

func TestHopLifecycleBaseProbeNamesIncludePreAndPostPools(t *testing.T) {
	m := newHopPoolLifecycle(nil)
	plan := routePlan{
		names: []string{"netivri-r0-0-post", "netivri-r0-1"},
		prePools: []*hopPoolSpec{
			newHopPoolSpec(preHopPool, []string{"socks5://a.invalid:1080", "socks5://b.invalid:1080"}, 0),
			nil,
		},
		postPools: []*hopPoolSpec{
			nil,
			newHopPoolSpec(postHopPool, []string{"http://a.invalid:80", "http://b.invalid:80"}, 0),
		},
	}
	got := m.baseProbeNames([]routePlan{plan})
	if len(got) != 2 || got[0] != "netivri-r0-0" || got[1] != "netivri-r0-1" {
		t.Fatalf("base probe names = %v, want both pre and post members", got)
	}
}

func TestHopLifecycleSingletonIsNotAdapted(t *testing.T) {
	m := newHopPoolLifecycle(nil)
	plan := routePlan{key: "r", gen: 0, index: 0, names: []string{"netivri-r0-0"}, prePools: []*hopPoolSpec{newHopPoolSpec(preHopPool, []string{"socks5://one.invalid:1080"}, 0)}}
	uses, err := m.prepareUses(Options{RouteNodes: map[string][]map[string]any{"r": {{}}}}, []routePlan{plan}, map[string]C.Proxy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 0 {
		t.Fatal("singleton chain was replaced by a lifecycle adapter")
	}
}
