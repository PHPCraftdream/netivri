package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func testRouteNode(id, server string, port int) map[string]any {
	return map[string]any{
		"id":     id,
		"type":   "socks5",
		"server": server,
		"port":   port,
	}
}

func TestPrepareRoutesLegacyAndAccountDefaults(t *testing.T) {
	tests := []struct {
		name       string
		options    Options
		wantProxy  []string
		wantGroups int
		wantRules  []string
	}{
		{
			name:      "direct legacy",
			wantRules: nil,
		},
		{
			name:      "node legacy",
			options:   Options{Node: socks5Node()},
			wantProxy: []string{nodeName},
			wantRules: []string{"MATCH,netivri-node"},
		},
		{
			name: "account default direct",
			options: Options{
				RouteNodes: map[string][]map[string]any{
					"de": {testRouteNode("de-id", "de.example", 1080)},
				},
				AccountRoute: map[string]string{"alice": "de"},
			},
			wantProxy:  []string{"netivri-r0-0"},
			wantGroups: 1,
			wantRules:  []string{"IN-USER,alice,netivri-group-0", "MATCH,DIRECT"},
		},
		{
			name: "account default node",
			options: Options{
				Node: socks5Node(),
				RouteNodes: map[string][]map[string]any{
					"de": {testRouteNode("de-id", "de.example", 1080)},
				},
				AccountRoute: map[string]string{"alice": "de"},
			},
			wantProxy:  []string{nodeName, "netivri-r0-0"},
			wantGroups: 1,
			wantRules:  []string{"IN-USER,alice,netivri-group-0", "MATCH,netivri-node"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxies, groups, rules, err := prepareRoutes(test.options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rules, test.wantRules) {
				t.Fatalf("rules = %v, want %v", rules, test.wantRules)
			}
			if len(groups) != test.wantGroups {
				t.Fatalf("group count = %d, want %d", len(groups), test.wantGroups)
			}
			var gotNames []string
			for _, proxy := range proxies {
				name, _ := proxy["name"].(string)
				gotNames = append(gotNames, name)
			}
			if !reflect.DeepEqual(gotNames, test.wantProxy) {
				t.Fatalf("proxy names = %v, want %v", gotNames, test.wantProxy)
			}
		})
	}
}

func TestPrepareRoutesPreservesProvidedRouteOrder(t *testing.T) {
	first := testRouteNode("z-id", "first.example", 1080)
	first["name"] = "provider,comma"
	second := testRouteNode("a-id", "second.example", 1081)
	unused := testRouteNode("unused", "unused.example", 1082)
	routeNodes := []map[string]any{first, second}
	options := Options{
		Node: socks5Node(),
		RouteNodes: map[string][]map[string]any{
			"de": routeNodes,
			"fi": {unused},
		},
		AccountRoute: map[string]string{"alice": "de"},
	}
	beforeFirst := cloneMapping(first)
	beforeSecond := cloneMapping(second)
	beforeOrder := append([]map[string]any(nil), routeNodes...)

	proxies, groups, rules, err := prepareRoutes(options)
	if err != nil {
		t.Fatal(err)
	}
	wantRules := []string{"IN-USER,alice,netivri-group-0", "MATCH,netivri-node"}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
	wantMembers := []string{"netivri-r0-0", "netivri-r0-1"}
	if got := groups[0]["proxies"]; !reflect.DeepEqual(got, wantMembers) {
		t.Fatalf("group members = %v, want %v", got, wantMembers)
	}
	if groups[0]["default-selected"] != nil {
		t.Fatalf("group sets an unrequested default-selected: %v", groups[0])
	}
	if proxies[0]["name"] != nodeName {
		t.Fatalf("default proxy name = %v, want %s", proxies[0]["name"], nodeName)
	}
	if proxies[1]["server"] != "first.example" || proxies[2]["server"] != "second.example" {
		t.Fatalf("proxy order was not preserved: %v", proxies)
	}
	if proxies[1]["name"] != "netivri-r0-0" || proxies[2]["name"] != "netivri-r0-1" {
		t.Fatalf("internal names = %v, %v", proxies[1]["name"], proxies[2]["name"])
	}
	if !reflect.DeepEqual(first, beforeFirst) || !reflect.DeepEqual(second, beforeSecond) {
		t.Fatal("provider node mappings were mutated")
	}
	if !reflect.DeepEqual(routeNodes, beforeOrder) || options.RouteNodes["fi"][0]["server"] != "unused.example" {
		t.Fatal("provider route slices were mutated or unused route changed")
	}

	proxies[1]["server"] = "changed.example"
	proxies[1]["name"] = "changed-proxy"
	groups[0]["name"] = "changed-group"
	rules[0] = "changed-rule"
	if first["server"] != "first.example" || first["name"] != "provider,comma" {
		t.Fatal("mutating prepared proxy affected source mapping")
	}
	if groups[0]["name"] != "changed-group" || rules[0] != "changed-rule" {
		t.Fatal("prepared outputs did not accept independent mutation")
	}
	if options.RouteNodes["de"][0]["server"] != "first.example" ||
		options.RouteNodes["de"][0]["name"] != "provider,comma" {
		t.Fatal("prepared output mutation reached caller's input map")
	}
	if !reflect.DeepEqual(options.AccountRoute, map[string]string{"alice": "de"}) {
		t.Fatal("routing construction mutated the account map")
	}
}

