//go:build e2e

package main

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // G501: AnyTLS names its padding scheme by MD5 digest, a protocol field
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/anytls/padding"
	"github.com/metacubex/mihomo/transport/vmess"
	M "github.com/metacubex/sing/common/metadata"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

var errE2EAnyTLSRejected = errors.New("AnyTLS authentication or policy rejection")

func TestE2EAnyTLSRealCLILifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eAnyTLSLifecycle{ctx: ctx}
	setupAnyTLSServer(t, st)
	readAnyTLSLinks(t, st)
	subAddr := startAnyTLSSubscriptions(t, st)
	setupAnyTLSClient(t, st, subAddr)
	checkAnyTLSInitialRoutes(t, st)
	freshAlice := rotateAnyTLSAlice(t, st)
	checkAnyTLSSubscriptionUpdate(t, st)
	checkAnyTLSRestrictedClient(t, st, freshAlice)
	checkAnyTLSClearedClients(t, st)
	restartAnyTLSServer(t, st, freshAlice)
	checkAnyTLSAfterRestart(t, st)
	removeAnyTLSEdge(t, st)
}

// e2eAnyTLSLifecycle is the shared state of TestE2EAnyTLSRealCLILifecycle.
// secrets grow as links and passwords are created, so every helper redacts
// with the current list.
type e2eAnyTLSLifecycle struct {
	ctx                context.Context
	backend            string
	alicePass          string
	bobPass            string
	aliceUp            *accountsUpstream
	bobUp              *accountsUpstream
	homeA              string
	homeB              string
	port               int
	secrets            []string
	links              map[string]string
	linkMu             sync.RWMutex
	serverAngel        *upProc
	initialServerAngel *upProc
	clientAngel        *upProc
}

// setupAnyTLSServer writes A's fixture, each account on its own upstream, and
// adds the AnyTLS edge shared by alice and bob.
func setupAnyTLSServer(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	const alicePass = "anytls-e2e-alice-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	const bobPass = "anytls-e2e-bob-account-password"     //nolint:gosec // G101: fixture password of a test account, not a credential
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
	st.secrets = []string{alicePass, bobPass, aliceHash, bobHash}
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverAngel.Status())
	st.initialServerAngel = st.serverAngel
	st.port = freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "anytls", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eAnyTLSWaitStatus(t, st.ctx, st.homeA, true, st.secrets...)
}

// readAnyTLSLinks reads both server links and adds them and their passwords to
// the redaction list.
func readAnyTLSLinks(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	st.links = map[string]string{"alice": e2eAnyTLSLink(t, st.homeA, "alice"), "bob": e2eAnyTLSLink(t, st.homeA, "bob")}
	st.secrets = append(st.secrets, st.links["alice"], st.links["bob"])
	st.secrets = append(st.secrets, e2eAnyTLSPasswords(st.links)...)
}

// serveLoopbackSubscriptions serves handler on a loopback listener, stops it at
// cleanup and returns the listener address.
func serveLoopbackSubscriptions(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for local subscription")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(done); _ = server.Serve(listener) }()
	return listener.Addr().String()
}

// startAnyTLSSubscriptions serves both links as subscriptions on the loopback
// and returns the listener address.
func startAnyTLSSubscriptions(t *testing.T, st *e2eAnyTLSLifecycle) string {
	t.Helper()
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			st.linkMu.RLock()
			defer st.linkMu.RUnlock()
			_, _ = io.WriteString(w, st.links[account]+"\n")
		})
	}
	return serveLoopbackSubscriptions(t, mux)
}

// setupAnyTLSClient adds both subscriptions and their accounts to B and starts
// B's angel.
func setupAnyTLSClient(t *testing.T, st *e2eAnyTLSLifecycle, subAddr string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-edge"
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", "http://"+subAddr+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
	}
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	assertStatusSuccess(t, st.clientAngel.Status())
}

// restartAnyTLSClient restarts B after A changes: probes may be dropped or A's
// core replaced, leaving dead-node health and backoff cached on B.
func restartAnyTLSClient(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "down")
	if code != 0 {
		t.Fatalf("B down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.clientAngel.Stop()
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.secrets...)
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	assertStatusSuccess(t, st.clientAngel.Status())
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.secrets...)
}

