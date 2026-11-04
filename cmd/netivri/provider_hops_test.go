package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

const (
	hopGlobalPre = "socks5://g.example:1080"
	hopProvPreA  = "http://pa.example:8080"
	hopProvPostB = "socks5://pb.example:1080"
	hopAccPre    = "https://acc.example:8443"
)

func hopSnapshots() map[string]*subscription.Snapshot {
	return map[string]*subscription.Snapshot{"a": snap(node("a1", "DE")), "b": snap(node("b1", "DE"))}
}

func hopSubs() []config.Subscription {
	return []config.Subscription{
		{Name: "a", URL: "https://a.example/s", PreSOCKS5: hopProvPreA},
		{Name: "b", URL: "https://b.example/s", PostSOCKS5: hopProvPostB},
	}
}

// Cascade global -> provider -> account: each level replaces the one above.
func TestBuildAccountRoutePoolProviderHopsCascade(t *testing.T) {
	accounts := []config.Account{
		{Login: "inherit", Provider: "a,b"},
		{Login: "none", Provider: "a,b", PreSOCKS5: "none", PostSOCKS5: "none"},
		{Login: "own", Provider: "a,b", PreSOCKS5: hopAccPre},
	}
	pool, err := buildAccountRoutePoolHops(accounts, hopSnapshots(), hopGlobalPre, "", hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	inherit, none, own := pool.accountRoutes["inherit"], pool.accountRoutes["none"], pool.accountRoutes["own"]
	if inherit == none || inherit == own || none == own {
		t.Fatalf("routes must differ: %q %q %q", inherit, none, own)
	}
	// provider a overrides the global pre hop; b has none and takes the global one
	if got := pool.nodePre[inherit]; !reflect.DeepEqual(got, []string{hopProvPreA, hopGlobalPre}) {
		t.Errorf("inherit nodePre = %v", got)
	}
	// only b sets a post hop
	if got := pool.nodePost[inherit]; !reflect.DeepEqual(got, []string{"", hopProvPostB}) {
		t.Errorf("inherit nodePost = %v", got)
	}
	// the account value (even none) replaces everything below it
	if pool.routePre[none] != "none" || pool.routePost[none] != "none" || len(pool.nodePre[none]) != 0 || len(pool.nodePost[none]) != 0 {
		t.Errorf("none route: pre=%q post=%q nodePre=%v nodePost=%v", pool.routePre[none], pool.routePost[none], pool.nodePre[none], pool.nodePost[none])
	}
	if got := pool.nodePre[own]; pool.routePre[own] != hopAccPre || !reflect.DeepEqual(got, []string{hopAccPre, hopAccPre}) {
		t.Errorf("own route: pre=%q nodePre=%v", pool.routePre[own], got)
	}
	// its post side is not set by the account, so provider b still applies there
	if got := pool.nodePost[own]; !reflect.DeepEqual(got, []string{"", hopProvPostB}) {
		t.Errorf("own nodePost = %v", got)
	}
}

// Without provider hops nothing changes: no per-node lists, the same route key.
func TestBuildAccountRoutePoolWithoutProviderHopsIsUnchanged(t *testing.T) {
	accounts := []config.Account{{Login: "u", Provider: "a,b"}}
	plain, err := buildAccountRoutePool(accounts, hopSnapshots(), hopGlobalPre, "")
	if err != nil {
		t.Fatal(err)
	}
	withEmpty, err := buildAccountRoutePoolHops(accounts, hopSnapshots(), hopGlobalPre, "", hopsOf([]config.Subscription{{Name: "a", URL: "https://a.example/s"}}))
	if err != nil {
		t.Fatal(err)
	}
	if plain.accountRoutes["u"] != withEmpty.accountRoutes["u"] || len(withEmpty.nodePre) != 0 || len(withEmpty.nodePost) != 0 {
		t.Errorf("routes %q vs %q, nodePre=%v nodePost=%v", plain.accountRoutes["u"], withEmpty.accountRoutes["u"], withEmpty.nodePre, withEmpty.nodePost)
	}
}

// An account without a provider list draws on every provider, so provider hops apply per node.
func TestBuildAccountRoutePoolProviderHopsForCountryOnlyAccount(t *testing.T) {
	pool, err := buildAccountRoutePoolHops([]config.Account{{Login: "u", ExitCountry: "DE"}}, hopSnapshots(), "", "", hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	key := pool.accountRoutes["u"]
	if got := pool.nodePre[key]; !reflect.DeepEqual(got, []string{hopProvPreA, ""}) {
		t.Errorf("nodePre = %v", got)
	}
}

// A direct account takes no provider hop.
func TestBuildAccountRoutePoolDirectIgnoresProviderHops(t *testing.T) {
	pool, err := buildAccountRoutePoolHops([]config.Account{{Login: "d", Exit: "direct", PostSOCKS5: hopAccPre}}, hopSnapshots(), "", "", hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.nodePre) != 0 || len(pool.nodePost) != 0 {
		t.Errorf("nodePre=%v nodePost=%v", pool.nodePre, pool.nodePost)
	}
}

func TestResolveAccountChainCascade(t *testing.T) {
	cfg := &config.Config{Chain: config.Chain{PreSOCKS5: hopGlobalPre}, Subscriptions: hopSubs()}
	cases := []struct {
		name          string
		acc           config.Account
		post          bool
		value, source string
	}{
		{"account url wins", config.Account{Login: "x", Provider: "a", PreSOCKS5: hopAccPre}, false, hopAccPre, "account"},
		{"account none wins", config.Account{Login: "x", Provider: "a", PreSOCKS5: "none"}, false, "none", "none"},
		{"provider replaces global", config.Account{Login: "x", Provider: "a"}, false, hopProvPreA, "provider"},
		{"provider without a hop falls to global", config.Account{Login: "x", Provider: "b"}, false, hopGlobalPre, "global"},
		{"mixed providers vary", config.Account{Login: "x", Provider: "a,b"}, false, "varies", "provider"},
		{"no provider list sees every provider", config.Account{Login: "x"}, false, "varies", "provider"},
		{"post side: provider b", config.Account{Login: "x", Provider: "b"}, true, hopProvPostB, "provider"},
		{"post side: nothing set", config.Account{Login: "x", Provider: "a"}, true, "none", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAccountChain(cfg, &tc.acc, tc.post)
			if got.value != tc.value || got.source != tc.source {
				t.Fatalf("got %+v, want value=%q source=%q", got, tc.value, tc.source)
			}
		})
	}
}

func TestAccountInfoShowsProviderSource(t *testing.T) {
	cfg := &config.Config{Subscriptions: hopSubs(), Accounts: []config.Account{{Login: "alice", Provider: "a"}}}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	if info.PreSOCKS5.Source != "provider" || info.PreSOCKS5.Value != hopProvPreA {
		t.Fatalf("pre = %+v", info.PreSOCKS5)
	}
	if got := accountsHopLabel(info.PreSOCKS5.Source, info.PreSOCKS5.Value); got != "provider set" {
		t.Fatalf("label = %q", got)
	}
	if got := accountsHopLabel("provider", "varies"); got != "provider varies" {
		t.Fatalf("label = %q", got)
	}
	status := accountStatus(cfg, nil)
	if len(status) != 1 || status[0].Chain.PreSOCKS5Source != "provider" || status[0].Chain.PreSOCKS5 != hopProvPreA {
		t.Fatalf("status = %+v", status)
	}
}

func TestProviderUpdateSetsAndClearsHops(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.example/sub"})
	pre, post := "http://u:hop-pw@h.example:8080", "none"
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Pre: &pre, Post: &post}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subscriptions[0].PreSOCKS5 != pre || cfg.Subscriptions[0].PostSOCKS5 != "none" {
		t.Fatalf("saved = %+v", cfg.Subscriptions[0])
	}
	list, err := ProviderList(home)
	if err != nil {
		t.Fatal(err)
	}
	if list.Providers[0].Pre != "http://h.example:8080" || list.Providers[0].Post != "none" {
		t.Fatalf("listed = %+v", list.Providers[0])
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "hop-pw") {
		t.Fatalf("hop credentials leaked in %s", raw)
	}
	empty := ""
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Pre: &empty}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(home)
	if cfg.Subscriptions[0].PreSOCKS5 != "" || cfg.Subscriptions[0].PostSOCKS5 != "none" {
		t.Fatalf("after clear = %+v", cfg.Subscriptions[0])
	}
	bad := "ftp://h.example:21"
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Pre: &bad}); err == nil {
		t.Fatal("a hop that is not socks5/http/https must be rejected")
	}
}

