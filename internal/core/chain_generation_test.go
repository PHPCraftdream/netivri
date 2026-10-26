package core

import (
	"reflect"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestChainHelpersAreGeneratedAndAttributed(t *testing.T) {
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node()}},
		AccountRoute: map[string]string{"a": "r"},
		RoutePre:     map[string]string{"r": "socks5://pre.example:1080"},
		RoutePost:    map[string]string{"r": "socks5://user:secret@post.example:1080"},
	}
	proxies, groups, _, err := prepareRoutesGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	names := proxyMappingNames(proxies)
	want := []string{"netivri-g3-r0-0-pre", "netivri-g3-r0-0-post", "netivri-g3-r0-0"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("proxies = %v, want %v", names, want)
	}
	for _, proxy := range proxies {
		if proxy["password"] == "secret" && proxy["name"] != helperProxyName(3, 0, 0, "post") {
			t.Fatalf("credential on unexpected proxy %v", proxy["name"])
		}
	}
	if got := groups[0]["proxies"].([]string); !reflect.DeepEqual(got, []string{"netivri-g3-r0-0-post"}) {
		t.Fatalf("group members = %v", got)
	}
	plans, err := buildRoutePlansGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	labels := memberNameLabels(plans)
	for _, name := range []string{"netivri-g3-r0-0", "netivri-g3-r0-0-pre", "netivri-g3-r0-0-post"} {
		if labels[name] != "r0-0" {
			t.Errorf("%s attributed to %q", name, labels[name])
		}
	}
}

func TestGenerationNamesMatchMappingsPlansLabelsAndWarmLeaves(t *testing.T) {
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node()}},
		AccountRoute: map[string]string{"a": "r"},
		RoutePre:     map[string]string{"r": "socks5://pre.example:1080"},
		RoutePost:    map[string]string{"r": "socks5://post.example:1080"},
	}
	proxies, _, _, err := prepareRoutesGen(o, 9)
	if err != nil {
		t.Fatal(err)
	}
	mapped := proxyMappingNames(proxies)
	wantMapped := []string{"netivri-g9-r0-0-pre", "netivri-g9-r0-0-post", "netivri-g9-r0-0"}
	if !reflect.DeepEqual(mapped, wantMapped) {
		t.Fatalf("proxy mappings = %v, want %v", mapped, wantMapped)
	}
	plans, err := buildRoutePlansGen(o, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || !reflect.DeepEqual(plans[0].names, []string{"netivri-g9-r0-0-post"}) {
		t.Fatalf("plan names = %#v", plans)
	}
	if got := memberNameLabels(plans)["netivri-g9-r0-0-post"]; got != "r0-0" {
		t.Fatalf("plan label = %q", got)
	}
	warmable := make(map[string]C.Proxy, len(mapped))
	for _, name := range mapped {
		warmable[name] = testWarmProxy{name: name, typ: C.Socks5}
	}
	gotWarm := warmLeaves(warmable, nil)
	if len(gotWarm) != 1 || gotWarm[0].Name() != "netivri-g9-r0-0-post" {
		t.Fatalf("warm leaves = %v", gotWarm)
	}
}

func TestDirectPostRoutePlanUsesPostMember(t *testing.T) {
	o := Options{AccountRoute: map[string]string{"a": "direct"}, RoutePost: map[string]string{"direct": "socks5://post.example:1080"}}
	plans, err := buildRoutePlansGen(o, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || !reflect.DeepEqual(plans[0].names, []string{"netivri-g4-r0-0-post"}) {
		t.Fatalf("plans = %#v", plans)
	}
	if got := memberNameLabels(plans)["netivri-g4-r0-0-post"]; got != "r0-0" {
		t.Fatalf("label = %q", got)
	}
}

func TestDirectRouteBuildStickyGroupUsesBuiltinDirect(t *testing.T) {
	plans, err := buildRoutePlansGen(Options{AccountRoute: map[string]string{"a": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "none"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	direct := testWarmProxy{name: "DIRECT", typ: C.Direct}
	groups, err := buildStickyGroups(plans, map[string]C.Proxy{}, newAffinityTable(nil, 0), newHealthTable(), nil)
	if err == nil {
		t.Fatal("buildStickyGroups without DIRECT should fail")
	}
	groups, err = buildStickyGroups(plans, map[string]C.Proxy{"DIRECT": direct}, newAffinityTable(nil, 0), newHealthTable(), nil)
	if err != nil {
		t.Fatalf("buildStickyGroups with builtin DIRECT: %v", err)
	}
	if groups[plans[0].group] == nil {
		t.Fatal("DIRECT route did not produce a sticky group")
	}
}
func TestWarmLeavesExcludeDirectAndProbeThreeChainsOnceEach(t *testing.T) {
	proxies := map[string]C.Proxy{"DIRECT": testWarmProxy{name: "DIRECT", typ: C.Direct}}
	for node := 0; node < 3; node++ {
		base := memberProxyName(0, 0, node)
		proxies[base] = testWarmProxy{name: base, typ: C.Socks5}
		pre, post := base+"-pre", base+"-post"
		proxies[pre] = testWarmProxy{name: pre, typ: C.Socks5}
		proxies[post] = testWarmProxy{name: post, typ: C.Socks5}
	}
	got := warmLeaves(proxies, func(name string) bool { return name == "DIRECT" || generatedMemberName(name) })
	if len(got) != 3 {
		t.Fatalf("warm leaves = %d, want three outer route proxies", len(got))
	}
	for _, proxy := range got {
		if !isRoutePostName(proxy.Name()) {
			t.Errorf("probe target %q is not outermost route member", proxy.Name())
		}
	}
}
