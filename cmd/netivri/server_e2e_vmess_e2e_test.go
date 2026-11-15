//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

var errE2EVMessRejected = errors.New("VMess request rejected")

func TestE2eVMessLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eVMessState{ctx: ctx}
	setupVMessServerA(t, st)
	readVMessLinks(t, st)
	subAddr := startVMessSubscriptions(t, st)
	setupVMessClientB(t, st, "http://"+subAddr)
	checkVMessInitialRoutes(t, st)
	oldAliceLink := st.currentLinks["alice"]
	checkVMessAliceFilter(t, st, oldAliceLink)
	rotateVMessAlice(t, st, oldAliceLink)
	refreshVMessLinks(t, st)
	restartVMessServer(t, st)
	removeVMessEdge(t, st)
}

// e2eVMessState is the shared state of TestE2eVMessLifecycle. allSecrets grows as
// URIs are created; the cleanups read the secret lists at teardown, and the
// subscription handlers read currentLinks at request time.
type e2eVMessState struct {
	ctx                       context.Context
	alicePass                 string
	bobPass                   string
	aliceHash                 string
	bobHash                   string
	backend                   string
	aliceUp                   *accountsUpstream
	bobUp                     *accountsUpstream
	homeA                     string
	homeB                     string
	port                      int
	allSecrets                []string
	serverCleanupSecrets      []string
	clientAngelCleanupSecrets []string
	currentLinks              map[string]string
	mu                        sync.RWMutex
	serverAngel               *upProc
	initialServerAngel        *upProc
	clientAngel               *upProc
}

// setupVMessServerA writes A's fixture, starts A's angel and adds the VMess edge
// that alice and bob share.
func setupVMessServerA(t *testing.T, st *e2eVMessState) {
	t.Helper()
	const alicePass = "vmess-e2e-alice-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	const bobPass = "vmess-e2e-bob-password"     //nolint:gosec // G101: fixture password of a test account, not a credential
	st.alicePass, st.bobPass = alicePass, bobPass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice account password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob account password")
	}
	st.aliceHash, st.bobHash = aliceHash, bobHash
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: aliceHash, Provider: "alice-route"},
			{Login: "bob", Password: bobHash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", st.bobUp)})
	st.allSecrets = make([]string, 0, 4)
	st.allSecrets = append(st.allSecrets, alicePass, bobPass, aliceHash, bobHash)
	st.serverCleanupSecrets = append([]string(nil), st.allSecrets...)
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	st.initialServerAngel = st.serverAngel
	t.Cleanup(func() {
		st.initialServerAngel.Stop()
		assertNoSecrets(t, st.initialServerAngel.Stdout(), st.initialServerAngel.Stderr(), st.serverCleanupSecrets...)
	})

	st.port = freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "vmess", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eVMessWaitServerStatus(t, st.ctx, st.homeA, true, st.allSecrets...)
}

// readVMessLink returns the VMess URI of account and checks that it leaks no secret.
func readVMessLink(t *testing.T, st *e2eVMessState, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", "edge", "--account", account, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.alicePass, st.bobPass, st.aliceHash, st.bobHash)
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

// readVMessLinks reads both URIs and records their credentials for redaction.
func readVMessLinks(t *testing.T, st *e2eVMessState) {
	t.Helper()
	st.currentLinks = make(map[string]string, 2)
	for _, account := range []string{"alice", "bob"} {
		link := readVMessLink(t, st, account)
		st.currentLinks[account] = link
		st.allSecrets = append(st.allSecrets, e2eVMessCredentials(map[string]string{account: link})...)
		st.serverCleanupSecrets = append([]string(nil), st.allSecrets...)
	}
}

// startVMessSubscriptions serves each account's current URI on the loopback and
// returns the listener address.
func startVMessSubscriptions(t *testing.T, st *e2eVMessState) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/alice", func(w http.ResponseWriter, _ *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		_, _ = io.WriteString(w, st.currentLinks["alice"]+"\n")
	})
	mux.HandleFunc("/bob", func(w http.ResponseWriter, _ *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		_, _ = io.WriteString(w, st.currentLinks["bob"]+"\n")
	})
	subServer := &http.Server{
		Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second,
	}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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
	return subListener.Addr().String()
}