// driveAnyTLSRoute echoes once through B as account and checks that the echo
// reached own and not other; with retry it keeps trying for 20 s.
func driveAnyTLSRoute(t *testing.T, st *e2eAnyTLSLifecycle, account, pass string, own, other *accountsUpstream, retry bool) {
	t.Helper()
	baseOwn, baseOther := own.count.Load(), other.count.Load()
	var retryDeadline time.Time
	if retry {
		retryDeadline = time.Now().Add(20 * time.Second)
	}
	var last error
	for {
		attemptCtx := st.ctx
		cancelAttempt := func() {}
		if retry {
			attemptCtx, cancelAttempt = context.WithDeadline(st.ctx, retryDeadline)
		}
		err := e2eAnyTLSClientEcho(attemptCtx, st.clientAngel.Status().Data.Listen, account, pass, st.backend)
		cancelAttempt()
		if err == nil {
			if own.count.Load() > baseOwn && other.count.Load() == baseOther {
				return
			}
			last = errors.New("recorder routing mismatch")
		} else {
			last = err
		}
		if !retry || !time.Now().Before(retryDeadline) {
			break
		}
		t.Logf("AnyTLS %s echo attempt failed: %v", account, last)
		select {
		case <-st.ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		if st.ctx.Err() != nil {
			break
		}
	}
	t.Logf("AnyTLS %s echo failure: %v; %s", account, last, e2eAnyTLSDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.secrets...))
	t.Fatalf("AnyTLS %s echo/routing failed", account)
}

