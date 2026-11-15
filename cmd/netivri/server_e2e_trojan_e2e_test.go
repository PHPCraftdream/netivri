//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestE2ETrojanLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eTrojanState{ctx: ctx}
	setupTrojanServerA(t, st)
	aliceFeed, bobFeed := setupTrojanFeeds(t, st)
	setupTrojanClientB(t, st, aliceFeed, bobFeed)
	checkTrojanInitialRoutes(t, st)
	rotatedLink, rotatedSecret := rotateTrojanAlice(t, st)
	refreshTrojanAliceSubscription(t, st, rotatedLink)
	restrictTrojanAlice(t, st, rotatedLink)
	rotatedLink = clearTrojanRestriction(t, st, rotatedSecret)
	restartTrojanServer(t, st, rotatedLink)
	removeTrojanEdge(t, st)
}

// e2eTrojanState is the shared state of TestE2ETrojanLifecycle. secretCheck grows
// as secrets are created, and the cleanups read the angels at teardown.
type e2eTrojanState struct {
	ctx         context.Context
	pass        string
	hash        string
	backend     string
	aliceUp     *accountsUpstream
	bobUp       *accountsUpstream
	homeA       string
	homeB       string
	port        int
	secretCheck []string
	aliceLink   string
	bobLink     string
	aliceSecret string
	bobSecret   string
	aliceFeed   *e2eTrojanFeed
	initialProc *upProc
	serverA     *upProc
	restartProc *upProc
	clientB     *upProc
}

