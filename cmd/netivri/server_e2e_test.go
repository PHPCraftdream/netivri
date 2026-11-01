//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestE2EServerShadowsocksLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const pass = "server-e2e-account-password"

	upstream := accountsServer(t)
	backend := accountsEcho(t)
	accountHash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash test account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: accountHash, Provider: "route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "route", URL: "http://route.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "route", []subscription.Node{accountsSocksNode("route-node", "", upstream)})
	serverAngel := startUp(t, homeA, "--port", "0")
	accountsClient(t, serverAngel.Status().Data.Listen, "alice", pass, backend)

	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "shadowsocks", "--name", "edge", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "cipher=aes-128-gcm")
	if code != 0 {
		t.Fatalf("server add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass)
	waitStatusReload(t, ctx, homeA, 1)
	waitServerStatus(t, ctx, homeA, true)

	link := func() string {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, "link", "edge", "--format", "uri")
		if code != 0 {
			t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK || env.Data.Link == "" {
			t.Fatal("server link response did not contain a URI")
		}
		return env.Data.Link
	}
	var mu sync.RWMutex
	body := []byte(link() + "\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/subscription", func(w http.ResponseWriter, _ *http.Request) { mu.RLock(); defer mu.RUnlock(); _, _ = w.Write(body) })
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for local subscription")
	}
	serveDone := make(chan struct{})
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-serveDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(serveDone); _ = subServer.Serve(subListener) }()

	homeB := t.TempDir()
	code, out, stderr = accountsRun(t, homeB, "sub", "add", "http://"+subListener.Addr().String()+"/subscription", "--name", "edge")
	if code != 0 {
		t.Fatalf("local subscription add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	code, out, stderr = accountsRun(t, homeB, "account", "add", "alice", "--provider", "edge")
	if code != 0 {
		t.Fatalf("client account add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	clientAngel := startUp(t, homeB, "--port", "0")
	drive := func() {
		t.Helper()
		before := upstream.count.Load()
		accountsClient(t, clientAngel.Status().Data.Listen, "alice", pass, backend)
		if upstream.count.Load() <= before {
			t.Fatal("echo did not traverse the account node recorder")
		}
	}

	oldURI := string(body)
	if err := probeServerURI(ctx, oldURI, backend, "wrong-server-secret", true); err == nil {
		t.Fatal("wrong server secret reached the backend")
	}
	drive()
	if err := probeServerURI(ctx, oldURI, backend, "server-rotation-probe", false); err != nil {
		t.Fatal("generated URI failed before rotation")
	}
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotate failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass)
	waitStatusReload(t, ctx, homeA, 2)
	if err := probeServerURI(ctx, oldURI, backend, "server-rotation-probe", false); err == nil {
		t.Fatal("old URI still worked after rotation")
	}

	mu.Lock()
	body = []byte(link() + "\n")
	mu.Unlock()
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "edge")
	if code != 0 {
		t.Fatalf("subscription update failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeB, 1)
	drive()

	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass)
	waitStatusReload(t, ctx, homeA, 3)
	waitServerStatus(t, ctx, homeA, false)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("removed server port remains open: %v", err)
	}
}

func serverRun(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	return accountsRun(t, home, append([]string{"server"}, args...)...)
}

func waitServerStatus(t *testing.T, ctx context.Context, home string, wantPresent bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(t, home, "status")
		if code != 0 {
			t.Fatalf("status failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		var env struct {
			Data struct {
				Servers []struct {
					Name           string `json:"name"`
					Protocol       string `json:"protocol"`
					Listen         string `json:"listen"`
					Connections    int    `json:"connections"`
					DroppedClients int    `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		present := false
		for _, server := range env.Data.Servers {
			if server.Name == "edge" {
				present = true
				if wantPresent && (server.Protocol != "shadowsocks" || server.Listen == "" || server.Connections < 0 || server.DroppedClients < 0) {
					t.Fatal("edge server status is incomplete")
				}
			}
		}
		assertNoSecrets(t, out, stderr, "server-e2e-account-password")
		if present == wantPresent {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("edge server status did not become present=%t", wantPresent)
		case <-ticker.C:
		}
	}
}

func waitStatusReload(t *testing.T, ctx context.Context, home string, count uint64) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		st := liveStatusOf(t, home)
		if st.Data.Reload.Count >= count && st.Data.Reload.LastError == "" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("angel reload count did not reach %d", count)
		case <-ticker.C:
		}
	}
}

func waitServerPort(ctx context.Context, port int, wantOpen bool) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", addr)
		open := err == nil
		if conn != nil {
			_ = conn.Close()
		}
		if open == wantOpen {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("port state did not become %t", wantOpen)
		case <-ticker.C:
		}
	}
}

func probeServerURI(ctx context.Context, uri, target, payload string, wrongSecret bool) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		return fmt.Errorf("URI parsing failed")
	}
	m := nodes[0].Raw
	server := nodes[0].Server
	cipher, _ := m["cipher"].(string)
	password, _ := m["password"].(string)
	port := nodes[0].Port
	if server == "" || cipher == "" || password == "" || port < 1 {
		return fmt.Errorf("URI did not parse as Shadowsocks")
	}
	if wrongSecret {
		password = "known-incorrect-server-password"
	}
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "server-e2e-client", Server: server, Port: port, Password: password, Cipher: cipher})
	if err != nil {
		return err
	}
	defer client.Close()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	dstPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(dstPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return fmt.Errorf("unexpected echo")
	}
	return nil
}