// checkAnyTLSInitialRoutes checks that both accounts route to their own
// upstream, and that a wrong AnyTLS password is refused without reaching a
// recorder.
func checkAnyTLSInitialRoutes(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	baselineA, baselineB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	driveAnyTLSRoute(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, false)
	driveAnyTLSRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, false)
	if st.aliceUp.count.Load() != baselineA+1 || st.bobUp.count.Load() != baselineB+1 {
		t.Logf("initial recorder count mismatch; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("initial recorder increments mismatch")
	}
	if err := e2eAnyTLSWireProbe(st.ctx, st.links["alice"], "wrong-anytls-password", st.backend, false); !errors.Is(err, errE2EAnyTLSRejected) {
		t.Logf("wrong-password AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("wrong AnyTLS password was not rejected by the server")
	}
	if st.aliceUp.count.Load() != baselineA+1 || st.bobUp.count.Load() != baselineB+1 {
		t.Logf("failed-auth recorder count mismatch; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("failed AnyTLS authentication reached a recorder")
	}
}

// rotateAnyTLSAlice rotates alice's password, checks that the old link is
// refused and publishes the fresh link. It returns the fresh Alice link.
func rotateAnyTLSAlice(t *testing.T, st *e2eAnyTLSLifecycle) string {
	t.Helper()
	oldAlice := st.links["alice"]
	if err := e2eAnyTLSPositive(st.ctx, oldAlice, st.backend); err != nil {
		t.Logf("AnyTLS pre-rotation echo failure: %v; %s", err, e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("Alice link pre-rotation echo failed")
	}
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	oldPasswordAlice, oldPasswordBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eAnyTLSWireProbe(st.ctx, oldAlice, "", st.backend, false); !errors.Is(err, errE2EAnyTLSRejected) {
		t.Logf("rotated-password AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("old Alice password was not rejected after rotation")
	}
	if st.aliceUp.count.Load() != oldPasswordAlice || st.bobUp.count.Load() != oldPasswordBob {
		t.Logf("rotated-password recorder changed; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("old Alice password reached an account recorder")
	}
	freshAlice := e2eAnyTLSLink(t, st.homeA, "alice")
	st.secrets = append(st.secrets, freshAlice)
	st.secrets = append(st.secrets, e2eAnyTLSPasswords(map[string]string{"alice": freshAlice})...)
	st.linkMu.Lock()
	st.links["alice"] = freshAlice
	st.linkMu.Unlock()
	return freshAlice
}

// checkAnyTLSSubscriptionUpdate refreshes B's Alice subscription and checks
// that alice routes to A's upstream again.
func checkAnyTLSSubscriptionUpdate(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	driveAnyTLSRoute(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, false)
	if st.aliceUp.count.Load() != beforeAlice+1 || st.bobUp.count.Load() != beforeBob {
		t.Logf("refreshed Alice route mismatch; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("refreshed Alice subscription routed incorrectly")
	}
}

// checkAnyTLSRestrictedClient restricts alice to 192.0.2.0/24 and checks that,
// after B restarts, the disallowed client is refused while bob still routes.
func checkAnyTLSRestrictedClient(t *testing.T, st *e2eAnyTLSLifecycle, freshAlice string) {
	t.Helper()
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	restartAnyTLSClient(t, st)
	if err := e2eAnyTLSWireProbe(st.ctx, freshAlice, "", st.backend, true); !errors.Is(err, errE2EAnyTLSRejected) {
		t.Logf("allowed-client AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("disallowed Alice client was not refused")
	}
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() != beforeBob {
		t.Logf("disallowed route counter changed; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("disallowed client reached a recorder")
	}
	e2eAnyTLSWaitDropped(t, st.ctx, st.homeA, 1, st.secrets...)
	driveAnyTLSRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, true)
}

// checkAnyTLSClearedClients clears alice's client policy and checks that both
// accounts route again after B restarts.
func checkAnyTLSClearedClients(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	restartAnyTLSClient(t, st)
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	driveAnyTLSRoute(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, true)
	if st.aliceUp.count.Load() <= beforeAlice || st.bobUp.count.Load() != beforeBob {
		t.Logf("Alice route did not recover; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("Alice route did not recover after policy clear")
	}
	driveAnyTLSRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, true)
}

// restartAnyTLSServer stops A's angel, checks that the AnyTLS port closes,
// starts A again and checks that both server links are unchanged.
func restartAnyTLSServer(t *testing.T, st *e2eAnyTLSLifecycle, freshAlice string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("down failed: %s", serverErrorSummary(out, stderr))
	}
	st.serverAngel.Stop()
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.secrets...)
	st.initialServerAngel.Stop()
	assertNoSecrets(t, st.initialServerAngel.Stdout(), st.initialServerAngel.Stderr(), st.secrets...)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatalf("AnyTLS port stayed open after down: %v", err)
	}
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverAngel.Status())
	if err := waitServerPort(st.ctx, st.port, true); err != nil {
		t.Fatalf("AnyTLS port did not reopen: %v", err)
	}
	if e2eAnyTLSLink(t, st.homeA, "alice") != freshAlice {
		t.Logf("post-restart AnyTLS link changed; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("Alice server link changed across restart")
	}
	if e2eAnyTLSLink(t, st.homeA, "bob") != st.links["bob"] {
		t.Logf("post-restart Bob link changed; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("Bob server link changed across restart")
	}
}

// checkAnyTLSAfterRestart restarts B and checks that the same Alice
// subscription and Bob's route work again.
func checkAnyTLSAfterRestart(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	restartAnyTLSClient(t, st)
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	driveAnyTLSRoute(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, true)
	if st.aliceUp.count.Load() <= beforeAlice || st.bobUp.count.Load() != beforeBob {
		t.Logf("post-restart Alice routing failure; %s", e2eAnyTLSDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets...))
		t.Fatal("same Alice subscription failed after restart")
	}
	driveAnyTLSRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, true)
}

// removeAnyTLSEdge removes the AnyTLS edge, checks that its port closes and
// stops both angels.
func removeAnyTLSEdge(t *testing.T, st *e2eAnyTLSLifecycle) {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eAnyTLSWaitStatus(t, st.ctx, st.homeA, false, st.secrets...)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatalf("removed AnyTLS port remains open: %v", err)
	}
	st.serverAngel.Stop()
	st.clientAngel.Stop()
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.secrets...)
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.secrets...)
}

func e2eAnyTLSLink(t *testing.T, home, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, home, "link", "edge", "--account", account, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("server link response missing")
	}
	return env.Data.Link
}