// setupTrojanServerA writes A's fixture, starts A's angel and adds the Trojan
// edge that alice and bob share.
func setupTrojanServerA(t *testing.T, st *e2eTrojanState) {
	t.Helper()
	const pass = "trojan-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	st.pass = pass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	st.hash = hash
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "alice-route"},
			{Login: "bob", Password: hash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", st.bobUp)})
	st.secretCheck = make([]string, 0, 5)
	st.secretCheck = append(st.secretCheck, pass, hash)
	st.initialProc = startUp(t, st.homeA, "--port", "0")
	st.serverA = st.initialProc
	initialProc := st.initialProc
	t.Cleanup(func() {
		initialProc.Stop()
		assertNoSecrets(t, initialProc.Stdout(), initialProc.Stderr(), st.secretCheck...)
	})
	st.port = freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "trojan", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("Trojan server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass, hash)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eTrojanWaitStatus(t, st.ctx, st.homeA, true)
}

// getTrojanLink returns the Trojan URI of account and checks that it leaks no secret.
func getTrojanLink(t *testing.T, st *e2eTrojanState, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", "edge", "--account", account, "--format", "uri")
	if code != 0 {
		t.Fatalf("Trojan link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.Link == "" {
		t.Fatal("Trojan link response invalid")
	}
	assertNoSecrets(t, out, stderr, st.pass, st.hash)
	return env.Data.Link
}

// setupTrojanFeeds reads both URIs, checks alice's certificate pin and serves
// each URI as a subscription feed. It returns the feed URLs.
func setupTrojanFeeds(t *testing.T, st *e2eTrojanState) (string, string) {
	t.Helper()
	st.aliceLink, st.bobLink = getTrojanLink(t, st, "alice"), getTrojanLink(t, st, "bob")
	aliceURL, err := url.Parse(st.aliceLink)
	if err != nil || aliceURL.Query().Get("pcs") == "" {
		t.Fatal("generated Trojan URI lacks its certificate pin")
	}
	st.aliceSecret = e2eTrojanPassword(t, st.aliceLink)
	st.bobSecret = e2eTrojanPassword(t, st.bobLink)
	st.secretCheck = append(st.secretCheck, st.aliceSecret, st.bobSecret)
	st.aliceFeed = &e2eTrojanFeed{body: st.aliceLink + "\n"}
	bobFeed := &e2eTrojanFeed{body: st.bobLink + "\n"}
	return st.aliceFeed.e2eTrojanStart(t), bobFeed.e2eTrojanStart(t)
}

// setupTrojanClientB imports both feeds into B, adds one account per provider and
// starts B's angel.
func setupTrojanClientB(t *testing.T, st *e2eTrojanState, aliceFeed, bobFeed string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, item := range []struct{ name, feed string }{{"alice-edge", aliceFeed}, {"bob-edge", bobFeed}} {
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", item.feed, "--name", item.name)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.pass, st.hash, st.aliceSecret, st.bobSecret)
	}
	for _, item := range []struct{ account, provider string }{{"alice", "alice-edge"}, {"bob", "bob-edge"}} {
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "account", "add", item.account, "--provider", item.provider)
		if code != 0 {
			t.Fatalf("client account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.pass, st.hash, st.aliceSecret, st.bobSecret)
	}
	st.clientB = startUp(t, st.homeB, "--port", "0")
	clientB := st.clientB
	t.Cleanup(func() {
		clientB.Stop()
		assertNoSecrets(t, clientB.Stdout(), clientB.Stderr(), st.secretCheck...)
	})
}

// driveTrojan sends one echo through B as alice and one as bob, checking that
// each reached its own recorder only.
func driveTrojan(t *testing.T, st *e2eTrojanState) {
	t.Helper()
	a, b := st.aliceUp.count.Load(), st.bobUp.count.Load()
	accountsClient(t, st.clientB.Status().Data.Listen, "alice", st.pass, st.backend)
	if st.aliceUp.count.Load() <= a || st.bobUp.count.Load() != b {
		t.Fatal("Alice did not use exclusively Alice's recorder")
	}
	a, b = st.aliceUp.count.Load(), st.bobUp.count.Load()
	accountsClient(t, st.clientB.Status().Data.Listen, "bob", st.pass, st.backend)
	if st.bobUp.count.Load() <= b || st.aliceUp.count.Load() != a {
		t.Fatal("Bob did not use exclusively Bob's recorder")
	}
}

// checkTrojanInitialRoutes checks both routes, that a wrong secret for alice is
// rejected after TLS without reaching a recorder, and that the pinned probe works.
func checkTrojanInitialRoutes(t *testing.T, st *e2eTrojanState) {
	t.Helper()
	driveTrojan(t, st)
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eTrojanProbe(t, st.ctx, st.aliceLink, st.backend, true, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("wrong secret did not produce a post-TLS Trojan rejection")
	}
	if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() != beforeB {
		t.Fatal("wrong-secret probe reached a recorder")
	}
	if err := e2eTrojanProbe(t, st.ctx, st.aliceLink, st.backend, false, false); err != nil {
		t.Fatal("valid pinned Trojan probe failed")
	}
}

// rotateTrojanAlice rotates alice's secret and checks that the old one is
// rejected after TLS. It returns the rotated link and its secret.
func rotateTrojanAlice(t *testing.T, st *e2eTrojanState) (string, string) {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotation failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeA, 2)
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eTrojanProbe(t, st.ctx, st.aliceLink, st.backend, false, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("rotated old secret did not produce a post-TLS Trojan rejection")
	}
	if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() != beforeB {
		t.Fatal("old-secret probe reached a recorder")
	}
	rotatedLink := getTrojanLink(t, st, "alice")
	rotatedSecret := e2eTrojanPassword(t, rotatedLink)
	st.secretCheck = append(st.secretCheck, rotatedSecret)
	return rotatedLink, rotatedSecret
}

// refreshTrojanAliceSubscription points alice's feed at the rotated link and
// checks that B still routes both accounts.
func refreshTrojanAliceSubscription(t *testing.T, st *e2eTrojanState, rotatedLink string) {
	t.Helper()
	// The subscription keeps its URL and refreshes to the rotated credential.
	st.aliceFeed.e2eTrojanSet(rotatedLink + "\n")
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveTrojan(t, st)
}

// restrictTrojanAlice limits alice to 192.0.2.0/24: alice's rotated link is
// denied and bob's link is still allowed.
func restrictTrojanAlice(t *testing.T, st *e2eTrojanState, rotatedLink string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eTrojanProbe(t, st.ctx, rotatedLink, st.backend, false, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("restricted Alice client was not denied")
	}
	if err := e2eTrojanProbe(t, st.ctx, st.bobLink, st.backend, false, false); err != nil {
		t.Fatal("allowed Bob client failed")
	}
	if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() <= beforeB {
		t.Fatal("client restriction routing/counts incorrect")
	}
}

// clearTrojanRestriction clears alice's client policy, so the restart checks
// only listener persistence, and returns alice's link as read after the clear.
func clearTrojanRestriction(t *testing.T, st *e2eTrojanState, rotatedSecret string) string {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	rotatedLink := getTrojanLink(t, st, "alice")
	if e2eTrojanPassword(t, rotatedLink) != rotatedSecret {
		t.Fatal("rotated Alice secret changed while clearing client policy")
	}
	return rotatedLink
}

// restartTrojanServer restarts A's angel and checks that the saved link still
// works and both accounts route after the restart.
func restartTrojanServer(t *testing.T, st *e2eTrojanState, rotatedLink string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("server home down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	st.serverA.Stop()
	assertNoSecrets(t, st.serverA.Stdout(), st.serverA.Stderr(), st.secretCheck...)
	st.serverA = startUp(t, st.homeA, "--port", "0")
	restartProc := st.serverA
	st.restartProc = restartProc
	t.Cleanup(func() {
		restartProc.Stop()
		assertNoSecrets(t, restartProc.Stdout(), restartProc.Stderr(), st.secretCheck...)
	})
	if got := getTrojanLink(t, st, "alice"); got != rotatedLink {
		t.Fatal("server link/secrets changed across restart")
	}
	if err := e2eTrojanProbe(t, st.ctx, rotatedLink, st.backend, false, false); err != nil {
		t.Fatal("saved rotated link failed after restart")
	}
	driveTrojan(t, st)
}

// removeTrojanEdge removes the Trojan edge, checks that its port closes and
// stops every angel.
func removeTrojanEdge(t *testing.T, st *e2eTrojanState) {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eTrojanWaitStatus(t, st.ctx, st.homeA, false)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatalf("removed Trojan port remains open: %v", err)
	}
	if st.initialProc != nil {
		st.initialProc.Stop()
		assertNoSecrets(t, st.initialProc.Stdout(), st.initialProc.Stderr(), st.secretCheck...)
	}
	if st.restartProc != nil {
		st.restartProc.Stop()
		assertNoSecrets(t, st.restartProc.Stdout(), st.restartProc.Stderr(), st.secretCheck...)
	}
	st.clientB.Stop()
	assertNoSecrets(t, st.clientB.Stdout(), st.clientB.Stderr(), st.secretCheck...)
}

func e2eTrojanPassword(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		t.Fatal("generated Trojan URI has no credential")
	}
	password := u.User.Username()
	if password == "" {
		t.Fatal("generated Trojan URI has empty credential")
	}
	parsed, err := subscription.Parse([]byte(raw))
	if err != nil || len(parsed) != 1 {
		t.Fatal("generated Trojan URI did not import as one proxy")
	}
	importedPassword, _ := parsed[0].Raw["password"].(string)
	importedFingerprint, _ := parsed[0].Raw["fingerprint"].(string)
	insecure, _ := parsed[0].Raw["skip-cert-verify"].(bool)
	if importedPassword != password || importedFingerprint != u.Query().Get("pcs") || insecure {
		t.Fatal("Trojan URI import did not preserve credential and pin")
	}
	return password
}

func e2eTrojanProbe(t *testing.T, parent context.Context, raw, target string, wrong, expectDenied bool) error {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		t.Fatal("malformed Trojan URI")
	}
	serverPort, err := strconv.Atoi(u.Port())
	if err != nil || serverPort < 1 || serverPort > 65535 {
		t.Fatal("malformed Trojan port")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal("malformed probe target")
	}
	dstPort, err := strconv.Atoi(portText)
	if err != nil || dstPort < 1 || dstPort > 65535 {
		t.Fatal("malformed probe target port")
	}
	password := u.User.Username()
	if password == "" {
		t.Fatal("generated Trojan URI has empty credential")
	}
	if wrong {
		password = "known-wrong-trojan-secret" //nolint:gosec // G101: fixed wrong secret for the rejection probe, not a credential
	}
	if u.Query().Get("pcs") == "" {
		t.Fatal("missing Trojan certificate pin")
	}
	client, err := outbound.NewTrojan(outbound.TrojanOption{Name: "e2e-trojan-probe", Server: u.Hostname(), Port: serverPort, Password: password, SNI: u.Query().Get("sni"), Fingerprint: u.Query().Get("pcs")})
	if err != nil {
		t.Fatal("construct Trojan probe")
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: e2eUint16(t, dstPort)})
	if err != nil {
		t.Fatal("Trojan TLS/connection setup failed before authentication")
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal("set Trojan probe deadline")
	}
	if _, err := io.WriteString(conn, "trojan-e2e-probe"); err != nil {
		if expectDenied && e2eTrojanAuthRejected(err) {
			return err
		}
		t.Fatal("Trojan probe request write failed")
	}
	got := make([]byte, len("trojan-e2e-probe"))
	if _, err := io.ReadFull(conn, got); err != nil {
		if expectDenied && e2eTrojanAuthRejected(err) {
			return err
		}
		t.Fatal("Trojan probe echo read failed")
	}
	if string(got) != "trojan-e2e-probe" {
		t.Fatal("Trojan probe returned unexpected echo")
	}
	return nil
}

