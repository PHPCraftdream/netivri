package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func routeNames(t *testing.T, pool routePool, key string) []string {
	t.Helper()
	var names []string
	for _, raw := range pool.nodes[key] {
		names = append(names, raw["name"].(string))
	}
	return names
}

// An account's providers are tried in the order listed: every node of the
// first one is a better tier than any node of the second.
func TestBuildAccountRoutePoolProviderListKeepsPriority(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"a": snap(node("a1", "DE"), node("a2", "NL")),
		"b": snap(node("b1", "DE")),
	}
	got, err := buildAccountRoutePool([]config.Account{{Login: "u", Provider: "b,a"}}, snapshots, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.accountRoutes["u"] != credentialRouteKeyHops("b,a", "", nil, nil) {
		t.Fatalf("route = %q", got.accountRoutes["u"])
	}
	if names := routeNames(t, got, credentialRouteKeyHops("b,a", "", nil, nil)); strings.Join(names, ",") != "b1,a1,a2" {
		t.Fatalf("order = %v, want [b1 a1 a2]", names)
	}
	if !reflect.DeepEqual(got.tiers[credentialRouteKeyHops("b,a", "", nil, nil)], []int{0, 1, 1}) {
		t.Fatalf("tiers = %v", got.tiers[credentialRouteKeyHops("b,a", "", nil, nil)])
	}
	if !reflect.DeepEqual(got.labels[credentialRouteKeyHops("b,a", "", nil, nil)], []string{"b/b1", "a/a1", "a/a2"}) {
		t.Fatalf("labels = %v", got.labels[credentialRouteKeyHops("b,a", "", nil, nil)])
	}
}

// Countries come first: all providers' nodes of the first country, then the
// second one.
func TestBuildAccountRoutePoolCountryListIsCountryMajor(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE"), node("m-nl", "NL")),
		"alt":  snap(node("a-de", "DE"), node("a-nl", "NL")),
	}
	got, err := buildAccountRoutePool([]config.Account{{Login: "u", ExitCountry: "nl,de"}}, snapshots, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.accountRoutes["u"] != credentialRouteKeyHops("", "NL,DE", nil, nil) {
		t.Fatalf("route = %q", got.accountRoutes["u"])
	}
	if names := routeNames(t, got, credentialRouteKeyHops("", "NL,DE", nil, nil)); strings.Join(names, ",") != "a-nl,m-nl,a-de,m-de" {
		t.Fatalf("order = %v, want [a-nl m-nl a-de m-de]", names)
	}
	if !reflect.DeepEqual(got.tiers[credentialRouteKeyHops("", "NL,DE", nil, nil)], []int{0, 0, 1, 1}) {
		t.Fatalf("tiers = %v", got.tiers[credentialRouteKeyHops("", "NL,DE", nil, nil)])
	}
}

// With both lists: the country decides first, then the provider inside it.
func TestBuildAccountRoutePoolBothListsCountryThenProvider(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"a": snap(node("a-de", "DE"), node("a-nl", "NL")),
		"b": snap(node("b-de", "DE"), node("b-nl", "NL"), node("b-fi", "FI")),
	}
	acct := config.Account{Login: "u", Provider: "b,a", ExitCountry: "NL,DE"}
	got, err := buildAccountRoutePool([]config.Account{acct}, snapshots, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := credentialRouteKeyHops("b,a", "NL,DE", nil, nil)
	if names := routeNames(t, got, key); strings.Join(names, ",") != "b-nl,a-nl,b-de,a-de" {
		t.Fatalf("order = %v, want [b-nl a-nl b-de a-de]", names)
	}
	if !reflect.DeepEqual(got.tiers[key], []int{0, 1, 2, 3}) {
		t.Fatalf("tiers = %v", got.tiers[key])
	}
}

// A (country, provider) pair without nodes is skipped and takes no tier; only
// a route without any node fails.
func TestBuildAccountRoutePoolSkipsEmptyPairsAndMissingSnapshots(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"a": snap(node("a-de", "DE")),
		"b": snap(node("b-nl", "NL")),
	}
	acct := config.Account{Login: "u", Provider: "ghost,a,b", ExitCountry: "NL,DE"}
	got, err := buildAccountRoutePool([]config.Account{acct}, snapshots, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := credentialRouteKeyHops("ghost,a,b", "NL,DE", nil, nil)
	if names := routeNames(t, got, key); strings.Join(names, ",") != "b-nl,a-de" {
		t.Fatalf("order = %v, want [b-nl a-de]", names)
	}
	if !reflect.DeepEqual(got.tiers[key], []int{0, 1}) {
		t.Fatalf("tiers = %v", got.tiers[key])
	}
}

// The route key is the canonical form, so spellings of one route share it.
func TestBuildAccountRoutePoolCanonicalKey(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"a": snap(node("a-de", "DE")),
		"b": snap(node("b-de", "DE")),
	}
	accounts := []config.Account{
		{Login: "u1", Provider: "a, b", ExitCountry: "de, DE"},
		{Login: "u2", Provider: "a,b,a", ExitCountry: "DE"},
	}
	got, err := buildAccountRoutePool(accounts, snapshots, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.accountRoutes["u1"] != credentialRouteKeyHops("a,b", "DE", nil, nil) || got.accountRoutes["u2"] != credentialRouteKeyHops("a,b", "DE", nil, nil) {
		t.Fatalf("routes = %v", got.accountRoutes)
	}
	if len(got.nodes) != 1 || len(got.nodes[credentialRouteKeyHops("a,b", "DE", nil, nil)]) != 2 {
		t.Fatalf("nodes = %v", got.nodes)
	}
}