func e2eAnyTLSClient(uri string) (*outbound.AnyTLS, error) {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		return nil, errors.New("AnyTLS URI parse failed")
	}
	n := nodes[0]
	password, _ := n.Raw["password"].(string)
	pin, _ := n.Raw["fingerprint"].(string)
	if n.Server == "" || n.Port < 1 || password == "" || pin == "" {
		return nil, errors.New("AnyTLS link missing pinned endpoint")
	}
	return outbound.NewAnyTLS(outbound.AnyTLSOption{
		Name: "anytls-e2e-client", Server: n.Server, Port: n.Port,
		Password: password, SNI: e2eAnyTLSString(n.Raw["sni"]), Fingerprint: pin,
		ClientFingerprint: "chrome", UDP: false, IdleSessionCheckInterval: 30,
	})
}
func e2eAnyTLSString(v any) string { s, _ := v.(string); return s }

func e2eAnyTLSPasswords(links map[string]string) []string {
	var passwords []string
	for _, link := range links {
		nodes, err := subscription.Parse([]byte(link))
		if err == nil && len(nodes) == 1 {
			if pass, ok := nodes[0].Raw["password"].(string); ok {
				passwords = append(passwords, pass)
			}
		}
	}
	return passwords
}

func e2eAnyTLSPositive(ctx context.Context, uri, target string) error {
	client, err := e2eAnyTLSClient(uri)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	host, pt, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("echo endpoint invalid")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return errors.New("echo port invalid")
	}
	if p < 0 {
		return errors.New("echo port invalid")
	}
	if p > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	clientCtx, clientCancel := context.WithTimeout(probeCtx, 4*time.Second)
	defer clientCancel()
	conn, err := client.DialContext(clientCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		return errors.New("AnyTLS positive dial failed")
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("anytls-real-cli-echo")
	if _, err = conn.Write(payload); err != nil {
		return errors.New("AnyTLS echo write failed")
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		return errors.New("AnyTLS echo failed")
	}
	return nil
}
func e2eAnyTLSClientEcho(ctx context.Context, proxy, user, pass, target string) error {
	deadline := time.Now().Add(3 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", proxy)
	if err != nil {
		return errors.New("client proxy dial")
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(deadline)
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return errors.New("SOCKS greeting write")
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS auth method missing")
	}
	userLen := len(user)
	if userLen > math.MaxUint8 {
		return errors.New("SOCKS auth credentials too long")
	}
	passLen := len(pass)
	if passLen > math.MaxUint8 {
		return errors.New("SOCKS auth credentials too long")
	}
	if _, err = c.Write(append([]byte{1, byte(userLen)}, append(append([]byte(user), byte(passLen)), []byte(pass)...)...)); err != nil {
		return errors.New("SOCKS auth write")
	}
	var authReply [2]byte
	if _, err = io.ReadFull(c, authReply[:]); err != nil || authReply[1] != 0 {
		return errors.New("SOCKS auth rejected")
	}
	host, pt, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("echo address invalid")
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return errors.New("echo host is not IPv4")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return errors.New("echo port invalid")
	}
	if p < 0 {
		return errors.New("echo port invalid")
	}
	if p > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	p16 := uint16(p)
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = binary.BigEndian.AppendUint16(req, p16)
	if _, err = c.Write(req); err != nil {
		return errors.New("SOCKS connect write")
	}
	var reply [10]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		return errors.New("SOCKS connect rejected")
	}
	payload := []byte("anytls-routed-echo")
	if _, err = c.Write(payload); err != nil {
		return errors.New("routed echo write")
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
		return errors.New("routed echo failed")
	}
	return nil
}
func e2eAnyTLSWireProbe(ctx context.Context, uri, passwordOverride, backend string, policy bool) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		return errors.New("AnyTLS URI parse failed")
	}
	n := nodes[0]
	password, _ := n.Raw["password"].(string)
	pin, _ := n.Raw["fingerprint"].(string)
	if passwordOverride != "" {
		password = passwordOverride
	}
	if password == "" || pin == "" {
		return errors.New("AnyTLS URI credentials or pin missing")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", net.JoinHostPort(n.Server, strconv.Itoa(n.Port)))
	if err != nil {
		return errors.New("AnyTLS TCP dial failed")
	}
	defer func() { _ = raw.Close() }()
	_ = raw.SetDeadline(time.Now().Add(4 * time.Second))
	tlsHost := e2eAnyTLSString(n.Raw["sni"])
	if tlsHost == "" {
		tlsHost = n.Server
	}
	tlsConn, err := vmess.StreamTLSConn(probeCtx, raw, &vmess.TLSConfig{Host: tlsHost, FingerPrint: pin, ClientFingerprint: "chrome"})
	if err != nil {
		return errors.New("pinned TLS handshake failed")
	}
	defer func() { _ = tlsConn.Close() }()
	digest := sha256.Sum256([]byte(password))
	if _, err = tlsConn.Write(append(append([]byte(nil), digest[:]...), 0, 0)); err != nil {
		return errors.New("AnyTLS auth prelude write failed")
	}
	if !policy {
		return e2eAnyTLSExpectRejected(tlsConn)
	}
	if err = e2eAnyTLSOpenStream(tlsConn, backend); err != nil {
		return err
	}
	return e2eAnyTLSReadPolicyResponse(tlsConn)
}

