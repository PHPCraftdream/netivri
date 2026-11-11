package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

func TestProbeGroupIgnoresHopPoolDialErrors(t *testing.T) {
	const name = "netivri-g3-r0-0"
	key := healthMemberKey("r0-0", "", "")
	health := newHealthTable()
	health.registerMembers([]healthMember{{key: key, label: "r0-0"}})
	w := &warmer{
		options: WarmOptions{
			ProbeTimeout: time.Second,
			ProbeURL:     "http://probe.invalid/",
			onProbeGroup: probeGroupHealthHook(map[string]string{name: key}, health),
		},
		clock:   testWarmClock{now: time.Unix(100, 0)},
		proxies: []C.Proxy{testWarmProxy{name: name, typ: C.Socks5}},
	}
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		return 0, &hopPoolDialError{cause: errors.New("failed hop")}
	}
	probe := func() {
		w.probeGroup(context.Background(), []scheduled{{proxy: w.proxies[0], index: 0}})
	}
	probe()
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		return 0, fmt.Errorf("wrapped outbound: %s", (&hopPoolDialError{}).Error())
	}
	probe()
	if got := health.health(key).consProbeFail; got != 0 || health.downKey(key) {
		t.Fatalf("hop-pool probe errors changed node health: failures=%d down=%v", got, health.downKey(key))
	}
	w.probe = func(context.Context, C.Proxy, string) (uint16, error) {
		return 0, errors.New("genuine node failure")
	}
	probe()
	probe()
	if got := health.health(key).consProbeFail; got != 2 || !health.downKey(key) {
		t.Fatalf("genuine node probe errors: failures=%d down=%v, want 2/true", got, health.downKey(key))
	}
}