func TestBuildAccountRoutePoolListsFailClosed(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{"a": snap(node("a-de", "DE"))}
	cases := []struct {
		name    string
		account config.Account
		message string
	}{
		{"countries", config.Account{Login: "u", ExitCountry: "FI,SE"}, "no nodes available for countries FI,SE"},
		{"providers", config.Account{Login: "u", Provider: "x,y"}, `no nodes available for providers "x,y"`},
		{"both", config.Account{Login: "u", Provider: "a,x", ExitCountry: "FI,SE"}, `no nodes available for countries FI,SE of providers "a,x"`},
		{"one of each", config.Account{Login: "u", Provider: "a", ExitCountry: "FI"}, `no nodes available for country FI of provider "a"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAccountRoutePool([]config.Account{tc.account}, snapshots, nil, nil)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound {
				t.Fatalf("error = %#v", err)
			}
			if ce.Message != tc.message {
				t.Fatalf("message = %q, want %q", ce.Message, tc.message)
			}
		})
	}
}

func TestSnapshotDemandWithLists(t *testing.T) {
	all, named := snapshotDemand([]config.Account{
		{Login: "u1", Provider: "a, b", ExitCountry: "DE"},
		{Login: "u2", Provider: "c"},
	})
	if all || !reflect.DeepEqual(named, map[string]bool{"a": true, "b": true, "c": true}) {
		t.Fatalf("all=%v named=%v", all, named)
	}
	if all, _ := snapshotDemand([]config.Account{{Login: "u", ExitCountry: "DE,NL"}}); !all {
		t.Fatal("a country list without providers must read every snapshot")
	}
}

func TestAccountStatusShowsLists(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "u", Provider: "a,b", ExitCountry: "DE,NL"}}}
	got := accountStatus(cfg, map[string]string{"u": "a,b~DE,NL"})
	if len(got) != 1 || got[0].Provider != "a,b" || got[0].ExitCountry != "DE,NL" {
		t.Fatalf("got %#v", got)
	}
}

// "*" is every country the list does not name, at its place in the order,
// nodes of an unknown country included.
func TestBuildAccountRoutePoolAnyCountry(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"a": snap(node("a-de", "DE"), node("a-fr", "FR"), node("a-nl", "NL"), node("a-unknown", "")),
		"b": snap(node("b-fr", "FR"), node("b-nl", "NL")),
	}
	for _, tc := range []struct {
		name      string
		account   config.Account
		key       string
		wantNames string
		wantTiers []int
	}{
		{"last", config.Account{Login: "u", Provider: "a,b", ExitCountry: "NL,*"}, credentialRouteKeyHops("a,b", "NL,*", nil, nil), "a-nl,b-nl,a-de,a-fr,a-unknown,b-fr", []int{0, 1, 2, 2, 2, 3}},
		{"first", config.Account{Login: "u", Provider: "a", ExitCountry: "*,DE"}, credentialRouteKeyHops("a", "*,DE", nil, nil), "a-fr,a-nl,a-unknown,a-de", []int{0, 0, 0, 1}},
		{"alone with providers", config.Account{Login: "u", Provider: "b,a", ExitCountry: "*"}, credentialRouteKeyHops("b,a", "*", nil, nil), "b-fr,b-nl,a-de,a-fr,a-nl,a-unknown", []int{0, 0, 1, 1, 1, 1}},
		{"alone without providers", config.Account{Login: "u", ExitCountry: "*"}, credentialRouteKeyHops("", "*", nil, nil), "a-de,a-fr,a-nl,a-unknown,b-fr,b-nl", []int{0, 0, 0, 0, 0, 0}},
		{"nothing left for it", config.Account{Login: "u", Provider: "b", ExitCountry: "FR,NL,*"}, credentialRouteKeyHops("b", "FR,NL,*", nil, nil), "b-fr,b-nl", []int{0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildAccountRoutePool([]config.Account{tc.account}, snapshots, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.accountRoutes["u"] != tc.key {
				t.Fatalf("route = %q, want %q", got.accountRoutes["u"], tc.key)
			}
			if names := strings.Join(routeNames(t, got, tc.key), ","); names != tc.wantNames {
				t.Fatalf("order = %s, want %s", names, tc.wantNames)
			}
			if !reflect.DeepEqual(got.tiers[tc.key], tc.wantTiers) {
				t.Fatalf("tiers = %v, want %v", got.tiers[tc.key], tc.wantTiers)
			}
		})
	}
}

func TestSnapshotDemandAnyCountryReadsEverySnapshot(t *testing.T) {
	if all, _ := snapshotDemand([]config.Account{{Login: "u", ExitCountry: "*"}}); !all {
		t.Fatal(`"*" without providers must read every snapshot`)
	}
}

func TestBuildAccountRoutePoolAnyCountryWithoutNodesFailsClosed(t *testing.T) {
	_, err := buildAccountRoutePool([]config.Account{{Login: "u", Provider: "a", ExitCountry: "*"}}, map[string]*subscription.Snapshot{"a": snap()}, nil, nil)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("error = %#v", err)
	}
}
