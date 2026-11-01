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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const pass = "trojan-e2e-account-password"
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "alice-route"},
			{Login: "bob", Password: hash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", bobUp)})
	secretCheck := []string{pass, hash}
	serverA := startUp(t, homeA, "--port", "0")
	initialProc := serverA
	t.Cleanup(func() {
		initialProc.Stop()
		assertNoSecrets(t, initialProc.Stdout(), initialProc.Stderr(), secretCheck...)
	})
	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "trojan", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("Trojan server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass, hash)
	waitStatusReload(t, ctx, homeA, 1)
	e2eTrojanWaitStatus(t, ctx, homeA, true)
	getLink := func(account string) string {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, "link", "edge", "--account", account, "--format", "uri")
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
		assertNoSecrets(t, out, stderr, pass, hash)
		return env.Data.Link
	}
	aliceLink, bobLink := getLink("alice"), getLink("bob")
	aliceURL, err := url.Parse(aliceLink)
	if err != nil || aliceURL.Query().Get("pcs") == "" {
		t.Fatal("generated Trojan URI lacks its certificate pin")
	}
	aliceSecret := e2eTrojanPassword(t, aliceLink)
	bobSecret := e2eTrojanPassword(t, bobLink)
	secretCheck = append(secretCheck, aliceSecret, bobSecret)
	aliceFeedState := &e2eTrojanFeed{body: aliceLink + "\n"}
	bobFeedState := &e2eTrojanFeed{body: bobLink + "\n"}
	aliceFeed, bobFeed := aliceFeedState.e2eTrojanStart(t), bobFeedState.e2eTrojanStart(t)
	homeB := t.TempDir()
	for _, item := range []struct{ name, feed string }{{"alice-edge", aliceFeed}, {"bob-edge", bobFeed}} {
		code, out, stderr = accountsRun(t, homeB, "sub", "add", item.feed, "--name", item.name)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, pass, hash, aliceSecret, bobSecret)
	}
	for _, item := range []struct{ account, provider string }{{"alice", "alice-edge"}, {"bob", "bob-edge"}} {
		code, out, stderr = accountsRun(t, homeB, "account", "add", item.account, "--provider", item.provider)
		if code != 0 {
			t.Fatalf("client account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, pass, hash, aliceSecret, bobSecret)
	}
	clientB := startUp(t, homeB, "--port", "0")
	t.Cleanup(func() {
		clientB.Stop()
		assertNoSecrets(t, clientB.Stdout(), clientB.Stderr(), secretCheck...)
	})
	drive := func() {
		t.Helper()
		a, b := aliceUp.count.Load(), bobUp.count.Load()
		accountsClient(t, clientB.Status().Data.Listen, "alice", pass, backend)
		if aliceUp.count.Load() <= a || bobUp.count.Load() != b {
			t.Fatal("Alice did not use exclusively Alice's recorder")
		}
		a, b = aliceUp.count.Load(), bobUp.count.Load()
		accountsClient(t, clientB.Status().Data.Listen, "bob", pass, backend)
		if bobUp.count.Load() <= b || aliceUp.count.Load() != a {
			t.Fatal("Bob did not use exclusively Bob's recorder")
		}
	}
	drive()
	beforeA, beforeB := aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eTrojanProbe(t, ctx, aliceLink, backend, true, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("wrong secret did not produce a post-TLS Trojan rejection")
	}
	if aliceUp.count.Load() != beforeA || bobUp.count.Load() != beforeB {
		t.Fatal("wrong-secret probe reached a recorder")
	}
	if err := e2eTrojanProbe(t, ctx, aliceLink, backend, false, false); err != nil {
		t.Fatal("valid pinned Trojan probe failed")
	}
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotation failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 2)
	beforeA, beforeB = aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eTrojanProbe(t, ctx, aliceLink, backend, false, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("rotated old secret did not produce a post-TLS Trojan rejection")
	}
	if aliceUp.count.Load() != beforeA || bobUp.count.Load() != beforeB {
		t.Fatal("old-secret probe reached a recorder")
	}
	rotatedLink := getLink("alice")
	rotatedSecret := e2eTrojanPassword(t, rotatedLink)
	secretCheck = append(secretCheck, rotatedSecret)
	// The subscription keeps its URL and refreshes to the rotated credential.
	aliceFeedState.e2eTrojanSet(rotatedLink + "\n")
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeB, 1)
	drive()
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 3)
	beforeA, beforeB = aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eTrojanProbe(t, ctx, rotatedLink, backend, false, true); !e2eTrojanAuthRejected(err) {
		t.Fatal("restricted Alice client was not denied")
	}
	if err := e2eTrojanProbe(t, ctx, bobLink, backend, false, false); err != nil {
		t.Fatal("allowed Bob client failed")
	}
	if aliceUp.count.Load() != beforeA || bobUp.count.Load() <= beforeB {
		t.Fatal("client restriction routing/counts incorrect")
	}
	// Clear Alice's client policy so restart checks only listener persistence.
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 4)
	rotatedLink = getLink("alice")
	if e2eTrojanPassword(t, rotatedLink) != rotatedSecret {
		t.Fatal("rotated Alice secret changed while clearing client policy")
	}
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("server home down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	serverA.Stop()
	assertNoSecrets(t, serverA.Stdout(), serverA.Stderr(), secretCheck...)
	serverA = startUp(t, homeA, "--port", "0")
	restartProc := serverA
	t.Cleanup(func() {
		restartProc.Stop()
		assertNoSecrets(t, restartProc.Stdout(), restartProc.Stderr(), secretCheck...)
	})
	if got := getLink("alice"); got != rotatedLink {
		t.Fatal("server link/secrets changed across restart")
	}
	if err := e2eTrojanProbe(t, ctx, rotatedLink, backend, false, false); err != nil {
		t.Fatal("saved rotated link failed after restart")
	}
	drive()
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eTrojanWaitStatus(t, ctx, homeA, false)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("removed Trojan port remains open: %v", err)
	}
	if initialProc != nil {
		initialProc.Stop()
		assertNoSecrets(t, initialProc.Stdout(), initialProc.Stderr(), secretCheck...)
	}
	if restartProc != nil {
		restartProc.Stop()
		assertNoSecrets(t, restartProc.Stdout(), restartProc.Stderr(), secretCheck...)
	}
	clientB.Stop()
	assertNoSecrets(t, clientB.Stdout(), clientB.Stderr(), secretCheck...)
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
		password = "known-wrong-trojan-secret"
	}
	if u.Query().Get("pcs") == "" {
		t.Fatal("missing Trojan certificate pin")
	}
	client, err := outbound.NewTrojan(outbound.TrojanOption{Name: "e2e-trojan-probe", Server: u.Hostname(), Port: serverPort, Password: password, SNI: u.Query().Get("sni"), Fingerprint: u.Query().Get("pcs")})
	if err != nil {
		t.Fatal("construct Trojan probe")
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(dstPort)})
	if err != nil {
		t.Fatal("Trojan TLS/connection setup failed before authentication")
	}
	defer conn.Close()
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
		code, out, stderr := accountsRun(t, home, "status")
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
			t.Fatal("Trojan server status did not converge")
		case <-ticker.C:
		}
	}
}
