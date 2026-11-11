package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestFallbackHopPoolIdentityIgnoresUserinfo(t *testing.T) {
	first := hopPoolKey("http://first-user:first-secret@bad_host.invalid:8080")
	second := hopPoolKey("http://second-user:second-secret@bad_host.invalid:8080")
	if !strings.HasPrefix(first, "u:") || first != second {
		t.Fatal("fallback health identity must ignore credentials")
	}
	if len(first) != len("u:")+64 {
		t.Fatal("fallback health identity is not a SHA-256 key")
	}
}

func TestLegacyAndSingletonHopPoolsHaveIdenticalRoutes(t *testing.T) {
	pre := "socks5://pre-user:pre-secret@pre.example:1080"
	post := "https://post-user:post-secret@post.example:8443"
	base := Options{
		RouteNodes:   map[string][]map[string]any{"route-secret": {socks5Node(), {"type": "vless", "server": "node-two.invalid", "port": 443}}},
		AccountRoute: map[string]string{"alice": "route-secret"},
		RoutePre:     map[string]string{"route-secret": pre},
		RoutePost:    map[string]string{"route-secret": post},
	}
	for _, generation := range []int{0, 3} {
		t.Run("generation-"+string(rune('0'+generation)), func(t *testing.T) {
			wantProxies, wantGroups, wantRules, err := prepareRoutesGen(base, generation)
			if err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				name string
				edit func(*Options)
			}{
				{"route-pools", func(o *Options) {
					o.RoutePrePool = map[string][]string{"route-secret": {pre}}
					o.RoutePostPool = map[string][]string{"route-secret": {post}}
				}},
				{"node-pools", func(o *Options) {
					o.NodePrePool = map[string][][]string{"route-secret": {{pre}, {pre}}}
					o.NodePostPool = map[string][][]string{"route-secret": {{post}, {post}}}
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					o := base
					tc.edit(&o)
					gotProxies, gotGroups, gotRules, err := prepareRoutesGen(o, generation)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(gotProxies, wantProxies) || !reflect.DeepEqual(gotGroups, wantGroups) || !reflect.DeepEqual(gotRules, wantRules) {
						t.Fatal("pool output differs from scalar output")
					}
				})
			}
		})
	}
}

func TestPoolOverridesAndPerNodeProviderPools(t *testing.T) {
	legacy := "http://legacy.invalid:8080"
	routeHop := "http://route.invalid:8080"
	nodeHop := "http://provider-b.invalid:8080"
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node(), {"type": "vless", "server": "node-b.invalid", "port": 443}}},
		AccountRoute: map[string]string{"alice": "r"},
		RoutePre:     map[string]string{"r": legacy},
		RoutePrePool: map[string][]string{"r": {routeHop}},
		NodePrePool:  map[string][][]string{"r": {nil, {nodeHop}}},
	}
	if got := o.nodePrePool("r", 0); len(got) != 0 {
		t.Fatalf("explicit empty node pool failed to suppress scalar+route value: %v", got)
	}
	if got := o.nodePrePool("r", 1); !reflect.DeepEqual(got, []string{nodeHop}) {
		t.Fatalf("per-node provider pool = %v", got)
	}
	if got := o.nodePostPool("r", 0); !reflect.DeepEqual(got, []string(nil)) {
		t.Fatalf("empty inherited post pool = %v", got)
	}
	perNode := o
	perNode.NodePrePool = map[string][][]string{"r": {{"http://provider-a.invalid:8080"}, {nodeHop}}}
	mappings, _, _, err := prepareRoutesGen(perNode, 0)
	if err != nil {
		t.Fatal(err)
	}
	preServers := make(map[string]string)
	for _, mapping := range mappings {
		name, _ := mapping["name"].(string)
		if strings.HasSuffix(name, "-pre") {
			preServers[name] = mapping["server"].(string)
		}
	}
	if preServers[helperProxyName(0, 0, 0, "pre")] != "provider-a.invalid" || preServers[helperProxyName(0, 0, 1, "pre")] != "provider-b.invalid" {
		t.Fatalf("provider-specific pre mappings = %v", preServers)
	}
	delete(o.NodePrePool, "r")
	if got := o.nodePrePool("r", 0); !reflect.DeepEqual(got, []string{routeHop}) {
		t.Fatalf("route pool did not replace scalar: %v", got)
	}
	delete(o.RoutePrePool, "r")
	if got := o.nodePrePool("r", 0); !reflect.DeepEqual(got, []string{legacy}) {
		t.Fatalf("legacy scalar fallback = %v", got)
	}
}