// e2eAnyTLSExpectRejected reads once after the auth prelude: a rejected
// credential must get no data, only a close or a reset.
func e2eAnyTLSExpectRejected(r io.Reader) error {
	var b [1]byte
	_, err := r.Read(b[:])
	if err == nil {
		return errors.New("rejected credential received data")
	}
	if e2eAnyTLSIsTimeout(err) {
		return errors.New("AnyTLS rejection timed out")
	}
	if !e2eAnyTLSIsReject(err) {
		return errors.New("AnyTLS auth rejection had unexpected class")
	}
	return fmt.Errorf("%w", errE2EAnyTLSRejected)
}

// e2eAnyTLSOpenStream sends the settings frame, opens stream 1 and sends the
// echo destination of the policy probe.
func e2eAnyTLSOpenStream(w io.Writer, backend string) error {
	settings := []byte(fmt.Sprintf("v=2\nclient=netivri-test\npadding-md5=%x", md5.Sum(padding.DefaultPaddingScheme))) //nolint:gosec // G401: the AnyTLS wire field padding-md5 is an MD5 digest by protocol
	if err := e2eAnyTLSWriteFrame(w, 4, 0, settings); err != nil {
		return errors.New("AnyTLS settings write failed")
	}
	if err := e2eAnyTLSWriteFrame(w, 1, 1, nil); err != nil {
		return errors.New("AnyTLS stream open failed")
	}
	var dest bytes.Buffer
	host, pt, err := net.SplitHostPort(backend)
	if err != nil {
		return errors.New("echo endpoint invalid")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return errors.New("echo port invalid")
	}
	if p < 0 {
		return errors.New("echo port invalid")
	}
	if p > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	if err = M.SocksaddrSerializer.WriteAddrPort(&dest, M.ParseSocksaddrHostPort(host, uint16(p))); err != nil {
		return errors.New("AnyTLS destination encode failed")
	}
	if err = e2eAnyTLSWriteFrame(w, 2, 1, dest.Bytes()); err != nil {
		return errors.New("AnyTLS destination write failed")
	}
	return nil
}

// e2eAnyTLSReadPolicyResponse reads stream 1 until FIN. A SYNACK followed by
// FIN means the policy refused the stream; a data-carrying open means it routed.
func e2eAnyTLSReadPolicyResponse(r io.Reader) error {
	seenSynAck, seenFin := false, false
	for !seenFin {
		var h [7]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			if e2eAnyTLSIsTimeout(err) {
				return errors.New("AnyTLS policy response timed out")
			}
			if e2eAnyTLSIsReject(err) {
				return errors.New("policy probe closed before SYNACK and FIN")
			}
			return errors.New("policy response read failed")
		}
		cmd, sid, nbytes := h[0], binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint16(h[5:])
		if nbytes > 4096 {
			return errors.New("AnyTLS response frame oversized")
		}
		payload := make([]byte, int(nbytes))
		if _, err := io.ReadFull(r, payload); err != nil {
			return errors.New("AnyTLS response frame incomplete")
		}
		if sid != 1 {
			continue
		}
		switch cmd {
		case 7:
			seenSynAck = true
		case 2:
			return errors.New("policy probe reached a routed stream")
		case 3:
			if !seenSynAck {
				return errors.New("policy FIN arrived before SYNACK")
			}
			seenFin = true
		}
	}
	if !seenSynAck {
		return errors.New("policy response missed SYNACK")
	}
	return fmt.Errorf("%w", errE2EAnyTLSRejected)
}

