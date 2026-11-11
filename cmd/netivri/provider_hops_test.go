package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
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
		{Name: "a", URL: "https://a.example/s", PreSOCKS5: config.HopList{hopProvPreA}},
		{Name: "b", URL: "https://b.example/s", PostSOCKS5: config.HopList{hopProvPostB}},
	}
}

func TestBuildAccountRoutePoolSingletonHopIdentityRemainsLegacy(t *testing.T) {
	if got, want := credentialRouteKeyHops("p", "DE", config.HopList{"pre"}, config.HopList{"post"}), credentialRouteKey("p", "DE", "pre", "post"); got != want {
		t.Fatalf("singleton route identity = %q, want legacy identity %q", got, want)
	}
	hops := hopsOf([]config.Subscription{{Name: "p", PreSOCKS5: config.HopList{"pre"}, PostSOCKS5: config.HopList{"post"}}})
	if got, want := hops.tag([]string{"p"}, nil, nil), "p\x01pre\x01post"; got != want {
		t.Fatalf("singleton provider identity = %q, want legacy identity %q", got, want)
	}
}

// Cascade global -> provider -> account: each level replaces the one above.
func TestBuildAccountRoutePoolProviderHopsCascade(t *testing.T) {
	accounts := []config.Account{
		{Login: "inherit", Provider: "a,b"},
		{Login: "none", Provider: "a,b", PreSOCKS5: config.HopList{"none"}, PostSOCKS5: config.HopList{"none"}},
		{Login: "own", Provider: "a,b", PreSOCKS5: config.HopList{hopAccPre}},
	}
	pool, err := buildAccountRoutePoolHops(accounts, hopSnapshots(), config.HopList{hopGlobalPre}, nil, hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	inherit, none, own := pool.accountRoutes["inherit"], pool.accountRoutes["none"], pool.accountRoutes["own"]
	if inherit == none || inherit == own || none == own {
		t.Fatalf("routes must differ: %q %q %q", inherit, none, own)
	}
	// provider a overrides the global pre hop; b has none and takes the global one
	if got := pool.nodePre[inherit]; !reflect.DeepEqual(got, []config.HopList{{hopProvPreA}, {hopGlobalPre}}) {
		t.Errorf("inherit nodePre = %v", got)
	}
	// only b sets a post hop
	if got := pool.nodePost[inherit]; !reflect.DeepEqual(got, []config.HopList{nil, {hopProvPostB}}) {
		t.Errorf("inherit nodePost = %v", got)
	}
	// the account value (even none) replaces everything below it
	if pool.routePre[none][0] != "none" || pool.routePost[none][0] != "none" || len(pool.nodePre[none]) != 0 || len(pool.nodePost[none]) != 0 {
		t.Errorf("none route: pre=%q post=%q nodePre=%v nodePost=%v", pool.routePre[none], pool.routePost[none], pool.nodePre[none], pool.nodePost[none])
	}
	if got := pool.nodePre[own]; !reflect.DeepEqual(got, []config.HopList{{hopAccPre}, {hopAccPre}}) || !reflect.DeepEqual(pool.routePre[own], config.HopList{hopAccPre}) {
		t.Errorf("own route: pre=%q nodePre=%v", pool.routePre[own], got)
	}
	// its post side is not set by the account, so provider b still applies there
	if got := pool.nodePost[own]; !reflect.DeepEqual(got, []config.HopList{nil, {hopProvPostB}}) {
		t.Errorf("own nodePost = %v", got)
	}
}

func TestBuildAccountRoutePoolProviderHopsMultiListReplacement(t *testing.T) {
	globalPre := config.HopList{"socks5://global-pre-1:secret@global-pre-1.example:1080", "http://global-pre-2:secret@global-pre-2.example:8080"}
	globalPost := config.HopList{"https://global-post-1:secret@global-post-1.example:8443", "socks5://global-post-2:secret@global-post-2.example:1080"}
	aPre := config.HopList{"socks5://a-pre-1:secret@a-pre-1.example:1080", "http://a-pre-2:secret@a-pre-2.example:8080"}
	bPost := config.HopList{"https://b-post-1:secret@b-post-1.example:8443", "socks5://b-post-2:secret@b-post-2.example:1080"}
	accountPre := config.HopList{"socks5://account-pre-1:secret@account-pre-1.example:1080", "http://account-pre-2:secret@account-pre-2.example:8080"}
	accountPost := config.HopList{"https://account-post-1:secret@account-post-1.example:8443", "socks5://account-post-2:secret@account-post-2.example:1080"}
	subs := []config.Subscription{
		{Name: "a", URL: "https://provider-user:provider-secret@a.example/sub", PreSOCKS5: aPre},
		{Name: "b", URL: "https://provider-user:provider-secret@b.example/sub", PostSOCKS5: bPost},
	}
	accounts := []config.Account{
		{Login: "inherit", Provider: "b,a", ExitCountry: "DE"},
		{Login: "account", Provider: "b,a", ExitCountry: "DE", PreSOCKS5: accountPre, PostSOCKS5: accountPost},
		{Login: "cancel", Provider: "b,a", ExitCountry: "DE", PreSOCKS5: config.HopList{"none"}, PostSOCKS5: config.HopList{"none"}},
	}
	snapshotA := node("a1", "DE")
	snapshotA.Raw = map[string]any{"name": "a1", "password": "snapshot-fake-secret", "server": "snapshot-secret.example"}
	snapshotB := node("b1", "DE")
	snapshots := map[string]*subscription.Snapshot{"a": snap(snapshotA), "b": snap(snapshotB)}
	var validated []string
	pool, err := buildAccountRoutePoolHopsWithValidation(accounts, snapshots, globalPre, globalPost, hopsOf(subs), func(field string, _ config.HopList) error {
		validated = append(validated, field)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantValidated := []string{
		"chain.pre_socks5", "chain.post_socks5",
		"subscriptions[0].pre_socks5", "subscriptions[0].post_socks5",
		"subscriptions[1].pre_socks5", "subscriptions[1].post_socks5",
		"accounts[0].pre_socks5", "accounts[0].post_socks5",
		"accounts[1].pre_socks5", "accounts[1].post_socks5",
		"accounts[2].pre_socks5", "accounts[2].post_socks5",
	}
	if !reflect.DeepEqual(validated, wantValidated) {
		t.Fatalf("validated fields = %v, want %v", validated, wantValidated)
	}

	inherit, own, cancel := pool.accountRoutes["inherit"], pool.accountRoutes["account"], pool.accountRoutes["cancel"]
	if inherit == own || inherit == cancel || own == cancel {
		t.Fatalf("replacement routes should have distinct identities: %q %q %q", inherit, own, cancel)
	}
	if !reflect.DeepEqual(pool.routePre[inherit], globalPre) || !reflect.DeepEqual(pool.routePost[inherit], globalPost) {
		t.Fatal("inherited route lists did not replace only at their overriding level")
	}
	if !reflect.DeepEqual(pool.labels[inherit], []string{"b/b1", "a/a1"}) {
		t.Fatal("provider order was not preserved in node labels")
	}
	if got := pool.nodePre[inherit]; !reflect.DeepEqual(got, []config.HopList{globalPre, aPre}) {
		t.Fatal("nodePre did not replace per provider in node order")
	}
	if got := pool.nodePost[inherit]; !reflect.DeepEqual(got, []config.HopList{bPost, globalPost}) {
		t.Fatal("nodePost did not replace per provider in node order")
	}
	if !reflect.DeepEqual(pool.routePre[own], accountPre) || !reflect.DeepEqual(pool.routePost[own], accountPost) || len(pool.nodePre[own]) != 0 || len(pool.nodePost[own]) != 0 {
		t.Fatal("account lists appended lower levels instead of replacing them")
	}
	if !reflect.DeepEqual(pool.routePre[cancel], config.HopList{"none"}) || !reflect.DeepEqual(pool.routePost[cancel], config.HopList{"none"}) || len(pool.nodePre[cancel]) != 0 || len(pool.nodePost[cancel]) != 0 {
		t.Fatal("none did not stop inheritance")
	}
	for _, secret := range []string{"global-pre-1:secret", "global-post-1:secret", "provider-secret", "account-pre-1:secret", "account-post-1:secret", "snapshot-fake-secret", "snapshot-secret.example"} {
		if strings.Contains(inherit, secret) || strings.Contains(own, secret) || strings.Contains(cancel, secret) {
			t.Errorf("route identity leaked snapshot/chain secret %q", secret)
		}
	}

	for _, tc := range []struct {
		name string
		edit func([]config.Subscription)
		want config.HopList
	}{
		{"provider pre order", func(changed []config.Subscription) { changed[0].PreSOCKS5 = config.HopList{aPre[1], aPre[0]} }, config.HopList{aPre[1], aPre[0]}},
		{"provider post order", func(changed []config.Subscription) { changed[1].PostSOCKS5 = config.HopList{bPost[1], bPost[0]} }, config.HopList{bPost[1], bPost[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changedSubs := append([]config.Subscription(nil), subs...)
			tc.edit(changedSubs)
			changed, err := buildAccountRoutePoolHopsWithValidation(accounts[:1], hopSnapshots(), globalPre, globalPost, hopsOf(changedSubs), func(string, config.HopList) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			changedKey := changed.accountRoutes["inherit"]
			if changedKey == inherit {
				t.Fatal("reordered provider list did not change route identity")
			}
			if tc.name == "provider pre order" {
				if got := changed.nodePre[changedKey][1]; !reflect.DeepEqual(got, tc.want) {
					t.Fatal("nodePre provider hop order was not preserved")
				}
			} else if got := changed.nodePost[changedKey][0]; !reflect.DeepEqual(got, tc.want) {
				t.Fatal("nodePost provider hop order was not preserved")
			}
		})
	}
}

func TestBuildAccountRoutePoolHopsProductionValidationCoversEveryLevel(t *testing.T) {
	invalid := make(config.HopList, config.MaxHopsPerSide+1)
	for i := range invalid {
		invalid[i] = fmt.Sprintf("socks5://hop-%d.example:1080", i)
	}
	cases := []struct {
		name                  string
		field                 string
		globalPre, globalPost config.HopList
		hops                  providerHops
		accounts              []config.Account
	}{
		{name: "global pre", field: "chain.pre_socks5", globalPre: invalid},
		{name: "global post", field: "chain.post_socks5", globalPost: invalid},
		{name: "provider pre", field: "subscriptions[0].pre_socks5", hops: hopsOf([]config.Subscription{{Name: "p", URL: "https://p.example/sub", PreSOCKS5: invalid}})},
		{name: "provider post", field: "subscriptions[0].post_socks5", hops: hopsOf([]config.Subscription{{Name: "p", URL: "https://p.example/sub", PostSOCKS5: invalid}})},
		{name: "account pre", field: "accounts[0].pre_socks5", accounts: []config.Account{{PreSOCKS5: invalid}}},
		{name: "account post", field: "accounts[0].post_socks5", accounts: []config.Account{{PostSOCKS5: invalid}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildAccountRoutePoolHops(tc.accounts, nil, tc.globalPre, tc.globalPost, tc.hops); err == nil || !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "at most 16 hops per side") {
				t.Fatalf("invalid %s was not rejected with field %s and the unsupported-pool reason: %v", tc.name, tc.field, err)
			}
		})
	}
}

// Without provider hops nothing changes: no per-node lists, the same route key.
func TestProviderPoolRouteIdentityStableForMutableList(t *testing.T) {
	snapshots := map[string]*subscription.Snapshot{"p": snap(node("1", "DE"))}
	account := config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"}
	firstHops := hopsOf([]config.Subscription{{Name: "p", PostSOCKS5: config.HopList{"socks5://first.example:1080", "socks5://second.example:1080"}}})
	secondHops := hopsOf([]config.Subscription{{Name: "p", PostSOCKS5: config.HopList{"socks5://second.example:1080", "socks5://third.example:1080"}}})
	first, err := buildAccountRoutePoolHops([]config.Account{account}, snapshots, nil, nil, firstHops)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildAccountRoutePoolHops([]config.Account{account}, snapshots, nil, nil, secondHops)
	if err != nil {
		t.Fatal(err)
	}
	if first.accountRoutes["alice"] != second.accountRoutes["alice"] {
		t.Fatalf("pool edit changed logical key: %q != %q", first.accountRoutes["alice"], second.accountRoutes["alice"])
	}
	if !reflect.DeepEqual(first.nodePost[first.accountRoutes["alice"]][0], config.HopList{"socks5://first.example:1080", "socks5://second.example:1080"}) || !reflect.DeepEqual(second.nodePost[second.accountRoutes["alice"]][0], config.HopList{"socks5://second.example:1080", "socks5://third.example:1080"}) {
		t.Fatal("provider post pool order/content was not mapped to its node")
	}
}

func TestBuildAccountRoutePoolWithoutProviderHopsIsUnchanged(t *testing.T) {
	accounts := []config.Account{{Login: "u", Provider: "a,b"}}
	plain, err := buildAccountRoutePool(accounts, hopSnapshots(), config.HopList{hopGlobalPre}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withEmpty, err := buildAccountRoutePoolHops(accounts, hopSnapshots(), config.HopList{hopGlobalPre}, nil, hopsOf([]config.Subscription{{Name: "a", URL: "https://a.example/s"}}))
	if err != nil {
		t.Fatal(err)
	}
	if plain.accountRoutes["u"] != withEmpty.accountRoutes["u"] || len(withEmpty.nodePre) != 0 || len(withEmpty.nodePost) != 0 {
		t.Errorf("routes %q vs %q, nodePre=%v nodePost=%v", plain.accountRoutes["u"], withEmpty.accountRoutes["u"], withEmpty.nodePre, withEmpty.nodePost)
	}
}

// An account without a provider list draws on every provider, so provider hops apply per node.
func TestBuildAccountRoutePoolProviderHopsForCountryOnlyAccount(t *testing.T) {
	pool, err := buildAccountRoutePoolHops([]config.Account{{Login: "u", ExitCountry: "DE"}}, hopSnapshots(), nil, nil, hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	key := pool.accountRoutes["u"]
	if got := pool.nodePre[key]; !reflect.DeepEqual(got, []config.HopList{{hopProvPreA}, nil}) {
		t.Errorf("nodePre = %v", got)
	}
}

// A direct account takes no provider hop.
func TestBuildAccountRoutePoolDirectIgnoresProviderHops(t *testing.T) {
	pool, err := buildAccountRoutePoolHops([]config.Account{{Login: "d", Exit: "direct", PostSOCKS5: config.HopList{hopAccPre}}}, hopSnapshots(), nil, nil, hopsOf(hopSubs()))
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.nodePre) != 0 || len(pool.nodePost) != 0 {
		t.Errorf("nodePre=%v nodePost=%v", pool.nodePre, pool.nodePost)
	}
}

func TestResolveAccountChainCascade(t *testing.T) {
	cfg := config.Default()
	cfg.Chain.PreSOCKS5 = config.HopList{hopGlobalPre}
	cfg.Subscriptions = hopSubs()
	cases := []struct {
		name   string
		acc    config.Account
		post   bool
		value  []string
		source string
	}{
		{"account url wins", config.Account{Login: "x", Provider: "a", PreSOCKS5: config.HopList{hopAccPre}}, false, []string{"https://acc.example:8443"}, "account"},
		{"account none wins", config.Account{Login: "x", Provider: "a", PreSOCKS5: config.HopList{"none"}}, false, []string{"none"}, "none"},
		{"provider replaces global", config.Account{Login: "x", Provider: "a"}, false, []string{hopProvPreA}, "provider"},
		{"provider without a hop falls to global", config.Account{Login: "x", Provider: "b"}, false, []string{hopGlobalPre}, "global"},
		{"mixed providers vary", config.Account{Login: "x", Provider: "a,b"}, false, []string{"varies"}, "provider"},
		{"no provider list sees every provider", config.Account{Login: "x"}, false, []string{"varies"}, "provider"},
		{"post side: provider b", config.Account{Login: "x", Provider: "b"}, true, []string{hopProvPostB}, "provider"},
		{"post side: nothing set", config.Account{Login: "x", Provider: "a"}, true, []string{"none"}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Accounts = []config.Account{tc.acc}
			cfg.Accounts[0].Password = config.InitPlaceholder
			if err := cfg.Validate(); err != nil {
				t.Fatalf("resolver fixture failed validation: %v", err)
			}
			if got := resolveAccountChain(cfg, &cfg.Accounts[0], tc.post); !reflect.DeepEqual(got.value, config.HopList(tc.value)) || got.source != tc.source {
				t.Fatalf("got %+v, want value=%q source=%q", got, tc.value, tc.source)
			}
		})
	}
}

func TestAccountInfoShowsProviderSource(t *testing.T) {
	cfg := config.Default()
	cfg.Subscriptions = hopSubs()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "a"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("account-info fixture failed validation: %v", err)
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	if info.PreSOCKS5.Source != "provider" || !reflect.DeepEqual(info.PreSOCKS5.Value, []string{hopProvPreA}) {
		t.Fatalf("pre = %+v", info.PreSOCKS5)
	}
	if got := accountsHopListLabel(info.PreSOCKS5.Source, info.PreSOCKS5.Value); got != "provider 1 hop: http://pa.example:8080" {
		t.Fatalf("label = %q", got)
	}
	if got := accountsHopLabel("provider", "varies"); got != "provider varies" {
		t.Fatalf("label = %q", got)
	}
	status := accountStatus(cfg, nil)
	if len(status) != 1 || status[0].Chain.PreSOCKS5Source != "provider" || !slices.Equal(status[0].Chain.PreSOCKS5, []string{hopProvPreA}) {
		t.Fatalf("status = %+v", status)
	}
}

func TestProviderUpdateSetsAndClearsHops(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.example/sub"})
	pre, post := "http://u:hop-pw@h.example:8080", "none"
	preList, postList := config.HopList{pre}, config.HopList{post}
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Pre: &preList, Post: &postList}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Subscriptions[0].PreSOCKS5, config.HopList{pre}) || !reflect.DeepEqual(cfg.Subscriptions[0].PostSOCKS5, config.HopList{"none"}) {
		t.Fatal("ProviderUpdate did not persist the requested hop lists")
	}
	list, err := ProviderList(home)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(list.Providers[0].Pre, []string{"http://h.example:8080"}) || !slices.Equal(list.Providers[0].Post, []string{"none"}) {
		t.Fatal("listed provider hops did not match the expected redacted singleton values")
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "hop-pw") {
		t.Fatal("provider list JSON leaked credentials")
	}
	empty := config.HopList{}
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Pre: &empty}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(home)
	if len(cfg.Subscriptions[0].PreSOCKS5) != 0 || cfg.Subscriptions[0].PostSOCKS5[0] != "none" {
		t.Fatal("provider update did not clear only pre-hop")
	}
	bad := config.HopList{"ftp://h.example:21"}
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
		t.Fatal("provider command output mismatch or exposed credentials")
	}
	out.Reset()
	if err := runSub([]string{"set-socks5", "p", "--home", home, "--pre", "inherit"}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadConfig(home)
	if len(cfg.Subscriptions[0].PreSOCKS5) != 0 || cfg.Subscriptions[0].PostSOCKS5[0] != "none" {
		t.Fatal("provider update did not clear only pre-hop")
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
		cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.example/s", PostSOCKS5: config.HopList{hop}}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("hop %q must be rejected", hop)
		} else if strings.Contains(err.Error(), "h.example") {
			t.Error("config validation error echoed the invalid hop")
		}
	}
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.example/s", PreSOCKS5: config.HopList{"https://h.example:8443"}, PostSOCKS5: config.HopList{"none"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
