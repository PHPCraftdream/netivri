package core

import (
	"reflect"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestPrepareRoutesDirectPostPoolKeepsScalarPathAndBuildsDirectCarrier(t *testing.T) {
	single := Options{
		AccountRoute: map[string]string{"alice": "direct"},
		RoutePost:    map[string]string{"direct": "socks5://post.invalid:1080"},
	}
	proxies, groups, rules, err := prepareRoutesGen(single, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || len(groups) != 0 ||
		proxies[0]["name"] != "netivri-r0-0-post" || proxies[0]["dialer-proxy"] != "DIRECT" ||
		!reflect.DeepEqual(rules[:1], []string{"IN-USER,alice,netivri-r0-0-post"}) {
		t.Fatalf("singleton direct-post mapping mismatch: proxy count=%d group count=%d rule count=%d", len(proxies), len(groups), len(rules))
	}

	options := Options{
		AccountRoute: map[string]string{"alice": "direct", "bob": "direct"},
		RoutePostPool: map[string][]string{"direct": {
			"socks5://first.invalid:1080",
			"http://user:secret@second.invalid:8080",
		}},
	}
	proxies, groups, rules, err = prepareRoutesGen(options, 4)
	if err != nil {
		t.Fatal(err)
	}
	postName := helperProxyName(4, 0, 0, "post")
	if len(proxies) != 0 || len(groups) != 1 {
		t.Fatalf("multi direct-post placeholder count mismatch: proxies=%d groups=%d", len(proxies), len(groups))
	}
	wantMembers := []string{"DIRECT"}
	if !reflect.DeepEqual(groups[0], map[string]any{"name": postName, "type": "select", "proxies": wantMembers}) {
		t.Fatalf("direct-post placeholder group mismatch")
	}
	for _, rule := range rules[:2] {
		if rule != "IN-USER,alice,"+postName && rule != "IN-USER,bob,"+postName {
			t.Fatalf("direct-post rule bypasses carrier")
		}
	}

	uses, err := directPostPoolUses(options, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 1 {
		t.Fatalf("direct post pool uses = %d, want 1", len(uses))
	}
	use := uses[0]
	if !use.direct || use.role != postHopPool || use.node != 0 || use.logical != (hopPoolLogicalKey{role: postHopPool, route: "direct", node: 0}) ||
		use.adapterName != postName || use.nodeAddress != "" || use.nodeProto != "" {
		t.Fatalf("direct post use metadata mismatch: direct=%t role=%d node=%d node address set=%t direct protocol=%t", use.direct, use.role, use.node, use.nodeAddress != "", use.nodeProto == "direct")
	}
	if len(use.members) != 2 {
		t.Fatalf("direct post native config count=%d, want 2", len(use.members))
	}
}

func TestDirectPostPoolUsesFollowSortedDistinctRoutesAndNeedNoNode(t *testing.T) {
	options := Options{
		AccountRoute: map[string]string{"z": "z-route", "a": "a-route", "b": "z-route", "n": "node-route", "empty": "empty-route"},
		RouteNodes: map[string][]map[string]any{
			"node-route": {socks5Node()},
		},
		RoutePostPool: map[string][]string{
			"z-route":    {"socks5://z1.invalid:1080", "socks5://z2.invalid:1080"},
			"a-route":    {"http://a1.invalid:8080", "http://a2.invalid:8080"},
			"node-route": {"http://unused.invalid:8080", "http://unused2.invalid:8080"},
		},
		RoutePre: map[string]string{"z-route": "none"},
	}
	// Route indexes are a-route=0, empty-route=1, node-route=2, z-route=3.
	// The direct-use helper omits routes without a multi-hop direct-post pool.
	uses, err := directPostPoolUses(options, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 2 {
		t.Fatalf("direct post use count mismatch: got=%d want=2", len(uses))
	}
	if uses[0].route != "a-route" || uses[1].route != "z-route" ||
		uses[0].adapterName != helperProxyName(7, 0, 0, "post") || uses[1].adapterName != helperProxyName(7, 3, 0, "post") {
		t.Fatalf("direct post uses do not follow sorted route order: first=%q second=%q", uses[0].route, uses[1].route)
	}
}

func TestDirectPostPoolLifecycleInstallWiresGenerationAdapterAndHealth(t *testing.T) {
	options := Options{
		AccountRoute: map[string]string{"alice": "direct"},
		RoutePostPool: map[string][]string{"direct": {
			"socks5://first.invalid:1080",
			"http://user:secret@second.invalid:8080",
		}},
	}
	lifecycle := newHopPoolLifecycle(nil)
	updated, names, err := lifecycle.installGen(options, nil, make(map[string]C.Proxy), nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	postName := helperProxyName(4, 0, 0, "post")
	installed := updated[postName]
	if installed == nil || installed.Name() != postName || installed.Type() != C.Selector {
		t.Fatalf("generation post native adapter was not installed as a selector")
	}
	if len(lifecycle.uses) != 1 {
		t.Fatalf("direct post lifecycle use count mismatch: got=%d want=1", len(lifecycle.uses))
	}
	use := lifecycle.uses[hopPoolLogicalKey{role: postHopPool, route: "direct", node: 0}]
	if use == nil || !use.direct || use.adapterName != postName || use.pool == nil {
		t.Fatalf("direct post lifecycle registration mismatch")
	}
	if len(lifecycle.snapshot()) != 2 || len(use.pool.Snapshot()) != 2 {
		t.Fatalf("direct post health snapshot entry count mismatch")
	}
	if len(names) != 1 || names[0] != postName {
		t.Fatalf("direct post installed adapter name mismatch")
	}
	if len(lifecycle.targets[use.pool]) != 2 {
		t.Fatalf("direct post native probe targets=%d, want two", len(lifecycle.targets[use.pool]))
	}
	for _, targets := range lifecycle.targets[use.pool] {
		if len(targets) != 1 || !targets[0].direct || targets[0].uri == "" {
			t.Fatal("direct post probe target is not native and direct")
		}
	}
}
