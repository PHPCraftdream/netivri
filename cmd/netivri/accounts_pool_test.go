package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/stats"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func snap(nodes ...subscription.Node) *subscription.Snapshot {
	return &subscription.Snapshot{Nodes: nodes}
}

func node(id, country string) subscription.Node {
	return subscription.Node{ID: id, Country: country, Raw: map[string]any{"name": id}}
}

// The design table of docs/design/accounts.md, one row per case.
func TestBuildAccountRoutePoolDesignTable(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE"), node("m-nl", "NL")),
		"alt":  snap(node("a-de", "DE")),
	}
	cases := []struct {
		name    string
		account config.Account
		key     string
		first   string // raw name of the first node of the route
	}{
		{"default route", config.Account{Login: "u"}, "", ""},
		{"country only", config.Account{Login: "u", ExitCountry: "DE"}, "~DE", "a-de"},
		{"provider only", config.Account{Login: "u", Provider: "main"}, "main~", "m-de"},
		{"provider and country", config.Account{Login: "u", Provider: "main", ExitCountry: "DE"}, "main~DE", "m-de"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildAccountRoutePool([]config.Account{tc.account}, snapshots)
			if err != nil {
				t.Fatal(err)
			}
			if tc.key == "" {
				if len(got.accountRoutes) != 0 || len(got.nodes) != 0 {
					t.Fatalf("unexpected pool: %+v", got)
				}
				return
			}
			if got.accountRoutes["u"] != tc.key {
				t.Fatalf("route = %q, want %q", got.accountRoutes["u"], tc.key)
			}
			if len(got.nodes[tc.key]) == 0 || got.nodes[tc.key][0]["name"] != tc.first {
				t.Fatalf("nodes of %q: %v", tc.key, got.nodes[tc.key])
			}
		})
	}
}

func TestBuildAccountRoutePoolCountryOnlySpansProvidersOrdered(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("z", "DE"), node("nl", "NL")),
		"alt":  snap(node("a", "DE"), node("a-dup", "DE")),
	}
	got, err := buildAccountRoutePool([]config.Account{{Login: "u", ExitCountry: "DE"}}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range got.nodes["~DE"] {
		names = append(names, raw["name"].(string))
	}
	if strings.Join(names, ",") != "a,a-dup,z" {
		t.Fatalf("order = %v, want [a a-dup z]", names)
	}
}

func TestBuildAccountRoutePoolIDTieBrokenByProviderName(t *testing.T) {
	from := func(provider string) subscription.Node {
		return subscription.Node{ID: "same", Country: "DE", Raw: map[string]any{"name": "same", "from": provider}}
	}
	snapshots := map[string]*subscription.Snapshot{
		"zeta":  snap(from("zeta")),
		"alpha": snap(from("alpha")),
	}
	got, err := buildAccountRoutePool([]config.Account{{Login: "u", ExitCountry: "DE"}}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	route := got.nodes["~DE"]
	if len(route) != 2 || route[0]["from"] != "alpha" || route[1]["from"] != "zeta" {
		t.Fatalf("nodes = %v, want alpha before zeta", route)
	}
}

func TestBuildAccountRoutePoolEachKeyBuiltOnce(t *testing.T) {
	accounts := []config.Account{
		{Login: "a", Provider: "p", ExitCountry: "DE"},
		{Login: "b", Provider: "p", ExitCountry: "DE"},
	}
	snapshots := map[string]*subscription.Snapshot{"p": snap(node("1", "DE"), node("2", "DE"))}
	got, err := buildAccountRoutePool(accounts, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.nodes) != 1 || len(got.nodes["p~DE"]) != 2 {
		t.Fatalf("pool = %+v", got.nodes)
	}
	if got.accountRoutes["a"] != "p~DE" || got.accountRoutes["b"] != "p~DE" {
		t.Fatalf("routes = %v", got.accountRoutes)
	}
}

func TestBuildAccountRoutePoolFailClosed(t *testing.T) {
	cases := []struct {
		name     string
		account  config.Account
		snapshot map[string]*subscription.Snapshot
	}{
		{"country without nodes", config.Account{Login: "u", ExitCountry: "JP"}, map[string]*subscription.Snapshot{"p": snap(node("1", "DE"))}},
		{"provider not configured", config.Account{Login: "u", Provider: "ghost"}, nil},
		{"provider without snapshot", config.Account{Login: "u", Provider: "p"}, nil},
		{"provider without country nodes", config.Account{Login: "u", Provider: "p", ExitCountry: "JP"}, map[string]*subscription.Snapshot{"p": snap(node("1", "DE"))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAccountRoutePool([]config.Account{tc.account}, tc.snapshot)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound || ce.Hint != "run netivri sub update before netivri up" {
				t.Fatalf("unexpected error: %#v", err)
			}
			lower := strings.ToLower(ce.Message)
			if strings.Contains(lower, "http") || strings.Contains(lower, "://") {
				t.Fatalf("message %q leaks a URL", ce.Message)
			}
		})
	}
}

func TestAccountRoutePoolLoadsStoredSnapshotsAndSkipsMissing(t *testing.T) {
	home := t.TempDir()
	store := subscription.NewStore(home + "/subscriptions")
	if err := store.Save("matching", subscription.Snapshot{Nodes: []subscription.Node{{ID: "1", Country: "DE", Raw: map[string]any{"name": "fixture"}}}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Accounts:      []config.Account{{Login: "a", Provider: "matching", ExitCountry: "DE"}},
		Subscriptions: []config.Subscription{{Name: "missing"}, {Name: "matching"}},
	}
	got, err := accountRoutePool(home, cfg)
	if err != nil || got.nodes["matching~DE"][0]["name"] != "fixture" || got.accountRoutes["a"] != "matching~DE" {
		t.Fatalf("pool=%v routes=%v err=%v", got.nodes, got.accountRoutes, err)
	}
	if labels := got.labels["matching~DE"]; len(labels) != 1 || labels[0] != "matching/1" {
		t.Fatalf("labels = %v, want [matching/1]", labels)
	}
	if len(got.infos) != 1 || got.infos[0] != (stats.NodeInfo{Label: "matching/1", Provider: "matching", Country: "DE"}) {
		t.Fatalf("infos = %+v", got.infos)
	}
}

// TestBuildAccountRoutePoolLabelsFollowNodeOrder: labels and infos line up
// with the ordered nodes of each route, one info per distinct node.
func TestBuildAccountRoutePoolLabelsFollowNodeOrder(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("z", "DE"), node("a", "DE")),
		"alt":  snap(node("m", "DE")),
	}
	accounts := []config.Account{
		{Login: "any", ExitCountry: "DE"},
		{Login: "onlymain", Provider: "main", ExitCountry: "DE"},
	}
	got, err := buildAccountRoutePool(accounts, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.labels["~DE"], ",") != "main/a,alt/m,main/z" {
		t.Fatalf("~DE labels = %v", got.labels["~DE"])
	}
	if strings.Join(got.labels["main~DE"], ",") != "main/a,main/z" {
		t.Fatalf("main~DE labels = %v", got.labels["main~DE"])
	}
	for key, labels := range got.labels {
		if len(labels) != len(got.nodes[key]) {
			t.Fatalf("route %q: %d labels for %d nodes", key, len(labels), len(got.nodes[key]))
		}
	}
	if len(got.infos) != 3 {
		t.Fatalf("infos = %+v, want 3 distinct nodes", got.infos)
	}
}

