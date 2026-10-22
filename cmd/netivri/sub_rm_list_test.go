package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func seedStore(t *testing.T, app *subApp, name, body string) {
	t.Helper()
	res, err := subscription.ParseDetailed([]byte(body))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	snap := subscription.Snapshot{FetchedAt: testNow, Nodes: res.Nodes, Skipped: res.Skipped.Banner + res.Skipped.Invalid}
	if err := app.store().Save(name, snap); err != nil {
		t.Fatalf("store save %s: %v", name, err)
	}
}

func TestSubListRedactsURLs(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub/topsecret123?token=abcd", Via: "socks5://user:secretpw@127.0.0.1:1080"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	seedStore(t, app, "a", testBody)
	if err := app.list(); err != nil {
		t.Fatalf("list: %v", err)
	}
	s := out.String()
	for _, secret := range []string{"topsecret123", "token=abcd", "secretpw", "socks5", "/sub/"} {
		if strings.Contains(s, secret) {
			t.Errorf("sub list output leaks %q: %s", secret, s)
		}
	}
	if !strings.Contains(s, "a.example") {
		t.Errorf("host missing from output: %s", s)
	}
	data := decodeData(t, out)
	subs, _ := data["subscriptions"].([]any)
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %v", data["subscriptions"])
	}
	a := subs[0].(map[string]any)
	if a["name"] != "a" || a["host"] != "a.example" || a["via_set"] != true ||
		a["nodes"] != float64(3) || a["fetched_at"] != "2026-10-20T12:00:00Z" {
		t.Errorf("entry a = %v", a)
	}
	b := subs[1].(map[string]any)
	if b["via_set"] != false || b["nodes"] != nil || b["fetched_at"] != nil {
		t.Errorf("entry b = %v, want via_set false and null nodes/fetched_at", b)
	}
}

func TestSubListEmpty(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app)
	if err := app.list(); err != nil {
		t.Fatalf("list: %v", err)
	}
	data := decodeData(t, out)
	subs, _ := data["subscriptions"].([]any)
	if len(subs) != 0 {
		t.Errorf("subscriptions = %v, want empty", subs)
	}
}

func TestSubRm(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	seedStore(t, app, "a", testBody)
	seedStore(t, app, "b", testBodyFI)
	if err := app.rm([]string{"--home", app.home, "a"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	data := decodeData(t, out)
	if data["removed"] != "a" {
		t.Errorf("removed = %v", data["removed"])
	}
	cfg, err := config.Load(app.configPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "b" {
		t.Errorf("config after rm = %+v", cfg.Subscriptions)
	}
	if _, err := app.store().Load("a"); err == nil {
		t.Error("stored snapshot of the removed subscription still exists")
	}
	if _, err := app.store().Load("b"); err != nil {
		t.Errorf("snapshot of the kept subscription is gone: %v", err)
	}
}

func TestSubRmWithoutSnapshot(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	if err := app.rm([]string{"--home", app.home, "a"}); err != nil {
		t.Fatalf("rm without a stored snapshot: %v", err)
	}
}

func TestSubRmUnknown(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	err := app.rm([]string{"--home", app.home, "nosuch"})
	assertCode(t, err, codeSubNotFound, cli.ExitNotFound)
}