// setupVMessClientB imports both subscriptions into B, adds one account per
// provider and starts B's angel.
func setupVMessClientB(t *testing.T, st *e2eVMessState, baseURL string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", baseURL+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("client subscription add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.allSecrets...)
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("client account add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.allSecrets...)
	}
	st.clientAngelCleanupSecrets = append([]string(nil), st.allSecrets...)
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	t.Cleanup(func() {
		st.clientAngel.Stop()
		assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.clientAngelCleanupSecrets...)
	})
}

// driveVMess sends one echo through B as account and checks that only own
// recorded it.
func driveVMess(t *testing.T, st *e2eVMessState, account, pass string, own, other *accountsUpstream) {
	t.Helper()
	beforeOwn, beforeOther := own.count.Load(), other.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, account, pass, st.backend)
	if own.count.Load() != beforeOwn+1 || other.count.Load() != beforeOther {
		t.Fatalf("%s recorder routing mismatch: own %d->%d, other %d->%d", account, beforeOwn, own.count.Load(), beforeOther, other.count.Load())
	}
}

// checkVMessInitialRoutes checks that a wrong UUID is refused without reaching a
// recorder and that both accounts route to their own upstream.
func checkVMessInitialRoutes(t *testing.T, st *e2eVMessState) {
	t.Helper()
	if err := e2eVMessProbe(st.ctx, st.currentLinks["alice"], st.backend, "valid-before-wrong-uuid", false, false); err != nil {
		t.Fatalf("valid Alice link failed before wrong UUID: %v", err)
	}
	beforeWrongAlice, beforeWrongBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eVMessProbe(st.ctx, st.currentLinks["alice"], st.backend, "wrong-UUID-probe", true, true); !errors.Is(err, errE2EVMessRejected) {
		t.Fatalf("wrong UUID was not refused: %v", err)
	}
	if st.aliceUp.count.Load() != beforeWrongAlice || st.bobUp.count.Load() != beforeWrongBob {
		t.Fatal("wrong UUID request reached an account recorder")
	}
	driveVMess(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
	driveVMess(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
}

// checkVMessAliceFilter limits alice to 192.0.2.0/24 and checks that her old URI
// is refused while the allowlist is set, then routes again once it is cleared.
func checkVMessAliceFilter(t *testing.T, st *e2eVMessState, oldAliceLink string) {
	t.Helper()
	if err := e2eVMessProbe(st.ctx, oldAliceLink, st.backend, "valid-before-filter", false, false); err != nil {
		t.Fatalf("valid Alice link failed before filter: %v", err)
	}
	beforeRejected := st.aliceUp.count.Load()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	if err := e2eVMessProbe(st.ctx, oldAliceLink, st.backend, "disallowed-client-probe", false, true); !errors.Is(err, errE2EVMessRejected) {
		t.Fatalf("valid Alice URI not refused by allowlist: %v", err)
	}
	if st.aliceUp.count.Load() != beforeRejected {
		t.Fatal("disallowed Alice connection reached her recorder")
	}
	e2eVMessWaitDropped(t, st.ctx, st.homeA, 1, st.allSecrets...)
	driveVMess(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
	code, out, stderr = accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice client restriction failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	if err := e2eVMessProbe(st.ctx, oldAliceLink, st.backend, "restored-client-probe", false, false); err != nil {
		t.Fatalf("Alice link failed after clearing allowlist: %v", err)
	}
	if st.aliceUp.count.Load() != beforeRejected+1 {
		t.Fatal("restored Alice connection did not reach Alice recorder")
	}
}

// rotateVMessAlice rotates alice's UUID and checks that her old URI is refused
// while bob still routes.
func rotateVMessAlice(t *testing.T, st *e2eVMessState, oldAliceLink string) {
	t.Helper()
	if err := e2eVMessProbe(st.ctx, oldAliceLink, st.backend, "valid-before-rotation", false, false); err != nil {
		t.Fatalf("old Alice link failed immediately before rotation: %v", err)
	}
	beforeRotateAlice, beforeRotateBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotate failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	if err := e2eVMessProbe(st.ctx, oldAliceLink, st.backend, "rotated-secret-probe", false, true); !errors.Is(err, errE2EVMessRejected) {
		t.Fatalf("old Alice UUID not refused after rotation: %v", err)
	}
	if st.aliceUp.count.Load() != beforeRotateAlice || st.bobUp.count.Load() != beforeRotateBob {
		t.Fatal("old rotated UUID changed recorder counts")
	}
	driveVMess(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
}

// refreshVMessLinks reads the rotated URIs, publishes them to the subscription
// server, refreshes alice's subscription on B and drives both accounts.
func refreshVMessLinks(t *testing.T, st *e2eVMessState) {
	t.Helper()
	refreshedLinks := make(map[string]string, 2)
	for _, account := range []string{"alice", "bob"} {
		refreshedLinks[account] = readVMessLink(t, st, account)
	}
	st.allSecrets = append(st.allSecrets, e2eVMessCredentials(refreshedLinks)...)
	st.serverCleanupSecrets = append([]string(nil), st.allSecrets...)
	st.clientAngelCleanupSecrets = append([]string(nil), st.allSecrets...)
	st.mu.Lock()
	st.currentLinks = refreshedLinks
	st.mu.Unlock()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription update failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveVMess(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
	driveVMess(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
}

// restartVMessServer restarts A's angel, checks that the port closes and reopens,
// that Alice's link still works and that both accounts route afterwards.
func restartVMessServer(t *testing.T, st *e2eVMessState) {
	t.Helper()
	stableAliceLink := st.currentLinks["alice"]
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	st.serverAngel.Stop()
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.allSecrets...)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatalf("VMess port did not close after shutdown: %v", err)
	}
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	serverAngelCleanup := st.serverAngel
	serverAngelCleanupSecrets := append([]string(nil), st.allSecrets...)
	t.Cleanup(func() {
		serverAngelCleanup.Stop()
		assertNoSecrets(t, serverAngelCleanup.Stdout(), serverAngelCleanup.Stderr(), serverAngelCleanupSecrets...)
	})
	if err := e2eVMessProbe(st.ctx, stableAliceLink, st.backend, "restart-persistent-link", false, false); err != nil {
		t.Fatalf("same Alice link failed after restart: %v", err)
	}
	driveVMess(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
	driveVMess(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
}

// removeVMessEdge removes the VMess edge, checks that its port closes and stops
// both angels.
func removeVMessEdge(t *testing.T, st *e2eVMessState) {
	t.Helper()
	baselineReload := liveStatusOf(t, st.homeA).Data.Reload.Count
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, baselineReload+1)
	e2eVMessWaitServerStatus(t, st.ctx, st.homeA, false, st.allSecrets...)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatalf("removed VMess port remains open: %v", err)
	}
	st.serverAngel.Stop()
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.allSecrets...)
	st.clientAngel.Stop()
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.allSecrets...)
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
		return errors.New("VMess URI parsing failed")
	}
	node := nodes[0]
	uuid, _ := node.Raw["uuid"].(string)
	cipher, _ := node.Raw["cipher"].(string)
	if node.Server == "" || uuid == "" || cipher == "" || node.Port < 1 {
		return errors.New("VMess URI fields are incomplete")
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
	defer func() { _ = client.Close() }()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("parse echo endpoint: %w", err)
	}
	dstPort, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("parse echo port: %w", err)
	}
	if dstPort < 0 {
		return fmt.Errorf("echo port %d out of range", dstPort)
	}
	if dstPort > math.MaxUint16 {
		return fmt.Errorf("echo port %d out of range", dstPort)
	}
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(dstPort)})
	if err != nil {
		return fmt.Errorf("VMess request failed before payload I/O: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		if expectReject && probeCtx.Err() == nil {
			return fmt.Errorf("%w: %w", errE2EVMessRejected, err)
		}
		return fmt.Errorf("VMess write failed: %w", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		if expectReject && probeCtx.Err() == nil {
			return fmt.Errorf("%w: %w", errE2EVMessRejected, err)
		}
		return fmt.Errorf("VMess read failed: %w", err)
	}
	if expectReject {
		return errors.New("expected refusal but received successful echo")
	}
	if string(got) != payload {
		return errors.New("unexpected echo")
	}
	return nil
}

func e2eVMessWaitServerStatus(t *testing.T, ctx context.Context, home string, wantPresent bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
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
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "VMess server status wait expired")
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
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
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
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "VMess dropped status wait expired")
			t.Fatal("VMess disallowed-client drop counter did not increment")
		case <-ticker.C:
		}
	}
}
