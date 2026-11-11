package main

import (
	"reflect"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestHopOptionsAccountPoolsPreserveOrderedLists(t *testing.T) {
	pre := config.HopList{"socks5://pre-one.example:1080", "http://pre-two.example:8080"}
	post := config.HopList{"https://post-one.example:8443", "socks5://post-two.example:1080"}
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE", PreSOCKS5: pre, PostSOCKS5: post}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("account pool fixture invalid: %v", err)
	}
	pool, err := buildAccountRoutePool(cfg.Accounts, map[string]*subscription.Snapshot{"p": snap(node("one", "DE"))}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	options, _, err := (&liveAngel{}).routeOptionsWithLoggerChecked(desiredState{cfg: cfg, pool: pool}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := options.AccountRoute["alice"]
	if key == "" {
		t.Fatal("alice has no route mapping")
	}
	if options.RoutePre[key] != "" || options.RoutePost[key] != "" {
		t.Fatalf("scalar route mappings truncated account pools: pre=%q post=%q", options.RoutePre[key], options.RoutePost[key])
	}
	if !reflect.DeepEqual(options.RoutePrePool, map[string][]string{key: pre}) || !reflect.DeepEqual(options.RoutePostPool, map[string][]string{key: post}) {
		t.Fatalf("route pools = pre %v post %v", options.RoutePrePool, options.RoutePostPool)
	}
}

func TestHopOptionsGlobalMultiPoolsRetainPerNodeSingletonAndNoneOverrides(t *testing.T) {
	globalPre := config.HopList{"socks5://global-pre-one.example:1080", "http://global-pre-two.example:8080"}
	globalPost := config.HopList{"socks5://global-post-one.example:1080", "http://global-post-two.example:8080"}
	pPre := config.HopList{"socks5://p-pre.example:1080"}
	pPost := config.HopList{"https://p-post.example:8443"}
	qPre := config.HopList{"http://q-pre.example:8080"}
	qPost := config.HopList{"socks5://q-post.example:1080"}
	for _, tc := range []struct {
		name                          string
		pPre, pPost, qPre, qPost      config.HopList
		wantPre, wantPost             [][]string
		wantScalarPre, wantScalarPost []string
	}{
		{
			name: "provider singleton then none",
			pPre: pPre, pPost: pPost, qPre: config.HopList{"none"}, qPost: config.HopList{"none"},
			wantPre: [][]string{pPre, {"none"}}, wantPost: [][]string{pPost, {"none"}},
			wantScalarPre: []string{pPre[0], "none"}, wantScalarPost: []string{pPost[0], "none"},
		},
		{
			name: "none then provider singleton",
			pPre: config.HopList{"none"}, pPost: config.HopList{"none"}, qPre: qPre, qPost: qPost,
			wantPre: [][]string{{"none"}, qPre}, wantPost: [][]string{{"none"}, qPost},
			wantScalarPre: []string{"none", qPre[0]}, wantScalarPost: []string{"none", qPost[0]},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subs := []config.Subscription{
				{Name: "p", URL: "https://p.example/sub", PreSOCKS5: tc.pPre, PostSOCKS5: tc.pPost},
				{Name: "q", URL: "https://q.example/sub", PreSOCKS5: tc.qPre, PostSOCKS5: tc.qPost},
			}
			cfg := config.Default()
			cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5 = globalPre, globalPost
			cfg.Subscriptions = subs
			cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p,q", ExitCountry: "DE"}}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("global/provider pool fixture invalid: %v", err)
			}
			pool, err := buildAccountRoutePoolHops(cfg.Accounts, map[string]*subscription.Snapshot{
				"p": snap(node("p1", "DE")), "q": snap(node("q1", "DE")),
			}, globalPre, globalPost, hopsOf(subs))
			if err != nil {
				t.Fatal(err)
			}
			options, _, err := (&liveAngel{}).routeOptionsWithLoggerChecked(desiredState{cfg: cfg, pool: pool}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			key := options.AccountRoute["alice"]
			if !reflect.DeepEqual(options.NodePrePool[key], tc.wantPre) || !reflect.DeepEqual(options.NodePostPool[key], tc.wantPost) {
				t.Fatalf("node pools = pre %v post %v, want pre %v post %v", options.NodePrePool[key], options.NodePostPool[key], tc.wantPre, tc.wantPost)
			}
			if !reflect.DeepEqual(options.NodePre[key], tc.wantScalarPre) || !reflect.DeepEqual(options.NodePost[key], tc.wantScalarPost) {
				t.Fatalf("scalar node mappings = pre %v post %v, want pre %v post %v", options.NodePre[key], options.NodePost[key], tc.wantScalarPre, tc.wantScalarPost)
			}
		})
	}
}

func TestHopOptionsProviderPoolsFollowRouteNodeOrder(t *testing.T) {
	pPre := config.HopList{"socks5://p-pre-one.example:1080", "http://p-pre-two.example:8080"}
	pPost := config.HopList{"socks5://p-post-one.example:1080", "https://p-post-two.example:8443"}
	qPre := config.HopList{"socks5://q-pre.example:1080"}
	qPost := config.HopList{"http://q-post.example:8080"}
	subs := []config.Subscription{
		{Name: "p", URL: "https://p.example/sub", PreSOCKS5: pPre, PostSOCKS5: pPost},
		{Name: "q", URL: "https://q.example/sub", PreSOCKS5: qPre, PostSOCKS5: qPost},
		{Name: "n", URL: "https://n.example/sub"},
	}
	cfg := config.Default()
	cfg.Subscriptions = subs
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p,q,n", ExitCountry: "DE"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("provider pool fixture invalid: %v", err)
	}
	snapshots := map[string]*subscription.Snapshot{
		"p": snap(node("p2", "DE"), node("p1", "DE")),
		"q": snap(node("q1", "DE")),
		"n": snap(node("n1", "DE")),
	}
	pool, err := buildAccountRoutePoolHops(cfg.Accounts, snapshots, nil, nil, hopsOf(subs))
	if err != nil {
		t.Fatal(err)
	}
	options, _, err := (&liveAngel{}).routeOptionsWithLoggerChecked(desiredState{cfg: cfg, pool: pool}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := options.AccountRoute["alice"]
	wantNodes := []string{"p1", "p2", "q1", "n1"}
	gotNodes := make([]string, 0, len(options.RouteNodes[key]))
	for _, raw := range options.RouteNodes[key] {
		name, _ := raw["name"].(string)
		gotNodes = append(gotNodes, name)
	}
	if !reflect.DeepEqual(gotNodes, wantNodes) {
		t.Fatalf("route node order = %v, want %v", gotNodes, wantNodes)
	}
	wantPre := [][]string{pPre, pPre, qPre, nil}
	wantPost := [][]string{pPost, pPost, qPost, nil}
	if !reflect.DeepEqual(options.NodePrePool[key], wantPre) || !reflect.DeepEqual(options.NodePostPool[key], wantPost) {
		t.Fatalf("node pools = pre %v post %v", options.NodePrePool[key], options.NodePostPool[key])
	}
	if !reflect.DeepEqual(options.NodePre[key], []string{"", "", qPre[0], ""}) || !reflect.DeepEqual(options.NodePost[key], []string{"", "", qPost[0], ""}) {
		t.Fatalf("scalar node mappings = pre %v post %v", options.NodePre[key], options.NodePost[key])
	}
}

func TestHopOptionsGlobalPostPoolMapsEveryAccountAndKeepsScalarCompatibility(t *testing.T) {
	globalPost := config.HopList{"socks5://global-one.example:1080", "http://global-two.example:8080"}
	cfg := config.Default()
	cfg.Chain.PostSOCKS5 = globalPost
	cfg.Accounts = []config.Account{
		{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"},
		{Login: "bob", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("account-only global post pool rejected: %v", err)
	}
	pool, err := buildAccountRoutePool(cfg.Accounts, map[string]*subscription.Snapshot{"p": snap(node("one", "DE"))}, nil, globalPost)
	if err != nil {
		t.Fatal(err)
	}
	options, _, err := (&liveAngel{}).routeOptionsWithLoggerChecked(desiredState{cfg: cfg, pool: pool}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.GlobalPost != "" {
		t.Fatalf("multi-hop global fallback = %q, want empty", options.GlobalPost)
	}
	wantRoutes := make(map[string][]string, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		key := options.AccountRoute[account.Login]
		if key == "" {
			t.Fatalf("%s has no AccountRoute mapping", account.Login)
		}
		wantRoutes[key] = globalPost
	}
	if !reflect.DeepEqual(options.RoutePostPool, wantRoutes) {
		t.Fatalf("global inheritance route pools = %v, want %v", options.RoutePostPool, wantRoutes)
	}

	// A single global hop retains the legacy scalar option and route keys;
	// the additive pool maps may be present but remain empty.
	legacy := config.Default()
	legacy.Chain.PreSOCKS5 = config.HopList{"socks5://global-pre.example:1080"}
	legacy.Chain.PostSOCKS5 = config.HopList{"socks5://global-post.example:1080"}
	legacy.Accounts = []config.Account{{Login: "carol", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"}}
	legacyPool, err := buildAccountRoutePool(legacy.Accounts, map[string]*subscription.Snapshot{"p": snap(node("one", "DE"))}, legacy.Chain.PreSOCKS5, legacy.Chain.PostSOCKS5)
	if err != nil {
		t.Fatal(err)
	}
	legacyOptions, _, err := (&liveAngel{}).routeOptionsWithLoggerChecked(desiredState{cfg: legacy, pool: legacyPool}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := legacyOptions.AccountRoute["carol"]
	if legacyOptions.GlobalPost != legacy.Chain.PostSOCKS5[0] || legacyOptions.RoutePre[legacyKey] != legacy.Chain.PreSOCKS5[0] || legacyOptions.RoutePost[legacyKey] != legacy.Chain.PostSOCKS5[0] {
		t.Fatalf("legacy scalar values changed: global=%q pre=%q post=%q", legacyOptions.GlobalPost, legacyOptions.RoutePre[legacyKey], legacyOptions.RoutePost[legacyKey])
	}
	if wantKey := credentialRouteKey("p", "DE", legacy.Chain.PreSOCKS5[0], legacy.Chain.PostSOCKS5[0]); legacyKey != wantKey {
		t.Fatalf("legacy singleton route key = %q, want %q", legacyKey, wantKey)
	}
	if len(legacyOptions.RoutePrePool) != 0 || len(legacyOptions.RoutePostPool) != 0 || len(legacyOptions.NodePrePool) != 0 || len(legacyOptions.NodePostPool) != 0 {
		t.Fatalf("unexpected additive pools for scalar chains: route pre=%v post=%v node pre=%v post=%v", legacyOptions.RoutePrePool, legacyOptions.RoutePostPool, legacyOptions.NodePrePool, legacyOptions.NodePostPool)
	}
}
