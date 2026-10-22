package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestNodeListAll(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	seedStore(t, app, "a", testBody)
	seedStore(t, app, "b", testBodyFI)
	if err := app.nodeList([]string{"--home", app.home}); err != nil {
		t.Fatalf("node list: %v", err)
	}
	data := decodeData(t, out)
	if data["count"] != float64(4) {
		t.Errorf("count = %v, want 4", data["count"])
	}
	nodes, _ := data["nodes"].([]any)
	// Stable order: subscription order, then provider order.
	wantSubs := []string{"a", "a", "a", "b"}
	for i, w := range wantSubs {
		n := nodes[i].(map[string]any)
		if n["sub"] != w {
			t.Errorf("nodes[%d].sub = %v, want %v", i, n["sub"], w)
		}
		for _, key := range []string{"id", "name", "type", "server", "port", "country"} {
			if _, ok := n[key]; !ok {
				t.Errorf("nodes[%d] misses %q: %v", i, key, n)
			}
		}
	}
	if nodes[0].(map[string]any)["country"] != "DE" ||
		nodes[3].(map[string]any)["country"] != "FI" {
		t.Errorf("countries = %v / %v, want DE / FI", nodes[0].(map[string]any)["country"], nodes[3].(map[string]any)["country"])
	}
	// Credentials from the raw proxy mappings must never appear.
	for _, secret := range []string{"pass", "00000000-0000", "cipher"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("node list output leaks %q: %s", secret, out.String())
		}
	}
}

func TestNodeListFilters(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app,
		config.Subscription{Name: "a", URL: "https://a.example/sub"},
		config.Subscription{Name: "b", URL: "https://b.example/sub"},
	)
	seedStore(t, app, "a", testBody)
	seedStore(t, app, "b", testBodyFI)

	out.Reset()
	if err := app.nodeList([]string{"--home", app.home, "--sub", "b"}); err != nil {
		t.Fatalf("node list --sub: %v", err)
	}
	data := decodeData(t, out)
	if data["count"] != float64(1) {
		t.Errorf("--sub count = %v, want 1", data["count"])
	}

	out.Reset()
	if err := app.nodeList([]string{"--home", app.home, "--country", "de"}); err != nil {
		t.Fatalf("node list --country de: %v", err)
	}
	data = decodeData(t, out)
	if data["count"] != float64(1) {
		t.Errorf("--country de count = %v, want 1 (case-insensitive)", data["count"])
	}

	out.Reset()
	if err := app.nodeList([]string{"--home", app.home, "--sub", "a", "--country", "JP"}); err != nil {
		t.Fatalf("node list combined: %v", err)
	}
	data = decodeData(t, out)
	if data["count"] != float64(1) {
		t.Errorf("combined count = %v, want 1", data["count"])
	}
}

func TestNodeListBadCountry(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	for _, cc := range []string{"D", "GER", "D1", "ZZ", "XX"} {
		if err := app.nodeList([]string{"--home", app.home, "--country", cc}); err == nil {
			t.Errorf("--country %q: want a usage error", cc)
		} else {
			assertCode(t, err, "USAGE", cli.ExitUsage)
		}
	}
}

func TestNodeListAliveRejected(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	err := app.nodeList([]string{"--home", app.home, "--alive"})
	if err == nil {
		t.Fatal("--alive: want a usage error")
	}
	var ce *cli.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Message, "--alive is not available yet") {
		t.Errorf("--alive error = %v, want a not-available-yet usage error", err)
	}
}

func TestNodeListUnknownSub(t *testing.T) {
	app, _ := testApp(t, fakeBody(testBody))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	err := app.nodeList([]string{"--home", app.home, "--sub", "nosuch"})
	assertCode(t, err, codeSubNotFound, cli.ExitNotFound)
}

func TestNodeListWithoutSnapshots(t *testing.T) {
	app, out := testApp(t, fakeBody(testBody))
	seedConfig(t, app, config.Subscription{Name: "a", URL: "https://a.example/sub"})
	if err := app.nodeList([]string{"--home", app.home}); err != nil {
		t.Fatalf("node list: %v", err)
	}
	data := decodeData(t, out)
	if data["count"] != float64(0) {
		t.Errorf("count = %v, want 0", data["count"])
	}
}
