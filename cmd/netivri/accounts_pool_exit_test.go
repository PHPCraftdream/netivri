package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestExplicitDirectPoolAndDemand(t *testing.T) {
	accounts := []config.Account{
		{Login: "alice", Password: config.InitPlaceholder, Exit: "direct", PreSOCKS5: "", PostSOCKS5: ""},
		{Login: "bob", Password: config.InitPlaceholder, Exit: "direct", PreSOCKS5: "none", PostSOCKS5: "none"},
		{Login: "carol", Password: config.InitPlaceholder, Exit: "direct", PostSOCKS5: "socks5://post.example:1080"},
	}
	all, named := snapshotDemand(accounts)
	if all || len(named) != 0 {
		t.Fatalf("direct snapshot demand: all=%v named=%v", all, named)
	}
	pool, err := accountRoutePool(t.TempDir(), &config.Config{Accounts: accounts, Chain: config.Chain{PreSOCKS5: "socks5://global-pre.example:1080"}, Subscriptions: []config.Subscription{{Name: "broken"}}})
	if err != nil {
		t.Fatalf("direct pool consulted unrelated snapshot: %v", err)
	}
	for _, a := range accounts {
		key := pool.accountRoutes[a.Login]
		if key == "direct" || key == "" || len(pool.nodes[key]) != 0 || len(pool.labels[key]) != 0 || len(pool.tiers[key]) != 0 {
			t.Fatalf("direct pool for %s: key=%q pool=%+v", a.Login, key, pool)
		}
		if pool.routePre[key] != "none" {
			t.Errorf("%s pre=%q", a.Login, pool.routePre[key])
		}
	}
	if pool.routePre[pool.accountRoutes["alice"]] != "none" || pool.routePre[pool.accountRoutes["carol"]] != "none" {
		t.Fatalf("explicit direct did not suppress global pre: %+v", pool.routePre)
	}
	if pool.accountRoutes["alice"] != pool.accountRoutes["bob"] {
		t.Fatal("empty direct post and explicit none should share effective none")
	}
	if pool.accountRoutes["carol"] == pool.accountRoutes["alice"] {
		t.Fatal("distinct effective post did not get a distinct route key")
	}
	if pool.routePost[pool.accountRoutes["alice"]] != "none" || pool.routePost[pool.accountRoutes["carol"]] != accounts[2].PostSOCKS5 {
		t.Fatalf("post routes=%v", pool.routePost)
	}
}

func TestDirectPostOverridesAndGlobalInheritance(t *testing.T) {
	accounts := []config.Account{
		{Login: "inherited", Password: config.InitPlaceholder, Exit: "direct"},
		{Login: "cleared", Password: config.InitPlaceholder, Exit: "direct", PostSOCKS5: "none"},
	}
	globalPost := "socks5://global-post.example:1080"
	pool, err := buildAccountRoutePool(accounts, nil, "socks5://global-pre.example:1080", globalPost)
	if err != nil {
		t.Fatal(err)
	}
	inheritedKey, clearedKey := pool.accountRoutes["inherited"], pool.accountRoutes["cleared"]
	if inheritedKey == clearedKey {
		t.Fatal("different effective post hops shared a route key")
	}
	if pool.routePost[inheritedKey] != globalPost || pool.routePost[clearedKey] != "none" {
		t.Fatal("effective post overrides were not retained")
	}
	if pool.routePre[inheritedKey] != "none" || pool.routePre[clearedKey] != "none" {
		t.Fatal("direct mode did not suppress pre hops")
	}
}

func TestExitFingerprintAndStatus(t *testing.T) {
	routeCfg := &config.Config{Accounts: []config.Account{{Login: "alice", Password: config.InitPlaceholder}}}
	directCfg := &config.Config{Accounts: []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct"}}}
	baseKey := credentialRouteKey("", "", "none", "none")
	base := desiredState{cfg: routeCfg, pool: routePool{accountRoutes: map[string]string{"alice": baseKey}}}
	directPool, err := buildAccountRoutePool(directCfg.Accounts, nil, "socks5://global-pre.example:1080", "")
	if err != nil {
		t.Fatal(err)
	}
	direct := desiredState{cfg: directCfg, pool: directPool}
	if routesFingerprint(base) == routesFingerprint(direct) {
		t.Fatal("exit mode did not affect route fingerprint")
	}
	if listenerFingerprint(base) != listenerFingerprint(direct) {
		t.Fatal("exit mode affected listener fingerprint")
	}
	got := accountStatus(directCfg, direct.pool.accountRoutes)
	if len(got) != 1 || got[0].Exit != "direct" {
		t.Fatalf("status = %+v", got)
	}
	if got[0].Chain.PreSOCKS5 != "none" {
		t.Fatalf("direct pre chain was not suppressed: %+v", got[0].Chain)
	}
	if routeCfg.Accounts[0].Exit != "" {
		t.Fatal("missing exit did not normalize to route")
	}
}
