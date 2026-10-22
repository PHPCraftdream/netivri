package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// seedCountriesApp creates an app with a seeded config and returns it, the
// stdout buffer and its subscription store.
func seedCountriesApp(t *testing.T, subs ...config.Subscription) (*subApp, *bytes.Buffer, *subscription.Store) {
	t.Helper()
	app, out := testApp(t, nil)
	seedConfig(t, app, subs...)
	return app, out, app.store()
}

func TestSubCountriesStoredSnapshot(t *testing.T) {
	app, out, store := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://secret.invalid/token"})
	nodes := []subscription.Node{
		{ID: "1", Country: "DE", Raw: map[string]any{"n": 1}},
		{ID: "2", Country: "DE", Raw: map[string]any{"n": 2}},
		{ID: "3", Country: "NL", Raw: map[string]any{"n": 3}},
		{ID: "4", Raw: map[string]any{"n": 4}},
	}
	if err := store.Save("p1", subscription.Snapshot{FetchedAt: testNow, Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	if err := app.countries([]string{"p1"}); err != nil {
		t.Fatal(err)
	}
	data := decodeData(t, out)
	if data["provider"] != "p1" {
		t.Fatalf("provider = %v", data["provider"])
	}
	if n, ok := data["nodes"].(float64); !ok || n != 4 {
		t.Fatalf("nodes = %v", data["nodes"])
	}
	raw, _ := json.Marshal(data["countries"])
	want := `[{"code":"DE","nodes":2},{"code":"NL","nodes":1}]`
	if string(raw) != want {
		t.Fatalf("countries = %s, want %s", raw, want)
	}
	if u, ok := data["unknown"].(float64); !ok || u != 1 {
		t.Fatalf("unknown = %v", data["unknown"])
	}
	if bytes.Contains(out.Bytes(), []byte("secret.invalid")) {
		t.Fatal("the subscription URL leaked into the output")
	}
}

func TestSubCountriesUnknownName(t *testing.T) {
	app, _, _ := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://x.invalid/s"})
	err := app.countries([]string{"nope"})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != codeSubNotFound || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestSubCountriesMissingSnapshotNulls(t *testing.T) {
	app, out, _ := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://x.invalid/s"})
	if err := app.countries([]string{"p1"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decodeData(t, out))
	want := `{"countries":null,"nodes":null,"provider":"p1","unknown":null}`
	if string(raw) != want {
		t.Fatalf("data = %s, want %s", raw, want)
	}
}

func TestSubCountriesAllProvidersAndUnion(t *testing.T) {
	app, out, store := seedCountriesApp(t,
		config.Subscription{Name: "b", URL: "https://x.invalid/b"},
		config.Subscription{Name: "a", URL: "https://x.invalid/a"},
		config.Subscription{Name: "c", URL: "https://x.invalid/c"},
	)
	if err := store.Save("b", subscription.Snapshot{FetchedAt: testNow, Nodes: []subscription.Node{
		{ID: "1", Country: "DE"}, {ID: "2", Country: "NL"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("a", subscription.Snapshot{FetchedAt: testNow, Nodes: []subscription.Node{
		{ID: "3", Country: "DE"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.countries(nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decodeData(t, out))
	// Config order; "c" has no snapshot and keeps nulls; the union sums DE
	// across b and a and is sorted by code.
	want := `{"countries":[{"code":"DE","nodes":2},{"code":"NL","nodes":1}],` +
		`"providers":[{"countries":[{"code":"DE","nodes":1},{"code":"NL","nodes":1}],"name":"b","nodes":2,"unknown":0},` +
		`{"countries":[{"code":"DE","nodes":1}],"name":"a","nodes":1,"unknown":0},` +
		`{"countries":null,"name":"c","nodes":null,"unknown":null}]}`
	if string(raw) != want {
		t.Fatalf("data = %s, want %s", raw, want)
	}
}

func TestSubCountriesFromURLDoesNotStore(t *testing.T) {
	fetchCalls := 0
	app, out := testApp(t, func(ctx context.Context, rawURL string, o fetch.Options) (*fetch.Result, error) {
		fetchCalls++
		if o.Proxy != "" {
			t.Errorf("unexpected proxy %q", o.Proxy)
		}
		return &fetch.Result{Body: []byte(testBody), Status: 200}, nil
	})
	seedConfig(t, app)
	cfgBefore, err := os.ReadFile(app.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.countries([]string{"--url", "https://secret.invalid/token"}); err != nil {
		t.Fatal(err)
	}
	data := decodeData(t, out)
	if n, ok := data["nodes"].(float64); !ok || n != 3 {
		t.Fatalf("nodes = %v", data["nodes"])
	}
	raw, _ := json.Marshal(data["countries"])
	want := `[{"code":"DE","nodes":1},{"code":"JP","nodes":1},{"code":"NL","nodes":1}]`
	if string(raw) != want {
		t.Fatalf("countries = %s, want %s", raw, want)
	}
	if u, ok := data["unknown"].(float64); !ok || u != 0 {
		t.Fatalf("unknown = %v", data["unknown"])
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d", fetchCalls)
	}
	// Nothing stored.
	entries, err := os.ReadDir(app.store().Dir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("store not empty: %v", entries)
	}
	// The config file is byte-for-byte untouched.
	cfgAfter, err := os.ReadFile(app.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Fatal("the config file changed")
	}
	if bytes.Contains(out.Bytes(), []byte("secret.invalid")) {
		t.Fatal("the subscription URL leaked into the output")
	}
}

// TestSubCountriesURLProxySelection: the fetch goes through --via when
// given, else through fetch.proxy, never direct when one is configured.
func TestSubCountriesURLProxySelection(t *testing.T) {
	var proxies []string
	app, out := testApp(t, func(ctx context.Context, rawURL string, o fetch.Options) (*fetch.Result, error) {
		proxies = append(proxies, o.Proxy)
		return &fetch.Result{Body: []byte(testBody), Status: 200}, nil
	})
	seedConfig(t, app)
	cfg, err := app.load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Fetch.Proxy = "socks5://127.0.0.1:1"
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := app.countries([]string{"--url", "https://secret.invalid/token"}); err != nil {
		t.Fatal(err)
	}
	if err := app.countries([]string{"--url", "https://secret.invalid/token", "--via", "http://127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 2 || proxies[0] != "socks5://127.0.0.1:1" || proxies[1] != "http://127.0.0.1:2" {
		t.Fatalf("proxies = %q, want [fetch.proxy, --via]", proxies)
	}
	if bytes.Contains(out.Bytes(), []byte("socks5://")) || bytes.Contains(out.Bytes(), []byte("http://127.0.0.1:2")) {
		t.Fatal("a proxy URL leaked into the output")
	}
}

func TestSubCountriesURLFetchErrorNoLeak(t *testing.T) {
	app, _ := testApp(t, func(ctx context.Context, rawURL string, o fetch.Options) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_HTTP_STATUS", Message: "unexpected HTTP status 403", Exit: cli.ExitNetwork}
	})
	seedConfig(t, app)
	err := app.countries([]string{"--url", "https://secret.invalid/token"})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "FETCH_HTTP_STATUS" {
		t.Fatalf("unexpected error: %#v", err)
	}
	if bytes.Contains([]byte(ce.Message), []byte("secret.invalid")) {
		t.Fatal("the subscription URL leaked into the error")
	}
}

func TestSubCountriesUsageErrors(t *testing.T) {
	app, out, _ := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://x.invalid/s"})
	// The arguments can carry a URL: cases are named, never printed.
	cases := []struct {
		name string
		args []string
	}{
		{"name with url", []string{"p1", "--url", "https://x.invalid/s"}},
		{"two names", []string{"p1", "p2"}},
		{"via without url", []string{"--via", "socks5://127.0.0.1:1"}},
		{"via with name", []string{"p1", "--via", "socks5://127.0.0.1:1"}},
		{"empty url", []string{"--url", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := app.countries(tc.args)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Exit != cli.ExitUsage {
				t.Fatal("not a usage error")
			}
			if out.Len() != 0 {
				t.Fatal("a usage error printed to stdout")
			}
		})
	}
}

// TestSubRmRefusesProviderInUse: sub rm fails while an account is bound.
func TestSubRmRefusesProviderInUse(t *testing.T) {
	app, out, _ := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://x.invalid/s"})
	cfg, err := app.load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = append(cfg.Accounts, config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "p1"})
	if err := app.store().Save("p1", subscription.Snapshot{FetchedAt: testNow, Nodes: []subscription.Node{{ID: "1", Country: "DE"}}}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := app.rm([]string{"p1"}); err == nil {
		t.Fatal("rm succeeded while the provider was in use")
	} else {
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "SUB_IN_USE" || ce.Exit != cli.ExitConflict {
			t.Fatalf("unexpected error: %#v", err)
		}
		if bytes.Contains([]byte(ce.Message), []byte("x.invalid")) {
			t.Fatalf("URL leaked in error: %s", ce.Message)
		}
	}
	// Nothing removed.
	after, err := app.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Subscriptions) != 1 || len(after.Accounts) != 1 {
		t.Fatalf("config changed: %+v", after)
	}
	if _, err := os.Stat(filepath.Join(app.home, "subscriptions", "p1.ktav")); err != nil {
		t.Fatalf("snapshot removed: %v", err)
	}
	out.Reset()
	if err := app.rm([]string{"unknown"}); err == nil {
		t.Fatal("rm of unknown subscription succeeded")
	}
}