func e2eAnyTLSWriteFrame(w io.Writer, cmd byte, sid uint32, payload []byte) error {
	n := len(payload)
	if n > 65535 {
		return errors.New("frame too large")
	}
	h := binary.BigEndian.AppendUint32([]byte{cmd}, sid)
	h = binary.BigEndian.AppendUint16(h, uint16(n))
	if _, err := w.Write(h); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func e2eAnyTLSIsTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func e2eAnyTLSIsReject(err error) bool {
	if err == nil || e2eAnyTLSIsTimeout(err) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "forcibly closed") || strings.Contains(s, "connection reset") || strings.Contains(s, "connection was aborted")
}

func e2eAnyTLSDiagnostics(ctx context.Context, t *testing.T, homeA, homeB string, secrets ...string) string {
	t.Helper()
	var b strings.Builder
	for _, item := range []struct{ label, home string }{{"A", homeA}, {"B", homeB}} {
		statusArgs := []string{"status"}
		if item.label == "B" {
			statusArgs = append(statusArgs, "--full")
		}
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, item.home, statusArgs...)
		if code != 0 {
			fmt.Fprintf(&b, " %s-status=%s", item.label, serverErrorSummary(out, stderr))
		} else {
			assertNoSecrets(t, out, stderr, secrets...)
			var env struct {
				Data struct {
					Servers []struct {
						Name        string `json:"name"`
						Protocol    string `json:"protocol"`
						Listen      string `json:"listen"`
						Connections int    `json:"connections"`
						Dropped     uint64 `json:"dropped_clients"`
					} `json:"servers"`
					Warm     any `json:"warm"`
					Stats    any `json:"stats"`
					Affinity any `json:"affinity"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) == nil {
				fmt.Fprintf(&b, " %s-status=ok servers=%+v warm=%+v stats=%+v affinity=%+v", item.label, env.Data.Servers, env.Data.Warm, env.Data.Stats, env.Data.Affinity)
			}
		}
		code, out, stderr = accountsRun(context.WithoutCancel(ctx), t, item.home, "logs", "--since", "2m", "--tail", "8")
		if code != 0 {
			fmt.Fprintf(&b, " %s-logs=%s", item.label, serverErrorSummary(out, stderr))
			continue
		}
		assertNoSecrets(t, out, stderr, secrets...)
		var env struct {
			Data struct {
				Entries []struct {
					Message string `json:"message"`
				} `json:"entries"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &env) == nil {
			for _, entry := range env.Data.Entries {
				safe := entry.Message
				for _, secret := range secrets {
					if secret != "" {
						safe = strings.ReplaceAll(safe, secret, "[redacted]")
					}
				}
				fmt.Fprintf(&b, " %s-log=%s", item.label, safe)
			}
		}
	}
	return b.String()
}

func e2eAnyTLSWaitStatus(t *testing.T, ctx context.Context, home string, present bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := "status unavailable"
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
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
			if json.Unmarshal([]byte(out), &env) == nil {
				found := false
				for _, s := range env.Data.Servers {
					if s.Name == "edge" {
						found = true
						if present && (s.Protocol != "anytls" || s.Listen == "" || s.Connections < 0 || len(s.Accounts) != 2) {
							t.Fatal("AnyTLS status incomplete")
						}
					}
				}
				assertNoSecrets(t, out, stderr, secrets...)
				if found == present {
					return
				}
				last = "server presence mismatch"
			}
		} else {
			last = serverErrorSummary(out, stderr)
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "AnyTLS status wait expired")
			t.Fatalf("AnyTLS status did not reach expected state (%s)", e2eRedactDiagnostic(last))
		case <-ticker.C:
		}
	}
}
func e2eAnyTLSWaitDropped(t *testing.T, ctx context.Context, home string, minimum uint64, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name    string `json:"name"`
						Dropped uint64 `json:"dropped_clients"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				for _, s := range env.Data.Servers {
					if s.Name == "edge" && s.Dropped >= minimum {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "AnyTLS dropped status wait expired")
			t.Fatal("AnyTLS dropped-client counter did not increment")
		case <-ticker.C:
		}
	}
}
