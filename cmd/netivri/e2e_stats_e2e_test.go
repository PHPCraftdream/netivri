//go:build e2e

package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// TestE2EStatsAndAffinity runs the daemon with one account that may use two
// nodes of a country: every connection to one site leaves through the same
// node, the daemon reports the binding, and after `down` (which flushes the
// store) `stats` shows the dials of exactly that node.
func TestE2EStatsAndAffinity(t *testing.T) {
	testutil.RequireKtavLib(t)
	h := newDaemonHome(t)
	pass := "stats-e2e-password"
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	first := accountsServer(t)
	second := accountsServer(t)
	writeConfig(t, h.dir, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "p1", ExitCountry: "DE"}}
		cfg.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://p1.invalid/sub"}}
	})
	accountsSaveSnapshot(t, h.dir, "p1", []subscription.Node{
		accountsSocksNode("n1", "DE", first),
		accountsSocksNode("n2", "DE", second),
	})

	code, stdout, stderr := daemonRun(t, h.dir, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	up := daemonData(t, stdout)
	h.pid = daemonInt(t, up, "pid")
	listen, _ := up["listen"].(string)
	if listen == "" {
		t.Fatalf("up data = %v", up)
	}

	const connections = 3
	for i := 0; i < connections; i++ {
		accountsClient(t, listen, "alice", pass, backend)
	}
	// The same site (the loopback address) used one node for all of them.
	a, b := first.count.Load(), second.count.Load()
	if a+b != connections || (a != connections && b != connections) {
		t.Fatalf("connections were split across nodes: n1=%d n2=%d, want all %d on one", a, b, connections)
	}
	bound := "p1/n1"
	if b == connections {
		bound = "p1/n2"
	}

	code, stdout, stderr = daemonRun(t, h.dir, "status")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	status := daemonData(t, stdout)
	affinity, _ := status["affinity"].(map[string]any)
	if affinity == nil || affinity["bindings"] != float64(1) {
		t.Fatalf("status affinity = %v, want one binding", status["affinity"])
	}
	if info, _ := status["stats"].(map[string]any); info == nil || info["path"] == "" {
		t.Fatalf("status stats = %v", status["stats"])
	}
	assertNoSecrets(t, stdout, stderr, pass, hash)

	// down closes the core first and flushes the store before exiting.
	code, stdout, stderr = daemonRun(t, h.dir, "down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}

	code, stdout, stderr = daemonRun(t, h.dir, "stats", "--since", "1h")
	if code != 0 {
		t.Fatalf("stats exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	items, _ := daemonData(t, stdout)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("stats items = %v, want both nodes", items)
	}
	var dialsOfBound float64
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		dials, _ := item["dials"].(float64)
		if item["label"] == bound {
			dialsOfBound = dials
			if item["dial_ok_pct"] != 100.0 {
				t.Errorf("dial_ok_pct of %v = %v, want 100", item["label"], item["dial_ok_pct"])
			}
			continue
		}
		if dials != 0 {
			t.Errorf("%v recorded %v dials, want none: the site was bound elsewhere", item["label"], dials)
		}
	}
	if dialsOfBound != connections {
		t.Errorf("%s recorded %v dials, want %d", bound, dialsOfBound, connections)
	}
	assertNoSecrets(t, stdout, stderr, pass, hash)
}
