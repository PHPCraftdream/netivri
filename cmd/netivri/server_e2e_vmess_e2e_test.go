//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
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

var e2eVMessRejectedError = errors.New("VMess request rejected")

func TestE2eVMessLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const alicePass = "vmess-e2e-alice-password"
	const bobPass = "vmess-e2e-bob-password"
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice account password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: aliceHash, Provider: "alice-route"},
			{Login: "bob", Password: bobHash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", bobUp)})
	allSecrets := []string{alicePass, bobPass, aliceHash, bobHash}
	serverCleanupSecrets := append([]string(nil), allSecrets...)
	serverAngel := startUp(t, homeA, "--port", "0")
	initialServerAngel := serverAngel
	t.Cleanup(func() {
		initialServerAngel.Stop()
		assertNoSecrets(t, initialServerAngel.Stdout(), initialServerAngel.Stderr(), serverCleanupSecrets...)
	})

	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "vmess", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eVMessWaitServerStatus(t, ctx, homeA, true, allSecrets...)

	currentLinks := make(map[string]string, 2)
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr := serverRun(t, homeA, "link", "edge", "--account", account, "--format", "uri")
		if code != 0 {
			t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, alicePass, bobPass, aliceHash, bobHash)
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK || env.Data.Link == "" {
			t.Fatal("server link response did not contain a URI")
		}
		currentLinks[account] = env.Data.Link
		allSecrets = append(allSecrets, e2eVMessCredentials(map[string]string{account: env.Data.Link})...)
		serverCleanupSecrets = append([]string(nil), allSecrets...)
	}

	var mu sync.RWMutex
	mux := http.NewServeMux()
	mux.HandleFunc("/alice", func(w http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		_, _ = io.WriteString(w, currentLinks["alice"]+"\n")
	})
	mux.HandleFunc("/bob", func(w http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		_, _ = io.WriteString(w, currentLinks["bob"]+"\n")
	})
	subServer := &http.Server{
		Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second,
	}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for local subscription")
	}
	subDone := make(chan struct{})
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() {
		defer close(subDone)
		_ = subServer.Serve(subListener)
	}()

	homeB := t.TempDir()
	baseURL := "http://" + subListener.Addr().String()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr = accountsRun(t, homeB, "sub", "add", baseURL+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("client subscription add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, allSecrets...)
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("client account add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, allSecrets...)
	}
	clientAngelCleanupSecrets := append([]string(nil), allSecrets...)
	clientAngel := startUp(t, homeB, "--port", "0")
	clientAngelCleanup := clientAngel
	t.Cleanup(func() {
		clientAngelCleanup.Stop()
		assertNoSecrets(t, clientAngelCleanup.Stdout(), clientAngelCleanup.Stderr(), clientAngelCleanupSecrets...)
	})
	drive := func(account, pass string, own, other *accountsUpstream) {
		t.Helper()
		beforeOwn, beforeOther := own.count.Load(), other.count.Load()
		accountsClient(t, clientAngel.Status().Data.Listen, account, pass, backend)
		if own.count.Load() != beforeOwn+1 || other.count.Load() != beforeOther {
			t.Fatalf("%s recorder routing mismatch: own %d->%d, other %d->%d", account, beforeOwn, own.count.Load(), beforeOther, other.count.Load())
		}
	}

	if err := e2eVMessProbe(ctx, currentLinks["alice"], backend, "valid-before-wrong-uuid", false, false); err != nil {
		t.Fatalf("valid Alice link failed before wrong UUID: %v", err)
	}
	beforeWrongAlice, beforeWrongBob := aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eVMessProbe(ctx, currentLinks["alice"], backend, "wrong-UUID-probe", true, true); !errors.Is(err, e2eVMessRejectedError) {
		t.Fatalf("wrong UUID was not refused: %v", err)
	}
	if aliceUp.count.Load() != beforeWrongAlice || bobUp.count.Load() != beforeWrongBob {
		t.Fatal("wrong UUID request reached an account recorder")
	}
	drive("bob", bobPass, bobUp, aliceUp)
	drive("alice", alicePass, aliceUp, bobUp)

	oldAliceLink := currentLinks["alice"]
	if err := e2eVMessProbe(ctx, oldAliceLink, backend, "valid-before-filter", false, false); err != nil {
		t.Fatalf("valid Alice link failed before filter: %v", err)
	}
	beforeRejected := aliceUp.count.Load()
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 2)
	if err := e2eVMessProbe(ctx, oldAliceLink, backend, "disallowed-client-probe", false, true); !errors.Is(err, e2eVMessRejectedError) {
		t.Fatalf("valid Alice URI not refused by allowlist: %v", err)
	}
	if aliceUp.count.Load() != beforeRejected {
		t.Fatal("disallowed Alice connection reached her recorder")
	}
	e2eVMessWaitDropped(t, ctx, homeA, 1, allSecrets...)
	drive("bob", bobPass, bobUp, aliceUp)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice client restriction failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 3)
	if err := e2eVMessProbe(ctx, oldAliceLink, backend, "restored-client-probe", false, false); err != nil {
		t.Fatalf("Alice link failed after clearing allowlist: %v", err)
	}
	if aliceUp.count.Load() != beforeRejected+1 {
		t.Fatal("restored Alice connection did not reach Alice recorder")
	}

	if err := e2eVMessProbe(ctx, oldAliceLink, backend, "valid-before-rotation", false, false); err != nil {
		t.Fatalf("old Alice link failed immediately before rotation: %v", err)
	}
	beforeRotateAlice, beforeRotateBob := aliceUp.count.Load(), bobUp.count.Load()
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotate failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 4)
	if err := e2eVMessProbe(ctx, oldAliceLink, backend, "rotated-secret-probe", false, true); !errors.Is(err, e2eVMessRejectedError) {
		t.Fatalf("old Alice UUID not refused after rotation: %v", err)
	}
	if aliceUp.count.Load() != beforeRotateAlice || bobUp.count.Load() != beforeRotateBob {
		t.Fatal("old rotated UUID changed recorder counts")
	}
	drive("bob", bobPass, bobUp, aliceUp)

	refreshedLinks := make(map[string]string, 2)
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr = serverRun(t, homeA, "link", "edge", "--account", account, "--format", "uri")
		if code != 0 {
			t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, alicePass, bobPass, aliceHash, bobHash)
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK || env.Data.Link == "" {
			t.Fatal("server link response did not contain a URI")
		}
		refreshedLinks[account] = env.Data.Link
	}
	allSecrets = append(allSecrets, e2eVMessCredentials(refreshedLinks)...)
	serverCleanupSecrets = append([]string(nil), allSecrets...)
	clientAngelCleanupSecrets = append([]string(nil), allSecrets...)
	mu.Lock()
	currentLinks = refreshedLinks
	mu.Unlock()
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription update failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeB, 1)
	drive("alice", alicePass, aliceUp, bobUp)
	drive("bob", bobPass, bobUp, aliceUp)

	stableAliceLink := currentLinks["alice"]
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	serverAngel.Stop()
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), allSecrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("VMess port did not close after shutdown: %v", err)
	}
	serverAngel = startUp(t, homeA, "--port", "0")
	serverAngelCleanup := serverAngel
	serverAngelCleanupSecrets := append([]string(nil), allSecrets...)
	t.Cleanup(func() {
		serverAngelCleanup.Stop()
		assertNoSecrets(t, serverAngelCleanup.Stdout(), serverAngelCleanup.Stderr(), serverAngelCleanupSecrets...)
	})
	if err := e2eVMessProbe(ctx, stableAliceLink, backend, "restart-persistent-link", false, false); err != nil {
		t.Fatalf("same Alice link failed after restart: %v", err)
	}
	drive("alice", alicePass, aliceUp, bobUp)
	drive("bob", bobPass, bobUp, aliceUp)
	baselineReload := liveStatusOf(t, homeA).Data.Reload.Count
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, baselineReload+1)
	e2eVMessWaitServerStatus(t, ctx, homeA, false, allSecrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("removed VMess port remains open: %v", err)
	}
	serverAngel.Stop()
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), allSecrets...)
	clientAngel.Stop()
	assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), allSecrets...)
}

