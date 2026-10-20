package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func testCountryNode(id, server string, port int) map[string]any {
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
				Countries: map[string][]map[string]any{
					"DE": {testCountryNode("de-id", "de.example", 1080)},
				},
				AccountCountry: map[string]string{"alice": "DE"},
			},
			wantProxy:  []string{"netivri-DE-0"},
			wantGroups: 1,
			wantRules:  []string{"IN-USER,alice,netivri-group-DE", "MATCH,DIRECT"},
		},
		{
			name: "account default node",
			options: Options{
				Node: socks5Node(),
				Countries: map[string][]map[string]any{
					"DE": {testCountryNode("de-id", "de.example", 1080)},
				},
				AccountCountry: map[string]string{"alice": "DE"},
			},
			wantProxy:  []string{nodeName, "netivri-DE-0"},
			wantGroups: 1,
			wantRules:  []string{"IN-USER,alice,netivri-group-DE", "MATCH,netivri-node"},
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

func TestPrepareRoutesPreservesProvidedCountryOrder(t *testing.T) {
	first := testCountryNode("z-id", "first.example", 1080)
	first["name"] = "provider,comma"
	second := testCountryNode("a-id", "second.example", 1081)
	unused := testCountryNode("unused", "unused.example", 1082)
	countryNodes := []map[string]any{first, second}
	options := Options{
		Node: socks5Node(),
		Countries: map[string][]map[string]any{
			"DE": countryNodes,
			"FI": {unused},
		},
		AccountCountry: map[string]string{"alice": "DE"},
	}
	beforeFirst := cloneMapping(first)
	beforeSecond := cloneMapping(second)
	beforeOrder := append([]map[string]any(nil), countryNodes...)

	proxies, groups, rules, err := prepareRoutes(options)
	if err != nil {
		t.Fatal(err)
	}
	wantRules := []string{"IN-USER,alice,netivri-group-DE", "MATCH,netivri-node"}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
	wantMembers := []string{"netivri-DE-0", "netivri-DE-1"}
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
	if proxies[1]["name"] != "netivri-DE-0" || proxies[2]["name"] != "netivri-DE-1" {
		t.Fatalf("internal names = %v, %v", proxies[1]["name"], proxies[2]["name"])
	}
	if !reflect.DeepEqual(first, beforeFirst) || !reflect.DeepEqual(second, beforeSecond) {
		t.Fatal("provider node mappings were mutated")
	}
	if !reflect.DeepEqual(countryNodes, beforeOrder) || options.Countries["FI"][0]["server"] != "unused.example" {
		t.Fatal("provider country slices were mutated or unused country changed")
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
	if options.Countries["DE"][0]["server"] != "first.example" ||
		options.Countries["DE"][0]["name"] != "provider,comma" {
		t.Fatal("prepared output mutation reached caller's input map")
	}
	if !reflect.DeepEqual(options.AccountCountry, map[string]string{"alice": "DE"}) {
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

func TestPrepareRoutesOrdersCountriesAndUsers(t *testing.T) {
	options := Options{
		Countries: map[string][]map[string]any{
			"DE": {testCountryNode("de", "de.example", 1080)},
			"NL": {testCountryNode("nl", "nl.example", 1081)},
			"FI": {testCountryNode("fi", "fi.example", 1082)},
		},
		AccountCountry: map[string]string{
			"z-user": "NL",
			"alice":  "DE",
			"bob":    "FI",
		},
	}
	_, groups, rules, err := prepareRoutes(options)
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []string{"netivri-group-DE", "netivri-group-FI", "netivri-group-NL"}
	for i, group := range groups {
		if group["name"] != wantGroups[i] {
			t.Fatalf("group %d name = %v, want %s", i, group["name"], wantGroups[i])
		}
	}
	wantRules := []string{
		"IN-USER,alice,netivri-group-DE",
		"IN-USER,bob,netivri-group-FI",
		"IN-USER,z-user,netivri-group-NL",
		"MATCH,DIRECT",
	}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
}

func TestPrepareRoutesRejectsInvalidLoginAndCountry(t *testing.T) {
	valid64 := strings.Repeat("a", 64)
	for _, test := range []struct {
		name       string
		login      string
		country    string
		validLogin bool
		valid      bool
	}{
		{name: "valid 64", login: valid64, country: "DE", validLogin: true, valid: true},
		{name: "valid punctuation", login: "A-z._@9", country: "DE", validLogin: true, valid: true},
		{name: "a", login: "a", country: "DE", validLogin: true, valid: true},
		{name: "comma", login: "a,b", country: "DE"},
		{name: "slash", login: "a/b", country: "DE"},
		{name: "space", login: "a b", country: "DE"},
		{name: "empty login", login: "", country: "DE"},
		{name: "65 characters", login: strings.Repeat("a", 65), country: "DE"},
		{name: "non ASCII login", login: "ál", country: "DE"},
		{name: "invalid ASCII country", login: "alice", country: "D1", validLogin: true},
		{name: "lowercase country", login: "alice", country: "de", validLogin: true},
		{name: "empty country", login: "alice", country: "", validLogin: true},
		{name: "short country", login: "alice", country: "D", validLogin: true},
		{name: "long country", login: "alice", country: "DEU", validLogin: true},
		{name: "non ASCII country", login: "alice", country: "ÉU", validLogin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := Options{
				AccountCountry: map[string]string{test.login: test.country},
			}
			if test.validLogin {
				options.Countries = map[string][]map[string]any{
					"DE": {testCountryNode("de", "de.example", 1080)},
				}
			}
			_, _, _, err := prepareRoutes(options)
			var cliErr *cli.Error
			if test.valid {
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
			if !test.validLogin && strings.Contains(err.Error(), test.login) && test.login != "" {
				t.Fatalf("error leaked invalid login %q", test.login)
			}
		})
	}
}

func TestPrepareRoutesMissingCountryNodes(t *testing.T) {
	for _, nodes := range [][]map[string]any{nil, {}} {
		_, _, _, err := prepareRoutes(Options{
			Countries:      map[string][]map[string]any{"DE": nodes},
			AccountCountry: map[string]string{"alice": "DE"},
		})
		var cliErr *cli.Error
		if !errors.As(err, &cliErr) || cliErr.Code != "NO_NODES_FOR_COUNTRY" || cliErr.Exit != cli.ExitNotFound {
			t.Fatalf("error = %v, want NO_NODES_FOR_COUNTRY/ExitNotFound", err)
		}
	}
}

func TestPrepareRoutesRejectsPseudoNodeType(t *testing.T) {
	_, _, _, err := prepareRoutes(Options{
		Countries: map[string][]map[string]any{
			"DE": {{"type": "direct", "server": "127.0.0.1", "port": 1080}},
		},
		AccountCountry: map[string]string{"alice": "DE"},
	})
	var cliErr *cli.Error
	if !errors.As(err, &cliErr) || cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
		t.Fatalf("error = %v, want NODE_INVALID/ExitUsage", err)
	}
}