func TestGenerationProxyNamesIncludeRawGroupsAndDirectPostPool(t *testing.T) {
	o := Options{
		AccountRoute: map[string]string{"alice": "a-direct", "bob": "z-node"},
		RoutePostPool: map[string][]string{"a-direct": {
			"socks5://first.invalid:1080",
			"http://second.invalid:8080",
		}},
		RouteNodes: map[string][]map[string]any{"z-node": {socks5Node()}},
	}
	proxies, groups, rawRules, err := prepareRoutesGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := buildRoutePlansGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseMembers(proxies)
	if err != nil {
		t.Fatal(err)
	}
	names := generationProxyNames(plans, proxies, groups)
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	parsedRules, err := parseRouteRules(rawRules, func(name string) bool { return known[name] || name == "DIRECT" })
	if err != nil {
		t.Fatalf("route rules rejected generated raw group targets: %v", err)
	}
	const postRule = "IN-USER,alice,netivri-g3-r0-0-post"
	if len(rawRules) == 0 || rawRules[0] != postRule {
		t.Fatalf("first raw route rule = %v, want literal %q", rawRules, postRule)
	}
	if len(parsedRules) == 0 || parsedRules[0].Adapter() != "netivri-g3-r0-0-post" {
		t.Fatalf("first parsed route rule target = %v, want literal %q", parsedRules, "netivri-g3-r0-0-post")
	}
	if !known["netivri-g3-r0-0-post"] {
		t.Fatalf("raw direct-post group %q missing from generated names %v", "netivri-g3-r0-0-post", names)
	}
	lifecycle := newHopPoolLifecycle(nil)
	updated, _, err := lifecycle.installGen(o, plans, parsed, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if installed := updated["netivri-g3-r0-0-post"]; installed == nil || installed.Name() != "netivri-g3-r0-0-post" {
		t.Fatalf("generation direct-post adapter %q was not installed", "netivri-g3-r0-0-post")
	}
}

func TestPooledPostCarrierHealthIsIndependentFromFullRouteHealth(t *testing.T) {
	cases := []struct {
		name string
		o    Options
	}{
		{
			name: "multi-pre-scalar-post",
			o: Options{
				AccountRoute: map[string]string{"alice": "route"},
				RouteNodes:   map[string][]map[string]any{"route": {socks5Node()}},
				NodePrePool:  map[string][][]string{"route": {{"socks5://pre-a.invalid:1080", "socks5://pre-b.invalid:1080"}}},
				NodePost:     map[string][]string{"route": {"http://post.invalid:8080"}},
			},
		},
		{
			name: "multi-post-scalar-pre",
			o: Options{
				AccountRoute: map[string]string{"alice": "route"},
				RouteNodes:   map[string][]map[string]any{"route": {socks5Node()}},
				NodePre:      map[string][]string{"route": {"socks5://pre.invalid:1080"}},
				NodePostPool: map[string][][]string{"route": {{"http://post-a.invalid:8080", "http://post-b.invalid:8080"}}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := buildRoutePlansGen(tc.o, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != 1 || len(plans[0].healthKeys) != 1 || len(plans[0].carrierHealthKeys) != 1 {
				t.Fatalf("unexpected plan health keys: %#v", plans)
			}
			plan := plans[0]
			fullKey, carrierKey := plan.healthKeys[0], plan.carrierHealthKeys[0]
			if fullKey == carrierKey {
				t.Fatal("pooled post route reused its full-route health key for the carrier")
			}
			members := healthMembersForPlans(plans)
			if len(members) != 2 || members[0].key != fullKey || members[1].key != carrierKey || members[1].hops.post.addr != "" {
				t.Fatalf("health registration did not separate full route and carrier: %#v", members)
			}
			health := newHealthTable()
			now := time.Unix(1000, 0)
			health.now = func() time.Time { return now }
			health.registerMembers(members)
			nameKeys := memberNameHealthKeys(plans)
			baseName := strings.TrimSuffix(plan.names[0], "-post")
			if nameKeys[baseName] != carrierKey || nameKeys[plan.names[0]] != fullKey {
				t.Fatalf("probe names do not map to carrier/full keys: base=%q route=%q", nameKeys[baseName], nameKeys[plan.names[0]])
			}
			hook := probeGroupHealthHook(nameKeys, health)
			for _, fullFirst := range []bool{false, true} {
				health = newHealthTable()
				health.now = func() time.Time { return now }
				health.registerMembers(members)
				hook = probeGroupHealthHook(nameKeys, health)
				fullFailure := func() {
					now = now.Add(time.Second)
					hook([]string{plan.names[0]}, false, 0, errors.New("node dial failed"))
				}
				carrierSuccess := func() {
					now = now.Add(time.Second)
					hook([]string{baseName}, true, time.Millisecond, nil)
				}
				for range 3 {
					if fullFirst {
						fullFailure()
						carrierSuccess()
					} else {
						carrierSuccess()
						fullFailure()
					}
				}
				if !health.downKey(fullKey) || health.downKey(carrierKey) || health.health(carrierKey).lastOK.IsZero() {
					t.Fatalf("full/carrier state mixed (full-first=%v): full down=%v carrier down=%v carrier lastOK=%v", fullFirst, health.downKey(fullKey), health.downKey(carrierKey), health.health(carrierKey).lastOK)
				}
				if health.hops[carrierKey].post.addr != "" {
					t.Fatalf("carrier hop metadata retained post hop: %#v", health.hops[carrierKey])
				}
			}
		})
	}
}

func TestScalarPostKeepsLegacyHealthKey(t *testing.T) {
	o := Options{
		AccountRoute: map[string]string{"alice": "route"},
		RouteNodes:   map[string][]map[string]any{"route": {socks5Node()}},
		NodePre:      map[string][]string{"route": {"socks5://pre.invalid:1080"}},
		NodePost:     map[string][]string{"route": {"http://post.invalid:8080"}},
	}
	plans, err := buildRoutePlansGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].carrierHealthKeys) != 1 || plans[0].healthKeys[0] != plans[0].carrierHealthKeys[0] {
		t.Fatalf("ordinary scalar route split health identity: %#v", plans)
	}
	members := healthMembersForPlans(plans)
	if len(members) != 1 {
		t.Fatalf("ordinary scalar route registered %d health records, want one", len(members))
	}
	if got := memberNameHealthKeys(plans); got[plans[0].names[0]] != plans[0].healthKeys[0] || got["netivri-g3-r0-0"] != plans[0].healthKeys[0] {
		t.Fatalf("ordinary scalar probe names changed health mapping: %v", got)
	}
}
