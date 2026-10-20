package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// Fixed clock for deterministic fetched_at assertions.
var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const testBody = `proxies:
  - {name: "🇩🇪 Frankfurt 1", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass, udp: true}
  - {name: "Tokyo 01", type: vmess, server: example.invalid, port: 443, uuid: "00000000-0000-0000-0000-000000000000", alterId: 0, cipher: auto, udp: true}
  - {name: "Troj [NL]", type: trojan, server: example.invalid, port: 443, password: pass, udp: true}
  - {name: "剩余流量：88 GB", type: ss, server: 127.0.0.1, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "Expire: 2026-01-01", type: ss, server: example.invalid, port: 0, cipher: aes-128-gcm, password: pass}
  - {name: "Broken", type: nope, server: example.invalid, port: 1}
  - {name: "🇩🇪 Frankfurt 1", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass, udp: true}
`

const testBodyFI = `proxies:
  - {name: "🇫🇮 Helsinki 1", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
`

func fakeBody(body string) func(context.Context, string, fetch.Options) (*fetch.Result, error) {
	return func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return &fetch.Result{Body: []byte(body), Status: 200}, nil
	}
}

func testApp(t *testing.T, fetchFn func(context.Context, string, fetch.Options) (*fetch.Result, error)) (*subApp, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	var out bytes.Buffer
	app := &subApp{
		fetch:  fetchFn,
		now:    func() time.Time { return testNow },
		stdout: &out,
		home:   home,
	}
	return app, &out
}

func seedConfig(t *testing.T, app *subApp, subs ...config.Subscription) {
	t.Helper()
	testutil.RequireKtavLib(t)
	cfg := config.Default()
	cfg.Subscriptions = subs
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

// decodeData unmarshals the success envelope printed to buf.
func decodeData(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, buf.String())
	}
	if !env.OK {
		t.Fatalf("ok = false: %s", buf.String())
	}
	return env.Data
}

func assertCode(t *testing.T, err error, code string, exit int) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %s", code)
	}
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not *cli.Error: %v", err)
	}
	if ce.Code != code {
		t.Fatalf("code = %q, want %q", ce.Code, code)
	}
	if ce.Exit != exit {
		t.Fatalf("exit = %d, want %d", ce.Exit, exit)
	}
}