func cloneMapping(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func TestPrepareRoutesOrdersKeysAndUsers(t *testing.T) {
	options := Options{
		RouteNodes: map[string][]map[string]any{
			"de": {testRouteNode("de", "de.example", 1080)},
			"nl": {testRouteNode("nl", "nl.example", 1081)},
			"fi": {testRouteNode("fi", "fi.example", 1082)},
		},
		AccountRoute: map[string]string{
			"z-user": "nl",
			"alice":  "de",
			"bob":    "fi",
		},
	}
	proxies, groups, rules, err := prepareRoutes(options)
	if err != nil {
		t.Fatal(err)
	}
	// Keys are indexed in sorted order: de = 0, fi = 1, nl = 2.
	wantGroups := []string{"netivri-group-0", "netivri-group-1", "netivri-group-2"}
	for i, group := range groups {
		if group["name"] != wantGroups[i] {
			t.Fatalf("group %d name = %v, want %s", i, group["name"], wantGroups[i])
		}
	}
	wantRules := []string{
		"IN-USER,alice,netivri-group-0",
		"IN-USER,bob,netivri-group-1",
		"IN-USER,z-user,netivri-group-2",
		"MATCH,DIRECT",
	}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
	var gotNames []string
	for _, proxy := range proxies {
		name, _ := proxy["name"].(string)
		gotNames = append(gotNames, name)
	}
	wantNames := []string{"netivri-r0-0", "netivri-r1-0", "netivri-r2-0"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("proxy names = %v, want %v", gotNames, wantNames)
	}
}

func TestPrepareRoutesSharedAndDistinctRouteKeys(t *testing.T) {
	t.Run("same key shares one group", func(t *testing.T) {
		options := Options{
			RouteNodes: map[string][]map[string]any{
				"de": {testRouteNode("de", "de.example", 1080)},
			},
			AccountRoute: map[string]string{"alice": "de", "bob": "de"},
		}
		proxies, groups, rules, err := prepareRoutes(options)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 1 {
			t.Fatalf("group count = %d, want 1", len(groups))
		}
		wantRules := []string{
			"IN-USER,alice,netivri-group-0",
			"IN-USER,bob,netivri-group-0",
			"MATCH,DIRECT",
		}
		if !reflect.DeepEqual(rules, wantRules) {
			t.Fatalf("rules = %v, want %v", rules, wantRules)
		}
		if len(proxies) != 1 {
			t.Fatalf("proxy count = %d, want 1", len(proxies))
		}
	})
	t.Run("two keys give two groups", func(t *testing.T) {
		options := Options{
			RouteNodes: map[string][]map[string]any{
				"de": {testRouteNode("de", "de.example", 1080)},
				"nl": {testRouteNode("nl", "nl.example", 1081)},
			},
			AccountRoute: map[string]string{"alice": "de", "bob": "nl"},
		}
		_, groups, _, err := prepareRoutes(options)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 2 {
			t.Fatalf("group count = %d, want 2", len(groups))
		}
		seen := map[string]bool{}
		for i, group := range groups {
			members, _ := group["proxies"].([]string)
			if len(members) != 1 {
				t.Fatalf("group %d members = %v, want one member", i, group["proxies"])
			}
			if seen[members[0]] {
				t.Fatalf("groups share a member name: %v", members[0])
			}
			seen[members[0]] = true
		}
	})
}

func TestPrepareRoutesKeyNeverReachesOutput(t *testing.T) {
	weird := `prov~DE "x", y / z`
	options := Options{
		Node: socks5Node(),
		RouteNodes: map[string][]map[string]any{
			weird: {
				testRouteNode("a", "first.example", 1080),
				testRouteNode("b", "second.example", 1081),
			},
		},
		AccountRoute: map[string]string{"alice": weird},
	}
	proxies, groups, rules, err := prepareRoutes(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if strings.Contains(rule, weird) {
			t.Fatalf("rule contains the route key: %q", rule)
		}
	}
	for _, proxy := range proxies {
		name, _ := proxy["name"].(string)
		if strings.Contains(name, weird) {
			t.Fatalf("proxy name contains the route key: %q", name)
		}
	}
	if len(groups) != 1 {
		t.Fatalf("group count = %d, want 1", len(groups))
	}
	if groups[0]["name"] != "netivri-group-0" {
		t.Fatalf("group name = %v, want netivri-group-0", groups[0]["name"])
	}
	wantMembers := []string{"netivri-r0-0", "netivri-r0-1"}
	if got := groups[0]["proxies"]; !reflect.DeepEqual(got, wantMembers) {
		t.Fatalf("group members = %v, want %v", got, wantMembers)
	}
	if !reflect.DeepEqual(rules, []string{"IN-USER,alice,netivri-group-0", "MATCH,netivri-node"}) {
		t.Fatalf("rules = %v", rules)
	}
}

func TestPrepareRoutesRejectsInvalidLogin(t *testing.T) {
	valid64 := strings.Repeat("a", 64)
	for _, test := range []struct {
		name       string
		login      string
		validLogin bool
	}{
		{name: "valid 64", login: valid64, validLogin: true},
		{name: "valid punctuation", login: "A-z._@9", validLogin: true},
		{name: "a", login: "a", validLogin: true},
		{name: "comma", login: "a,b"},
		{name: "slash", login: "a/b"},
		{name: "space", login: "a b"},
		{name: "empty login", login: ""},
		{name: "65 characters", login: strings.Repeat("a", 65)},
		{name: "non ASCII login", login: "ál"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := Options{
				AccountRoute: map[string]string{test.login: "de"},
				RouteNodes: map[string][]map[string]any{
					"de": {testRouteNode("de", "de.example", 1080)},
				},
			}
			_, _, _, err := prepareRoutes(options)
			var cliErr *cli.Error
			if test.validLogin {
				if err != nil {
					t.Fatalf("valid account rejected: %v", err)
				}
				return
			}
			if !errors.As(err, &cliErr) {
				t.Fatalf("error = %v, want *cli.Error", err)
			}
			if cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
				t.Fatalf("error = %#v, want NODE_INVALID/ExitUsage", cliErr)
			}
			if strings.Contains(err.Error(), test.login) && test.login != "" {
				t.Fatalf("error leaked invalid login %q", test.login)
			}
		})
	}
}

