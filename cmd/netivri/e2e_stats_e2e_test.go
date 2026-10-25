//go:build e2e

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// TestE2EStatsAndAffinity runs the daemon with one account that may use two
// nodes of a country: every connection to one site leaves through the same
// node, the daemon reports the binding, and after `down` (which flushes the
// store) `stats` shows the dials of exactly that node.
func TestE2EStatsAndAffinity(t *testing.T) {
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
	n1 := accountsSocksNode("n1", "DE", first)
	n1.Name = "First Node"
	n2 := accountsSocksNode("n2", "DE", second)
	n2.Name = "Second Node"
	accountsSaveSnapshot(t, h.dir, "p1", []subscription.Node{n1, n2})

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
	// Push at least 64 KiB through one more connection in both directions so
	// the per-account bytes counters clear the 16 KiB window gate. That payload
	// connection also counts as one connection everywhere below.
	payloadConn := accountsClientPayload(t, listen, "alice", pass, backend, 64<<10)
	// The payload connection also counts: one extra dial on top of the loop.
	totalConns := connections + 1
	// The same site (the loopback address) used one node for all of them.
	a, b := first.count.Load(), second.count.Load()
	if a+b != int64(totalConns) || (a != int64(totalConns) && b != int64(totalConns)) {
		t.Fatalf("connections were split across nodes: n1=%d n2=%d, want all %d on one", a, b, totalConns)
	}
	bound := "p1/n1"
	if b == int64(totalConns) {
		bound = "p1/n2"
	}

	// The throughput poller only records account bytes while the connection's
	// tracker is open, so keep the payload connection alive until the stats
	// store reports non-zero bytes in both directions.
	deadline := time.Now().Add(30 * time.Second)
	var bytesUp, bytesDown float64
	for {
		statsCode, statsOut, _ := daemonRun(t, h.dir, "stats", "--by", "account", "--account", "alice", "--since", "1h")
		if statsCode == 0 {
			items, _ := daemonData(t, statsOut)["items"].([]any)
			if len(items) == 1 {
				acc, _ := items[0].(map[string]any)
				bytesUp, _ = acc["bytes_up"].(float64)
				bytesDown, _ = acc["bytes_down"].(float64)
				if bytesUp > 0 && bytesDown > 0 {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("account bytes not recorded within 30s: bytes_up=%v bytes_down=%v (the throughput poller must see the open connection's tracker and the store must flush it)", bytesUp, bytesDown)
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = payloadConn.Close()

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
	wantNames := map[string]string{"p1/n1": "First Node", "p1/n2": "Second Node"}
	var dialsOfBound float64
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["name"] != wantNames[item["label"].(string)] {
			t.Errorf("name of %v = %v, want %v", item["label"], item["name"], wantNames[item["label"].(string)])
		}
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
	if dialsOfBound != float64(totalConns) {
		t.Errorf("%s recorded %v dials, want %d", bound, dialsOfBound, totalConns)
	}
	assertNoSecrets(t, stdout, stderr, pass, hash)

	code, stdout, stderr = daemonRun(t, h.dir, "stats", "--by", "account", "--since", "1h")
	if code != 0 {
		t.Fatalf("stats --by account exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	items, _ = daemonData(t, stdout)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("account items = %v, want only alice", items)
	}
	acc, _ := items[0].(map[string]any)
	if acc["login"] != "alice" {
		t.Fatalf("account item = %v, want login alice", acc)
	}
	// Each connection is one logical dial, so the bound node must have seen
	// exactly totalConns dials (loop + payload connection).
	if dials, _ := acc["dials"].(float64); dials != float64(totalConns) {
		t.Errorf("alice dials = %v, want %d", acc["dials"], totalConns)
	}
	if acc["dial_ok_pct"] != 100.0 {
		t.Errorf("alice dial_ok_pct = %v, want 100", acc["dial_ok_pct"])
	}
	if nodes, _ := acc["nodes"].(float64); nodes < 1 {
		t.Errorf("alice nodes = %v, want at least 1", acc["nodes"])
	}
	assertNoSecrets(t, stdout, stderr, pass, hash)
}

// accountsClientPayload connects through the SOCKS proxy like accountsClient
// and exchanges a payload of the given size with the echo target in both
// directions. It returns the still-open connection so the caller can keep the
// connection's tracker alive while the throughput poller records the traffic;
// closing is the caller's responsibility.
func accountsClientPayload(t *testing.T, proxy, user, pass, target string, size int) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		t.Fatalf("SOCKS method %v: %v", method, err)
	}
	if _, err = c.Write(append([]byte{1, byte(len(user))}, append(append([]byte(user), byte(len(pass))), []byte(pass)...)...)); err != nil {
		t.Fatal(err)
	}
	var authReply [2]byte
	if _, err = io.ReadFull(c, authReply[:]); err != nil || authReply[1] != 0 {
		t.Fatalf("auth reply %v: %v", authReply, err)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("target is not IPv4: %s", target)
	}
	var port uint16
	if _, err = fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	request := append([]byte{5, 1, 0, 1}, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = c.Write(request); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		t.Fatalf("CONNECT reply %v: %v", reply, err)
	}
	payload := bytes.Repeat([]byte{0xA5}, size)
	if _, err = c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, size)
	if _, err = io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo %d bytes: %v", size, err)
	}
	return c
}