func TestSubAddNew(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, out := testApp(t, fakeBody(testBody))
	err := app.add([]string{"--home", app.home, "https://provider.example/sub?token=secret123", "--name", "prov"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	data := decodeData(t, out)
	if data["name"] != "prov" || data["added"] != true || data["existing"] != false {
		t.Errorf("name/added/existing = %v/%v/%v", data["name"], data["added"], data["existing"])
	}
	if data["nodes"] != float64(3) || data["skipped"] != float64(3) {
		t.Errorf("nodes/skipped = %v/%v, want 3/3", data["nodes"], data["skipped"])
	}
	want := map[string]any{"DE": float64(1), "JP": float64(1), "NL": float64(1)}
	got, ok := data["countries"].(map[string]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("countries = %v, want %v", data["countries"], want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("countries[%s] = %v, want %v", k, got[k], v)
		}
	}
	cfg, err := config.Load(app.configPath())
	if err != nil {
		t.Fatalf("config must exist after add: %v", err)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "prov" ||
		cfg.Subscriptions[0].URL != "https://provider.example/sub?token=secret123" {
		t.Errorf("config subscriptions = %+v", cfg.Subscriptions)
	}
	snap, err := app.store().Load("prov")
	if err != nil {
		t.Fatalf("store load: %v", err)
	}
	if len(snap.Nodes) != 3 || !snap.FetchedAt.Equal(testNow) {
		t.Errorf("snapshot nodes/fetched_at = %d/%v", len(snap.Nodes), snap.FetchedAt)
	}
}

func TestSubAddIdempotent(t *testing.T) {
	testutil.RequireKtavLib(t)
	subURL := "https://provider.example/sub?token=secret123"
	app, out := testApp(t, fakeBody(testBody))
	if err := app.add([]string{"--home", app.home, subURL, "--name", "prov"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	second := testNow.Add(time.Hour)
	app.now = func() time.Time { return second }
	out.Reset()
	if err := app.add([]string{"--home", app.home, subURL, "--name", "other"}); err != nil {
		t.Fatalf("second add: %v", err)
	}
	data := decodeData(t, out)
	if data["existing"] != true || data["added"] != false || data["name"] != "prov" {
		t.Errorf("existing/added/name = %v/%v/%v", data["existing"], data["added"], data["name"])
	}
	cfg, err := config.Load(app.configPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Subscriptions) != 1 {
		t.Fatalf("config has %d subscriptions, want 1", len(cfg.Subscriptions))
	}
	snap, err := app.store().Load("prov")
	if err != nil {
		t.Fatalf("store load: %v", err)
	}
	if !snap.FetchedAt.Equal(second) {
		t.Errorf("fetched_at = %v, want the second clock time", snap.FetchedAt)
	}
}

func TestSubAddNameDerivation(t *testing.T) {
	subs := []config.Subscription{{Name: "a.example.com", URL: "https://a.example.com/1"}}
	got := deriveName("https://A.Example.com:8443/sub?token=x", subs)
	if got != "a.example.com-2" {
		t.Errorf("deriveName = %q, want %q", got, "a.example.com-2")
	}
	got = deriveName("https://Weird_Host~Example.COM/path", nil)
	if got != "weird_host-example.com" {
		t.Errorf("deriveName = %q, want %q", got, "weird_host-example.com")
	}
	long := strings.Repeat("a", 70) + ".example.com"
	got = deriveName("https://"+long+"/", nil)
	if len(got) != 64 {
		t.Errorf("deriveName length = %d, want 64", len(got))
	}
	if !validSubName(got) {
		t.Errorf("derived name %q is not a valid stored name", got)
	}
	// Suffixes must keep names within 64 characters.
	base := strings.Repeat("b", 64)
	taken := []config.Subscription{{Name: base, URL: "https://x/"}}
	got = deriveName("https://"+base+".example.com/", taken)
	if len(got) != 64 || got == base {
		t.Errorf("suffixed name = %q (len %d), want a distinct 64-char name", got, len(got))
	}
}

func TestSubAddNameConflict(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, _ := testApp(t, fakeBody(testBody))
	seedConfig(t, app, config.Subscription{Name: "prov", URL: "https://a.example/sub"})
	err := app.add([]string{"--home", app.home, "https://b.example/sub", "--name", "prov"})
	assertCode(t, err, codeSubExistsName, cli.ExitConflict)
	cfg, err := config.Load(app.configPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].URL != "https://a.example/sub" {
		t.Errorf("config changed on conflict: %+v", cfg.Subscriptions)
	}
}

func TestSubAddFetchFailureLeavesConfigUntouched(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, _ := testApp(t, func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "boom", Exit: cli.ExitNetwork}
	})
	err := app.add([]string{"--home", app.home, "https://provider.example/sub"})
	assertCode(t, err, "FETCH_FAILED", cli.ExitNetwork)
	if _, statErr := os.Stat(app.configPath()); !os.IsNotExist(statErr) {
		t.Errorf("config file must not be created on failure, stat err = %v", statErr)
	}
	if names, listErr := app.store().List(); listErr != nil || len(names) != 0 {
		t.Errorf("store must stay empty, names = %v err = %v", names, listErr)
	}
}

func TestSubAddInvalidName(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	err := app.add([]string{"--home", app.home, "https://provider.example/sub", "--name", "bad name!"})
	assertCode(t, err, "USAGE", cli.ExitUsage)
}

func TestSubAddInvalidProxy(t *testing.T) {
	testutil.RequireKtavLib(t)
	// Real fetch.Get: an invalid --via fails before any network activity.
	app, _ := testApp(t, fetch.Get)
	err := app.add([]string{"--home", app.home, "https://provider.example/sub", "--via", "ftp://proxy.example:1080"})
	assertCode(t, err, "FETCH_URL_INVALID", cli.ExitUsage)
	if _, statErr := os.Stat(app.configPath()); !os.IsNotExist(statErr) {
		t.Errorf("config file must not be created on failure, stat err = %v", statErr)
	}
}

// Proxy selection: --via beats fetch.proxy; otherwise fetch.proxy; with
// neither the fetcher sees Proxy == "" (explicit direct, no env proxy).
func TestSubAddProxySelection(t *testing.T) {
	testutil.RequireKtavLib(t)
	var got []fetch.Options
	app, _ := testApp(t, func(_ context.Context, _ string, o fetch.Options) (*fetch.Result, error) {
		got = append(got, o)
		return &fetch.Result{Body: []byte(testBodyFI), Status: 200}, nil
	})
	seedConfig(t, app)
	cfg, err := app.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Fetch.Proxy = "socks5://127.0.0.1:1080"
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := app.add([]string{"--home", app.home, "https://a.example/sub", "--via", "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("add with --via: %v", err)
	}
	if err := app.add([]string{"--home", app.home, "https://b.example/sub"}); err != nil {
		t.Fatalf("add without --via: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d fetches, want 2", len(got))
	}
	if got[0].Proxy != "http://127.0.0.1:8080" {
		t.Errorf("--via proxy = %q, want the explicit one", got[0].Proxy)
	}
	if got[1].Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("proxy = %q, want fetch.proxy", got[1].Proxy)
	}
}

func TestSubAddStoresSnapshotFields(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, _ := testApp(t, fakeBody(testBodyFI))
	if err := app.add([]string{"--home", app.home, "https://fi.example/sub"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cfg, err := config.Load(app.configPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	name := cfg.Subscriptions[0].Name
	if name != "fi.example" {
		t.Errorf("derived name = %q, want fi.example", name)
	}
	snap, err := app.store().Load(name)
	if err != nil {
		t.Fatalf("store load: %v", err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].Country != "FI" || snap.Skipped != 0 {
		t.Errorf("snapshot = %d nodes, country %q, skipped %d", len(snap.Nodes), snap.Nodes[0].Country, snap.Skipped)
	}
}