func TestSubSetSOCKS5Command(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.example/sub"})
	var out bytes.Buffer
	if err := runSub([]string{"set-socks5", "p", "--home", home, "--pre", "http://u:hop-pw@h.example:8080", "--post", "none"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "http://h.example:8080") || strings.Contains(out.String(), "hop-pw") {
		t.Fatalf("output = %s", out.String())
	}
	out.Reset()
	if err := runSub([]string{"set-socks5", "p", "--home", home, "--pre", "inherit"}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadConfig(home)
	if cfg.Subscriptions[0].PreSOCKS5 != "" || cfg.Subscriptions[0].PostSOCKS5 != "none" {
		t.Fatalf("saved = %+v", cfg.Subscriptions[0])
	}
	for _, args := range [][]string{
		{"set-socks5", "p", "--home", home},
		{"set-socks5", "--home", home, "--pre", "none"},
		{"set-socks5", "p", "--home", home, "--pre", ""},
		{"set-socks5", "p", "--home", home, "--pre", "ftp://h.example:21"},
		{"set-socks5", "missing", "--home", home, "--pre", "none"},
	} {
		if err := runSub(args, &out); err == nil {
			t.Errorf("runSub(%v) must fail", args)
		}
	}
}

func TestConfigRejectsInvalidProviderHop(t *testing.T) {
	for _, hop := range []string{"ftp://h.example:21", "socks5://h.example", "h.example:1080"} {
		cfg := config.Default()
		cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.example/s", PostSOCKS5: hop}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("hop %q must be rejected", hop)
		} else if strings.Contains(err.Error(), "h.example") {
			t.Errorf("error echoes the value: %v", err)
		}
	}
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.example/s", PreSOCKS5: "https://h.example:8443", PostSOCKS5: "none"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