func e2eTrojanAuthRejected(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// Windows reports a closed peer as WSAECONNRESET/WSAECONNABORTED, which
	// does not match syscall.ECONNRESET there.
	if errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "forcibly closed") || strings.Contains(text, "connection reset") || strings.Contains(text, "connection was aborted")
}

type e2eTrojanFeed struct {
	mu   sync.RWMutex
	body string
}

func (f *e2eTrojanFeed) e2eTrojanSet(body string) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

func (f *e2eTrojanFeed) e2eTrojanStart(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/subscription", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.RLock()
		defer f.mu.RUnlock()
		_, _ = io.WriteString(w, f.body)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listen")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/subscription"
}

func e2eTrojanWaitStatus(t *testing.T, ctx context.Context, home string, want bool) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code != 0 {
			t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
		}
		var env struct {
			Data struct {
				Servers []struct {
					Name     string `json:"name"`
					Protocol string `json:"protocol"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatal("decode status")
		}
		present := false
		for _, server := range env.Data.Servers {
			if server.Name == "edge" {
				present = true
				if server.Protocol != "trojan" {
					t.Fatal("server status protocol mismatch")
				}
			}
		}
		if present == want {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Trojan status wait expired")
			t.Fatal("Trojan server status did not converge")
		case <-ticker.C:
		}
	}
}