func e2eVMessCredentials(links map[string]string) []string {
	var secrets []string
	for _, link := range links {
		nodes, err := subscription.Parse([]byte(link))
		if err != nil || len(nodes) != 1 {
			continue
		}
		if uuid, ok := nodes[0].Raw["uuid"].(string); ok && uuid != "" {
			secrets = append(secrets, uuid, link)
		}
	}
	return secrets
}

func e2eVMessProbe(ctx context.Context, uri, target, payload string, badUUID, expectReject bool) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		return fmt.Errorf("VMess URI parsing failed")
	}
	node := nodes[0]
	uuid, _ := node.Raw["uuid"].(string)
	cipher, _ := node.Raw["cipher"].(string)
	if node.Server == "" || uuid == "" || cipher == "" || node.Port < 1 {
		return fmt.Errorf("VMess URI fields are incomplete")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if badUUID {
		uuid = "00000000-0000-4000-8000-000000000001"
	}
	client, err := outbound.NewVmess(outbound.VmessOption{
		Name: "vmess-e2e-client", Server: node.Server, Port: node.Port, UUID: uuid,
		AlterID: 0, Cipher: cipher, Network: "tcp",
	})
	if err != nil {
		return fmt.Errorf("construct VMess client: %w", err)
	}
	defer client.Close()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("parse echo endpoint: %w", err)
	}
	dstPort, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("parse echo port: %w", err)
	}
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(dstPort)})
	if err != nil {
		return fmt.Errorf("VMess request failed before payload I/O: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		if expectReject && probeCtx.Err() == nil {
			return fmt.Errorf("%w: %v", e2eVMessRejectedError, err)
		}
		return fmt.Errorf("VMess write failed: %w", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		if expectReject && probeCtx.Err() == nil {
			return fmt.Errorf("%w: %v", e2eVMessRejectedError, err)
		}
		return fmt.Errorf("VMess read failed: %w", err)
	}
	if expectReject {
		return fmt.Errorf("expected refusal but received successful echo")
	}
	if string(got) != payload {
		return fmt.Errorf("unexpected echo")
	}
	return nil
}