func TestAccountRoutePoolMissingAllSnapshots(t *testing.T) {
	cfg := &config.Config{
		Accounts:      []config.Account{{Login: "a", ExitCountry: "DE"}},
		Subscriptions: []config.Subscription{{Name: "missing"}},
	}
	_, err := accountRoutePool(t.TempDir(), cfg)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", err)
	}
}

// brokenSnapshot makes the stored snapshot of name unreadable.
func brokenSnapshot(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "subscriptions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".ktav"), []byte("nodes: [\n    {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRoutePoolIgnoresSnapshotsNoRouteReads(t *testing.T) {
	home := t.TempDir()
	brokenSnapshot(t, home, "unused")
	cfg := &config.Config{
		Accounts:      []config.Account{{Login: "plain"}},
		Subscriptions: []config.Subscription{{Name: "unused"}},
	}
	got, err := accountRoutePool(home, cfg)
	if err != nil || len(got.nodes) != 0 || len(got.accountRoutes) != 0 || len(got.infos) != 0 {
		t.Fatalf("no routed account: nodes=%v routes=%v err=%v", got.nodes, got.accountRoutes, err)
	}

	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("used", subscription.Snapshot{Nodes: []subscription.Node{node("1", "DE")}}); err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "a", Provider: "used"}}
	cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: "used"})
	got, err = accountRoutePool(home, cfg)
	if err != nil || len(got.nodes["used~"]) != 1 || got.accountRoutes["a"] != "used~" {
		t.Fatalf("provider route must not read the other snapshot: nodes=%v routes=%v err=%v", got.nodes, got.accountRoutes, err)
	}
}

func TestAccountRoutePoolBrokenSnapshotOfNeededProvider(t *testing.T) {
	home := t.TempDir()
	brokenSnapshot(t, home, "p")
	cfg := &config.Config{
		Accounts:      []config.Account{{Login: "a", Provider: "p"}},
		Subscriptions: []config.Subscription{{Name: "p"}},
	}
	_, err := accountRoutePool(home, cfg)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "SUB_SNAPSHOT_INVALID" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestAccountStatusLiveRoutesAndNoCountry(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "z", Provider: "p", ExitCountry: "DE"}, {Login: "a"}}}
	got := accountStatus(cfg, map[string]string{"z": "p~NL"})
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if got[0].Login != "a" || got[0].Provider != "" || got[0].ExitCountry != "" {
		t.Fatalf("got %#v", got[0])
	}
	if got[1].Login != "z" || got[1].Provider != "p" || got[1].ExitCountry != "NL" {
		t.Fatalf("got %#v", got[1])
	}
}
