package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// fetchMap dispatches per-URL canned results; errors must be *cli.Error.
func fetchMap(t *testing.T, results map[string]any, got *[]fetch.Options) func(context.Context, string, fetch.Options) (*fetch.Result, error) {
	t.Helper()
	return func(_ context.Context, url string, o fetch.Options) (*fetch.Result, error) {
		if got != nil {
			*got = append(*got, o)
		}
		switch v := results[url].(type) {
		case string:
			return &fetch.Result{Body: []byte(v), Status: 200}, nil
		case error:
			return nil, v
		default:
			t.Fatalf("no canned result for %q", url)
			return nil, nil
		}
	}
}

func TestSubUpdateOne(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, out := testApp(t, fetchMap(t, map[string]any{"https://a.example/sub": testBodyFI}, nil))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	if err := app.update([]string{"--home", app.home, "a"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	data := decodeData(t, out)
	results, _ := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", data["results"])
	}
	r := results[0].(map[string]any)
	if r["name"] != "a" || r["ok"] != true || r["nodes"] != float64(1) || r["error"] != nil {
		t.Errorf("result = %v", r)
	}
	snap, err := app.store().Load("a")
	if err != nil {
		t.Fatalf("store load: %v", err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].Country != "FI" || !snap.FetchedAt.Equal(testNow) {
		t.Errorf("snapshot not updated: %d nodes, %v", len(snap.Nodes), snap.FetchedAt)
	}
}

func TestSubUpdateAll(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, out := testApp(t, fetchMap(t, map[string]any{
		"https://a.example/sub": testBody,
		"https://b.example/sub": testBodyFI,
	}, nil))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	if err := app.update([]string{"--home", app.home, "--all"}); err != nil {
		t.Fatalf("update --all: %v", err)
	}
	data := decodeData(t, out)
	results, _ := data["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v, want 2 in config order", data["results"])
	}
	if results[0].(map[string]any)["name"] != "a" || results[1].(map[string]any)["name"] != "b" {
		t.Errorf("results out of config order: %v", results)
	}
}

func TestSubUpdateAllWithoutFlag(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, out := testApp(t, fetchMap(t, map[string]any{"https://a.example/sub": testBodyFI}, nil))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	if err := app.update([]string{"--home", app.home}); err != nil {
		t.Fatalf("update: %v", err)
	}
	data := decodeData(t, out)
	results, _ := data["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v, want every subscription", data["results"])
	}
}

func TestSubUpdatePartialFailure(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, out := testApp(t, fetchMap(t, map[string]any{
		"https://a.example/sub": testBodyFI,
		"https://b.example/sub": &cli.Error{Code: "FETCH_HTTP_STATUS", Message: "unexpected HTTP status 403", Exit: cli.ExitNetwork},
	}, nil))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	// Old snapshot for b: a failed update must leave it untouched.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	oldSnap := subscription.Snapshot{FetchedAt: old}
	res, err := subscription.ParseDetailed([]byte(testBody))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	oldSnap.Nodes, oldSnap.Skipped = res.Nodes, res.Skipped.Banner+res.Skipped.Invalid
	if err := app.store().Save("b", oldSnap); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = app.update([]string{"--home", app.home, "--all"})
	assertCode(t, err, codeSubUpdateFailed, cli.ExitNetwork)

	// Results are still printed on stdout despite the failure.
	data := decodeData(t, out)
	results, _ := data["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v", data["results"])
	}
	first := results[0].(map[string]any)
	if first["ok"] != true {
		t.Errorf("first result = %v, want ok", first)
	}
	second := results[1].(map[string]any)
	if second["ok"] != false || second["nodes"] != nil {
		t.Errorf("second result = %v", second)
	}
	e, ok := second["error"].(map[string]any)
	if !ok || e["code"] != "FETCH_HTTP_STATUS" {
		t.Errorf("second error = %v", second["error"])
	}
	// Old snapshot untouched.
	snap, lerr := app.store().Load("b")
	if lerr != nil {
		t.Fatalf("store load: %v", lerr)
	}
	if !snap.FetchedAt.Equal(old) || len(snap.Nodes) != len(oldSnap.Nodes) {
		t.Errorf("failed update changed the stored snapshot: %v, %d nodes", snap.FetchedAt, len(snap.Nodes))
	}
}

func TestSubUpdateUnknownName(t *testing.T) {
	testutil.RequireKtavLib(t)
	app, _ := testApp(t, fakeBody(testBodyFI))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	err := app.update([]string{"--home", app.home, "nosuch"})
	assertCode(t, err, codeSubNotFound, cli.ExitNotFound)
}

func TestSubUpdateNameAndAllConflict(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBodyFI))
	err := app.update([]string{"--home", app.home, "a", "--all"})
	assertCode(t, err, "USAGE", cli.ExitUsage)
}

// Proxy selection on update: the subscription's via beats fetch.proxy.
func TestSubUpdateProxySelection(t *testing.T) {
	testutil.RequireKtavLib(t)
	var got []fetch.Options
	app, _ := testApp(t, fetchMap(t, map[string]any{
		"https://a.example/sub": testBodyFI,
		"https://b.example/sub": testBodyFI,
	}, &got))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub", Via: "socks5://127.0.0.1:1080"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	cfg, err := app.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Fetch.Proxy = "http://127.0.0.1:8080"
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := app.update([]string{"--home", app.home, "--all"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d fetches, want 2", len(got))
	}
	if got[0].Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("sub via = %q, want the per-subscription via", got[0].Proxy)
	}
	if got[1].Proxy != "http://127.0.0.1:8080" {
		t.Errorf("proxy = %q, want fetch.proxy", got[1].Proxy)
	}
}

// Update results must never carry the subscription URL.
func TestSubUpdateOutputHasNoURL(t *testing.T) {
	testutil.RequireKtavLib(t)
	secret := "token=topsecret123"
	app, out := testApp(t, fetchMap(t, map[string]any{"https://a.example/sub?" + secret: testBodyFI}, nil))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub?" + secret})
	if err := app.update([]string{"--home", app.home, "--all"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("stdout contains the subscription secret: %s", out.String())
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON object: %v", err)
	}
}