func TestNodePoolLengthsAndNamesDoNotExposeSecrets(t *testing.T) {
	secretURI := "socks5://private-user:private-secret@safe.invalid:1080"
	base := Options{
		RouteNodes:   map[string][]map[string]any{"opaque-route": {socks5Node(), {"type": "vless", "server": "node.invalid", "port": 443}}},
		AccountRoute: map[string]string{"alice": "opaque-route"},
	}
	for _, tc := range []struct {
		name string
		edit func(*Options)
	}{
		{"pre length", func(o *Options) { o.NodePrePool = map[string][][]string{"opaque-route": {{secretURI}}} }},
		{"post length", func(o *Options) { o.NodePostPool = map[string][][]string{"opaque-route": {{secretURI}, {}, {}}} }},
		{"invalid secret URL", func(o *Options) {
			o.RoutePrePool = map[string][]string{"opaque-route": {"unsupported://private-user:private-secret@safe.invalid:1080"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.edit(&o)
			_, _, _, err := prepareRoutesGen(o, 3)
			if err == nil {
				t.Fatal("invalid pool configuration was accepted")
			}
			if strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "safe.invalid") {
				t.Fatalf("validation error exposed URL detail: %v", err)
			}
		})
	}

	o := base
	o.RoutePrePool = map[string][]string{"opaque-route": {secretURI, "http://another-user:other-secret@other.invalid:8080"}}
	_, _, _, err := prepareRoutesGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := buildRoutePlansGen(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	name := plans[0].prePools[0].name
	key := plans[0].prePools[0].keys[0]
	for _, forbidden := range []string{"private-user", "private-secret", "safe.invalid", "another-user", "other-secret", "other.invalid"} {
		if strings.Contains(name, forbidden) || strings.Contains(key, forbidden) {
			t.Fatalf("secret or URL identity leaked into name/key: %q / %q", name, key)
		}
	}
}

func TestOverlappingProviderPrePoolsKeepCredentialsSeparate(t *testing.T) {
	preA := "http://user-a:sentinel-a@a.invalid:8080"
	preBFirst := "http://first-b:sentinel-first@b.invalid:8080"
	preBSecond := "http://second-b:sentinel-second@b.invalid:8080"
	preC := "http://user-c:sentinel-c@c.invalid:8080"
	o := Options{
		RouteNodes: map[string][]map[string]any{"provider-routes": {
			{"type": "socks5", "server": "node-a.invalid", "port": 1080},
			{"type": "vless", "server": "node-b.invalid", "port": 443},
		}},
		AccountRoute: map[string]string{"alice": "provider-routes"},
		NodePrePool: map[string][][]string{"provider-routes": {
			{preA, preBFirst},
			{preBSecond, preC},
		}},
	}
	proxies, _, _, err := prepareRoutesGen(o, 0)
	if err != nil {
		t.Fatal("provider-specific pools failed to prepare")
	}
	seen := make(map[string]bool)
	var shared []map[string]any
	for _, proxy := range proxies {
		name, _ := proxy["name"].(string)
		if seen[name] {
			t.Fatal("duplicate proxy mapping name")
		}
		seen[name] = true
		if proxy["server"] == "b.invalid" {
			shared = append(shared, proxy)
		}
	}
	if len(shared) != 2 {
		t.Fatal("same health key with different credentials did not produce separate mappings")
	}
	credentials := map[string][2]string{}
	for _, mapping := range shared {
		username, _ := mapping["username"].(string)
		password, _ := mapping["password"].(string)
		credentials[username] = [2]string{username, password}
		name, _ := mapping["name"].(string)
		for _, forbidden := range []string{"first-b", "second-b", "sentinel-first", "sentinel-second", "b.invalid"} {
			if strings.Contains(name, forbidden) {
				t.Fatal("credential or endpoint text leaked into helper name")
			}
		}
	}
	if credentials["first-b"] != [2]string{"first-b", "sentinel-first"} || credentials["second-b"] != [2]string{"second-b", "sentinel-second"} {
		t.Fatal("provider-specific helper mappings did not preserve their own credentials")
	}
	first := newHopPoolSpec(preHopPool, []string{preA, preBFirst}, 0)
	second := newHopPoolSpec(preHopPool, []string{"http://user-a:sentinel-a@a.invalid:8080", preBSecond}, 0)
	if first.id != second.id || first.configID == second.configID || first.name == second.name {
		t.Fatal("health identity and config identity were not separated")
	}
	for _, forbidden := range []string{"user-a", "sentinel-a", "first-b", "sentinel-first", "second-b", "sentinel-second", "a.invalid", "b.invalid", "c.invalid"} {
		if strings.Contains(first.name+second.name, forbidden) {
			t.Fatal("pool config identity leaked credentials or endpoint text")
		}
	}
}

func TestLegacyScalarValidationOnlyWhenEffective(t *testing.T) {
	badScalar := "unsupported://legacy-user:legacy-sentinel@legacy.invalid:8080"
	o := Options{
		RouteNodes:   map[string][]map[string]any{"active": {socks5Node()}},
		AccountRoute: map[string]string{"alice": "active"},
		RoutePre:     map[string]string{"active": badScalar},
		RoutePost:    map[string]string{"unused": badScalar},
		NodePrePool:  map[string][][]string{"active": {nil}},
	}
	if _, _, _, err := prepareRoutesGen(o, 0); err != nil {
		t.Fatalf("unused legacy scalar rejected: %v", err)
	}
	delete(o.NodePrePool, "active")
	_, _, _, err := prepareRoutesGen(o, 0)
	if err == nil || strings.Contains(err.Error(), "legacy-user") || strings.Contains(err.Error(), "legacy-sentinel") || strings.Contains(err.Error(), "legacy.invalid") {
		t.Fatal("effective malformed scalar was not safely rejected")
	}
}

func TestHopPoolRouteGoldenSingletons(t *testing.T) {
	cases := []struct {
		name       string
		generation int
		proxyNames []string
		proxies    []map[string]any
		groups     []map[string]any
		rules      []string
	}{
		{
			name:       "generation-0",
			generation: 0,
			proxyNames: []string{"netivri-r0-0-pre", "netivri-r0-0-post", "netivri-r0-0"},
			proxies: []map[string]any{
				{"name": "netivri-r0-0-pre", "type": "socks5", "server": "pre.invalid", "port": "1080"},
				{"name": "netivri-r0-0-post", "type": "http", "server": "post.invalid", "port": "8080", "dialer-proxy": "netivri-r0-0"},
				{"name": "netivri-r0-0", "type": "socks5", "server": "127.0.0.1", "port": 1080, "dialer-proxy": "netivri-r0-0-pre"},
			},
			groups: []map[string]any{{"name": "netivri-group-0", "type": "select", "proxies": []string{"netivri-r0-0-post"}}},
			rules:  []string{"IN-USER,alice,netivri-group-0", "MATCH,DIRECT"},
		},
		{
			name:       "generation-3",
			generation: 3,
			proxyNames: []string{"netivri-g3-r0-0-pre", "netivri-g3-r0-0-post", "netivri-g3-r0-0"},
			proxies: []map[string]any{
				{"name": "netivri-g3-r0-0-pre", "type": "socks5", "server": "pre.invalid", "port": "1080"},
				{"name": "netivri-g3-r0-0-post", "type": "http", "server": "post.invalid", "port": "8080", "dialer-proxy": "netivri-g3-r0-0"},
				{"name": "netivri-g3-r0-0", "type": "socks5", "server": "127.0.0.1", "port": 1080, "dialer-proxy": "netivri-g3-r0-0-pre"},
			},
			groups: []map[string]any{{"name": "netivri-g3-group-0", "type": "select", "proxies": []string{"netivri-g3-r0-0-post"}}},
			rules:  []string{"IN-USER,alice,netivri-g3-group-0", "MATCH,DIRECT"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := Options{
				RouteNodes:   map[string][]map[string]any{"route": {socks5Node()}},
				AccountRoute: map[string]string{"alice": "route"},
				NodePrePool:  map[string][][]string{"route": {{"socks5://pre.invalid:1080"}}},
				NodePostPool: map[string][][]string{"route": {{"http://post.invalid:8080"}}},
			}
			proxies, groups, rules, err := prepareRoutesGen(options, tc.generation)
			if err != nil {
				t.Fatal(err)
			}
			gotNames := make([]string, len(proxies))
			for i, proxy := range proxies {
				gotNames[i], _ = proxy["name"].(string)
			}
			if !reflect.DeepEqual(gotNames, tc.proxyNames) {
				t.Fatalf("proxy names = %v, want %v", gotNames, tc.proxyNames)
			}
			if !reflect.DeepEqual(proxies, tc.proxies) {
				t.Fatalf("proxy mappings = %#v, want %#v", proxies, tc.proxies)
			}
			if !reflect.DeepEqual(groups, tc.groups) {
				t.Fatalf("groups = %#v, want %#v", groups, tc.groups)
			}
			if !reflect.DeepEqual(rules, tc.rules) {
				t.Fatalf("rules = %v, want %v", rules, tc.rules)
			}
		})
	}
}
