//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
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

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestE2EShadowQUICLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eShadowQUICState{ctx: ctx}
	setupShadowQUICServerA(t, st)
	readShadowQUICLinks(t, st)
	setupShadowQUICClientB(t, st)
	checkShadowQUICInitialRoutes(t, st)
	checkShadowQUICWrongSecret(t, st)
	rotateShadowQUICAlice(t, st)
	updateShadowQUICSubscription(t, st)
	restrictShadowQUICAlice(t, st)
	checkShadowQUICRestrictedBob(t, st)
	clearShadowQUICRestriction(t, st)
	restartShadowQUICServer(t, st)
	checkShadowQUICAfterRestart(t, st)
	removeShadowQUICEdge(t, st)
}

// e2eShadowQUICState is the shared state of TestE2EShadowQUICLifecycle.
// secretCheck grows as links and secrets are created, and the cleanups read
// the current serverProc and clientProc at teardown.
type e2eShadowQUICState struct {
	ctx         context.Context
	accountPass string
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
	feedAlice   *e2eShadowQUICFeed
	feedBob     *e2eShadowQUICFeed
	serverA     *upProc
	serverProc  *upProc
	clientB     *upProc
	clientProc  *upProc
}

// setupShadowQUICServerA writes A's fixture and adds the ShadowQUIC edge that
// alice and bob share.
func setupShadowQUICServerA(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	const accountPass = "shadowquic-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	st.accountPass = accountPass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(accountPass)
	if err != nil {
		t.Fatal("hash test account password")
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
	st.serverA = startUp(t, st.homeA, "--port", "0")
	st.secretCheck = []string{accountPass, hash}
	st.serverProc = st.serverA
	t.Cleanup(func() {
		if st.serverProc != nil {
			st.serverProc.Stop()
			assertNoSecrets(t, st.serverProc.Stdout(), st.serverProc.Stderr(), st.secretCheck...)
		}
	})
	st.port = servertest.FreePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "shadowquic", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=localhost")
	if code != 0 {
		t.Fatalf("ShadowQUIC server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eShadowQUICWaitStatus(t, st.ctx, st.homeA, true, st.secretCheck...)
}

// getShadowQUICLink returns the mihomo ShadowQUIC link of account.
func getShadowQUICLink(t *testing.T, st *e2eShadowQUICState, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", "edge", "--account", account, "--host", "::ffff:127.0.0.1", "--format", "mihomo")
	if code != 0 {
		t.Fatalf("ShadowQUIC link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("ShadowQUIC link response invalid")
	}
	assertNoSecrets(t, out, stderr, st.accountPass, st.hash)
	return env.Data.Link
}

// readShadowQUICLinks reads the links of alice and bob and adds their secrets
// to the redaction list.
func readShadowQUICLinks(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	st.aliceLink, st.bobLink = getShadowQUICLink(t, st, "alice"), getShadowQUICLink(t, st, "bob")
	st.secretCheck = append(st.secretCheck, e2eShadowQUICPassword(t, st.aliceLink), e2eShadowQUICPassword(t, st.bobLink))
}

// setupShadowQUICClientB serves each link as a subscription feed, imports both
// into B and starts B's angel.
func setupShadowQUICClientB(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	st.feedAlice = &e2eShadowQUICFeed{body: st.aliceLink}
	feedURLAlice := st.feedAlice.start(t, "/alice")
	st.feedBob = &e2eShadowQUICFeed{body: st.bobLink}
	feedURLBob := st.feedBob.start(t, "/bob")
	st.homeB = t.TempDir()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", feedURLAlice, "--name", "edge")
	if code != 0 {
		t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", "alice", "--provider", "edge")
	if code != 0 {
		t.Fatalf("Alice account add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	code, out, stderr = accountsRun(t.Context(), t, st.homeB, "sub", "add", feedURLBob, "--name", "bob-edge")
	if code != 0 {
		t.Fatalf("Bob subscription add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", "bob", "--provider", "bob-edge")
	if code != 0 {
		t.Fatalf("Bob account add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	st.clientB = startUp(t, st.homeB, "--port", "0")
	st.clientProc = st.clientB
	t.Cleanup(func() {
		if st.clientProc != nil {
			st.clientProc.Stop()
			assertNoSecrets(t, st.clientProc.Stdout(), st.clientProc.Stderr(), st.secretCheck...)
		}
	})
}

// restartShadowQUICClient restarts B's angel and records it for cleanup.
func restartShadowQUICClient(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "down")
	if code != 0 {
		t.Fatalf("client B down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	st.clientB.Stop()
	assertNoSecrets(t, st.clientB.Stdout(), st.clientB.Stderr(), st.secretCheck...)
	st.clientB = startUp(t, st.homeB, "--port", "0")
	st.clientProc = st.clientB
}

// driveShadowQUIC sends one echo through B as account and checks that only that
// account's upstream recorded it. With retry it keeps trying until the deadline
// or the test context ends.
func driveShadowQUIC(st *e2eShadowQUICState, account string, retry bool) error {
	deadline := time.Now().Add(30 * time.Second)
	if parentDeadline, ok := st.ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	for {
		attemptCtx, cancelAttempt := context.WithDeadline(st.ctx, deadline)
		err := e2eShadowQUICEcho(attemptCtx, st.clientB.Status().Data.Listen, account, st.accountPass, st.backend)
		cancelAttempt()
		aliceDelta := st.aliceUp.count.Load() - beforeAlice
		bobDelta := st.bobUp.count.Load() - beforeBob
		if aliceDelta < 0 || bobDelta < 0 {
			return errors.New("recorder count decreased during routed echo")
		}
		if err == nil && ((account == "alice" && aliceDelta > 0 && bobDelta == 0) || (account == "bob" && bobDelta > 0 && aliceDelta == 0)) {
			return nil
		}
		if err == nil {
			err = errors.New("account recorder routing mismatch")
		}
		if !retry || time.Now().After(deadline) || st.ctx.Err() != nil {
			return err
		}
		select {
		case <-st.ctx.Done():
			return err
		case <-tick.C:
		}
	}
}

// checkShadowQUICInitialRoutes drives alice and bob through B.
func checkShadowQUICInitialRoutes(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	if err := driveShadowQUIC(st, "alice", false); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
	if err := driveShadowQUIC(st, "bob", false); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// checkShadowQUICWrongSecret checks that a wrong secret for alice is refused or
// times out, and that neither outcome reaches a recorder.
func checkShadowQUICWrongSecret(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	wrongPassword := e2eShadowQUICWrongPassword(t)
	st.secretCheck = append(st.secretCheck, wrongPassword)
	wrongErr := e2eShadowQUICProbe(st.ctx, st.port, "alice", wrongPassword, st.backend)
	if wrongErr == nil {
		t.Fatal("wrong ShadowQUIC secret was accepted")
	}
	if e2eShadowQUICTimedOut(wrongErr) {
		if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() != beforeB {
			t.Fatal("wrong-secret probe reached a recorder")
		}
		t.Log("JLS silently drops invalid authentication; client deadline is inconclusive, not a rejection classification")
	} else if !e2eShadowQUICRejected(wrongErr) {
		t.Fatalf("wrong-secret probe returned an unexpected error class: %T", wrongErr)
	}
	if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() != beforeB {
		t.Fatal("wrong-secret probe reached a recorder")
	}
}

// rotateShadowQUICAlice checks that alice's current secret works, rotates it,
// checks that the old secret is refused and publishes the new link to the feed.
func rotateShadowQUICAlice(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	oldAlice := st.aliceLink
	if err := e2eShadowQUICProbe(st.ctx, st.port, "alice", e2eShadowQUICPassword(t, oldAlice), st.backend); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotation failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	oldAliceCount, oldBobCount := st.aliceUp.count.Load(), st.bobUp.count.Load()
	switch err := e2eShadowQUICProbe(st.ctx, st.port, "alice", e2eShadowQUICPassword(t, oldAlice), st.backend); {
	case err == nil:
		t.Fatal("old Alice secret was accepted after rotation")
	case e2eShadowQUICRejected(err):
		if st.aliceUp.count.Load() != oldAliceCount || st.bobUp.count.Load() != oldBobCount {
			t.Fatal("old-secret probe reached a recorder")
		}
	case e2eShadowQUICTimedOut(err):
		if st.aliceUp.count.Load() != oldAliceCount || st.bobUp.count.Load() != oldBobCount {
			t.Fatal("old-secret probe reached a recorder")
		}
		t.Log("JLS silently drops invalid authentication; client deadline is inconclusive, not a rejection classification")
	default:
		t.Fatalf("old-secret probe returned an unexpected error class: %T", err)
	}
	st.aliceLink = getShadowQUICLink(t, st, "alice")
	st.secretCheck = append(st.secretCheck, e2eShadowQUICPassword(t, st.aliceLink))
	st.feedAlice.set(st.aliceLink)
}

// updateShadowQUICSubscription refreshes B's subscription after the rotation
// and drives both accounts through the restarted B.
func updateShadowQUICSubscription(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "edge")
	if code != 0 {
		t.Fatalf("subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	restartShadowQUICClient(t, st)
	// The first QUIC connection after a restart can be reset on a loaded
	// runner (windows e2e), like the later restarts below: retry it.
	if err := driveShadowQUIC(st, "alice", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
	if err := driveShadowQUIC(st, "bob", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// restrictShadowQUICAlice limits alice to 192.0.2.0/24: alice's probe from
// outside the range is refused and bob's probe still succeeds.
func restrictShadowQUICAlice(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("set Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	if err := e2eShadowQUICProbe(st.ctx, st.port, "alice", e2eShadowQUICPassword(t, st.aliceLink), st.backend); err == nil {
		t.Fatal("disallowed Alice client probe was accepted")
	} else if !e2eShadowQUICRejected(err) {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("disallowed Alice client probe timed out instead of being rejected")
		}
		t.Fatalf("disallowed Alice probe returned an unexpected error class: %T", err)
	}
	if err := e2eShadowQUICProbe(st.ctx, st.port, "bob", e2eShadowQUICPassword(t, st.bobLink), st.backend); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// checkShadowQUICRestrictedBob restarts B while alice is restricted and checks
// that bob still routes.
func checkShadowQUICRestrictedBob(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	restartShadowQUICClient(t, st)
	if err := driveShadowQUIC(st, "bob", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// clearShadowQUICRestriction clears alice's client filter and checks that both
// accounts route again after B restarts.
func clearShadowQUICRestriction(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	restartShadowQUICClient(t, st)
	if err := driveShadowQUIC(st, "alice", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
	if err := driveShadowQUIC(st, "bob", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// restartShadowQUICServer restarts A's angel and checks that alice's current
// secret works again afterwards.
func restartShadowQUICServer(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("server down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	st.serverA.Stop()
	assertNoSecrets(t, st.serverA.Stdout(), st.serverA.Stderr(), st.secretCheck...)
	st.serverProc = nil
	st.serverA = startUp(t, st.homeA, "--port", "0")
	st.serverProc = st.serverA
	if err := e2eShadowQUICProbe(st.ctx, st.port, "alice", e2eShadowQUICPassword(t, st.aliceLink), st.backend); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// checkShadowQUICAfterRestart restarts B and drives both accounts.
func checkShadowQUICAfterRestart(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	restartShadowQUICClient(t, st)
	if err := driveShadowQUIC(st, "alice", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
	if err := driveShadowQUIC(st, "bob", true); err != nil {
		e2eShadowQUICFailure(t, st.homeA, st.homeB, err, st.secretCheck...)
	}
}

// removeShadowQUICEdge removes the ShadowQUIC edge and checks that its UDP port
// is released.
func removeShadowQUICEdge(t *testing.T, st *e2eShadowQUICState) {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eShadowQUICWaitStatus(t, st.ctx, st.homeA, false, st.secretCheck...)
	if err := e2eShadowQUICWaitPort(st.ctx, st.port); err != nil {
		t.Fatalf("removed ShadowQUIC UDP port remains bound: %v", err)
	}
}

type e2eShadowQUICFeed struct {
	mu   sync.RWMutex
	body string
}

func (f *e2eShadowQUICFeed) set(body string) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

func (f *e2eShadowQUICFeed) start(t *testing.T, path string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
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
	return "http://" + ln.Addr().String() + path
}
func e2eShadowQUICPassword(t *testing.T, body string) string {
	t.Helper()
	result, err := subscription.Parse([]byte(body))
	if err != nil || len(result) != 1 {
		t.Fatal("invalid generated ShadowQUIC snippet")
	}
	password, _ := result[0].Raw["password"].(string)
	if password == "" {
		t.Fatal("ShadowQUIC snippet has no password")
	}
	return password
}
func e2eShadowQUICEcho(parent context.Context, proxy, account, password, target string) error {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	return e2eShadowQUICProxyEcho(ctx, proxy, account, password, target)
}

func e2eShadowQUICProxyEcho(ctx context.Context, proxy, account, password, target string) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	c, err := dialer.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := c.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	r := make([]byte, 2)
	if _, err = io.ReadFull(c, r); err != nil {
		return err
	}
	if r[1] != 2 {
		return errors.New("proxy did not request authentication")
	}
	accountLen := len(account)
	if accountLen > 255 {
		return errors.New("account name too long")
	}
	passLen := len(password)
	if passLen > 255 {
		return errors.New("password too long")
	}
	authRequest := make([]byte, 0, 3+len(account)+len(password))
	authRequest = append(authRequest, 1, byte(accountLen))
	authRequest = append(authRequest, account...)
	authRequest = append(authRequest, byte(passLen))
	authRequest = append(authRequest, password...)
	if _, err = c.Write(authRequest); err != nil {
		return err
	}
	if _, err = io.ReadFull(c, r); err != nil {
		return err
	}
	if r[1] != 0 {
		return errors.New("proxy authentication failed")
	}
	host, portText, e := net.SplitHostPort(target)
	if e != nil {
		return e
	}
	port, _ := strconv.Atoi(portText)
	if port < 0 {
		return errors.New("echo port invalid")
	}
	if port > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return errors.New("backend is not IPv4")
	}
	req := []byte{5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3]}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err = c.Write(req); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err = io.ReadFull(c, head); err != nil {
		return err
	}
	if head[1] != 0 {
		return errors.New("proxy CONNECT rejected")
	}
	rest := 6
	switch head[3] {
	case 3:
		b := make([]byte, 1)
		if _, err = io.ReadFull(c, b); err != nil {
			return err
		}
		rest = int(b[0]) + 2
	case 4:
		rest = 18
	}
	if _, err = io.CopyN(io.Discard, c, int64(rest)); err != nil {
		return err
	}
	payload := []byte("shadowquic-lifecycle-echo")
	if _, err = c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return errors.New("unexpected echo")
	}
	return nil
}

func e2eShadowQUICProbe(parent context.Context, port int, user, password, target string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	host, ptext, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	p, _ := strconv.Atoi(ptext)
	if p < 0 {
		return errors.New("echo port invalid")
	}
	if p > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	client, err := outbound.NewShadowQuic(outbound.ShadowQuicOption{Name: "shadowquic-e2e-probe", Server: "127.0.0.1", Port: port, Username: user, Password: password, SNI: "localhost"})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	payload := []byte("shadowquic-probe")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return errors.New("unexpected probe echo")
	}
	return nil
}

// e2eShadowQUICTimedOut reports a deadline or a network timeout anywhere in a
// (possibly joined) error: the silent drop of JLS shows up as one.
func e2eShadowQUICTimedOut(err error) bool {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "timeout") || strings.Contains(text, "no recent network activity") || strings.Contains(text, "deadline exceeded")
}

func e2eShadowQUICRejected(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "forcibly closed") || strings.Contains(text, "connection reset") || strings.Contains(text, "connection was aborted")
}

func e2eShadowQUICWrongPassword(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal("generate wrong ShadowQUIC credential")
	}
	return base64.RawURLEncoding.EncodeToString(secret)
}

func e2eShadowQUICWaitStatus(t *testing.T, ctx context.Context, home string, wantPresent bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
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
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatal("decode ShadowQUIC status")
			}
			present := false
			for _, server := range env.Data.Servers {
				if server.Name != "edge" {
					continue
				}
				present = true
				if server.Protocol != "shadowquic" || server.Listen == "" || len(server.Accounts) != 2 || server.Connections < 0 {
					t.Fatal("ShadowQUIC status is incomplete")
				}
			}
			assertNoSecrets(t, out, stderr, secrets...)
			if present == wantPresent {
				return
			}
		} else if !strings.Contains(stderr, "ANGEL_NOT_RUNNING") {
			t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "ShadowQUIC status wait expired")
			t.Fatalf("ShadowQUIC status did not become present=%t", wantPresent)
		case <-ticker.C:
		}
	}
}

func e2eShadowQUICFailure(t *testing.T, homeA, homeB string, err error, secrets ...string) {
	t.Helper()
	// The checks below stop the test on a possible secret in the output (a short
	// secret can match by chance), so name the real failure first.
	t.Logf("ShadowQUIC echo failed (primary error, redacted): %s", e2eRedactDiagnostic(err.Error()))
	codeA, statusA, stderrA := accountsRun(t.Context(), t, homeA, "status", "--full")
	codeB, statusB, stderrB := accountsRun(t.Context(), t, homeB, "status", "--full")
	assertNoSecrets(t, statusA, stderrA, append(e2eShadowQUICAccountSecrets(t, homeA), secrets...)...)
	assertNoSecrets(t, statusB, stderrB, append(e2eShadowQUICAccountSecrets(t, homeB), secrets...)...)
	if codeA != 0 {
		statusA = "status unavailable"
	} else {
		statusA = e2eShadowQUICSafeStatus(statusA)
	}
	if codeB != 0 {
		statusB = "status unavailable"
	} else {
		statusB = e2eShadowQUICSafeStatus(statusB)
	}
	logsA := serverE2ELogTail(t.Context(), t, homeA)
	assertNoSecrets(t, logsA, "", secrets...)
	statusSecrets := append(e2eShadowQUICAccountSecrets(t, homeA), secrets...)
	assertNoSecrets(t, statusA, "", statusSecrets...)
	statusSecrets = append(e2eShadowQUICAccountSecrets(t, homeB), secrets...)
	assertNoSecrets(t, statusB, "", statusSecrets...)
	t.Logf("ShadowQUIC diagnostics (redacted): A status=%s; A logs=%s; B status and node health=%s", statusA, logsA, statusB)
	t.Fatalf("ShadowQUIC echo failed: %v", err)
}

type e2eShadowQUICStatusEnvelope struct {
	Data struct {
		Angel   string          `json:"angel"`
		Reload  json.RawMessage `json:"reload"`
		Servers json.RawMessage `json:"servers"`
		Warm    json.RawMessage `json:"warm"`
	} `json:"data"`
}

func e2eShadowQUICSafeStatus(raw string) string {
	var env e2eShadowQUICStatusEnvelope
	if json.Unmarshal([]byte(raw), &env) != nil {
		return "status decode failed"
	}
	return strings.Join([]string{env.Data.Angel, string(env.Data.Reload), string(env.Data.Servers), string(env.Data.Warm)}, " ")
}

func e2eShadowQUICAccountSecrets(t *testing.T, home string) []string {
	t.Helper()
	cfg := loadConfigFile(t, home)
	secrets := make([]string, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		secrets = append(secrets, account.Password)
	}
	return secrets
}

func e2eShadowQUICWaitPort(ctx context.Context, port int) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	for {
		pc, err := (&net.ListenConfig{}).ListenPacket(context.WithoutCancel(ctx), "udp", addr)
		if err == nil {
			if closeErr := pc.Close(); closeErr != nil {
				return closeErr
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