func TestPrepareRoutesRejectsInvalidRouteKeys(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "empty key", key: ""},
		{name: "129 bytes", key: strings.Repeat("a", 129)},
		{name: "control character", key: "de\x01"},
		{name: "newline", key: "de\n"},
		{name: "non ASCII", key: "dé"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := Options{
				AccountRoute: map[string]string{"alice": test.key},
				RouteNodes: map[string][]map[string]any{
					"de": {testRouteNode("de", "de.example", 1080)},
				},
			}
			_, _, _, err := prepareRoutes(options)
			var cliErr *cli.Error
			if !errors.As(err, &cliErr) {
				t.Fatalf("error = %v, want *cli.Error", err)
			}
			if cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
				t.Fatalf("error = %#v, want NODE_INVALID/ExitUsage", cliErr)
			}
			if strings.Contains(err.Error(), test.key) && test.key != "" {
				t.Fatalf("error leaked invalid route key %q", test.key)
			}
		})
	}
	t.Run("valid boundary keys", func(t *testing.T) {
		for _, key := range []string{"de", strings.Repeat("a", 128), `~ /,"'`} {
			options := Options{
				AccountRoute: map[string]string{"alice": key},
				RouteNodes: map[string][]map[string]any{
					key: {testRouteNode("de", "de.example", 1080)},
				},
			}
			if _, _, _, err := prepareRoutes(options); err != nil {
				t.Fatalf("key %q rejected: %v", key, err)
			}
		}
	})
}

func TestPrepareRoutesMissingRouteNodes(t *testing.T) {
	for _, nodes := range [][]map[string]any{nil, {}} {
		_, _, _, err := prepareRoutes(Options{
			RouteNodes:   map[string][]map[string]any{"de": nodes},
			AccountRoute: map[string]string{"alice": "de"},
		})
		var cliErr *cli.Error
		if !errors.As(err, &cliErr) || cliErr.Code != "NO_NODES_FOR_COUNTRY" || cliErr.Exit != cli.ExitNotFound {
			t.Fatalf("error = %v, want NO_NODES_FOR_COUNTRY/ExitNotFound", err)
		}
	}
	t.Run("absent route entry", func(t *testing.T) {
		_, _, _, err := prepareRoutes(Options{
			RouteNodes:   map[string][]map[string]any{"nl": {testRouteNode("nl", "nl.example", 1081)}},
			AccountRoute: map[string]string{"alice": "de"},
		})
		var cliErr *cli.Error
		if !errors.As(err, &cliErr) || cliErr.Code != "NO_NODES_FOR_COUNTRY" || cliErr.Exit != cli.ExitNotFound {
			t.Fatalf("error = %v, want NO_NODES_FOR_COUNTRY/ExitNotFound", err)
		}
	})
}

func TestPrepareRoutesRejectsPseudoNodeType(t *testing.T) {
	_, _, _, err := prepareRoutes(Options{
		RouteNodes: map[string][]map[string]any{
			"de": {{"type": "direct", "server": "127.0.0.1", "port": 1080}},
		},
		AccountRoute: map[string]string{"alice": "de"},
	})
	var cliErr *cli.Error
	if !errors.As(err, &cliErr) || cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
		t.Fatalf("error = %v, want NODE_INVALID/ExitUsage", err)
	}
}