func e2eVMessWaitServerStatus(t *testing.T, ctx context.Context, home string, wantPresent bool, secrets ...string) {
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
					Name        string   `json:"name"`
					Protocol    string   `json:"protocol"`
					Listen      string   `json:"listen"`
					Accounts    []string `json:"accounts"`
					Connections int      `json:"connections"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode VMess status: %v", err)
		}
		present := false
		for _, server := range env.Data.Servers {
			if server.Name != "edge" {
				continue
			}
			present = true
			if wantPresent && (server.Protocol != "vmess" || server.Listen == "" || len(server.Accounts) != 2 || server.Connections < 0) {
				t.Fatal("VMess server status is incomplete")
			}
		}
		assertNoSecrets(t, out, stderr, secrets...)
		if present == wantPresent {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, "VMess server status wait expired")
			t.Fatalf("VMess server status did not become present=%t", wantPresent)
		case <-ticker.C:
		}
	}
}

func e2eVMessWaitDropped(t *testing.T, ctx context.Context, home string, minimum uint64, secrets ...string) {
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
					DroppedClients uint64 `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode VMess drop status: %v", err)
		}
		assertNoSecrets(t, out, stderr, secrets...)
		for _, server := range env.Data.Servers {
			if server.Name == "edge" && server.DroppedClients >= minimum {
				return
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, "VMess dropped status wait expired")
			t.Fatal("VMess disallowed-client drop counter did not increment")
		case <-ticker.C:
		}
	}
}
