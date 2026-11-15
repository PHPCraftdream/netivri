//go:build e2e

package main

import (
	"context"
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

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

var errE2ETrustTunnelRejected = errors.New("TrustTunnel authentication rejected")

func TestE2EServerTrustTunnelLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eTrustTunnelState{ctx: ctx}
	setupTrustTunnelServer(t, st)
	readTrustTunnelLinks(t, st)
	subAddr := startTrustTunnelSubscriptions(t, st)
	setupTrustTunnelClient(t, st, subAddr)
	checkTrustTunnelWrongCredentials(t, st)
	rotateTrustTunnelAlice(t, st)
	updateTrustTunnelSubscription(t, st)
	restrictTrustTunnelAlice(t, st)
	clearTrustTunnelRestriction(t, st)
	restartTrustTunnelServer(t, st)
	removeTrustTunnelEdge(t, st)
}

// e2eTrustTunnelState is the shared state of TestE2EServerTrustTunnelLifecycle.
// allSecrets and serverOutputSecrets grow as profiles are rotated, and the
// cleanups read the current angels at teardown.
type e2eTrustTunnelState struct {
	ctx                 context.Context
	pass                string
	hash                string
	backend             string
	aliceUp             *accountsUpstream
	bobUp               *accountsUpstream
	homeA               string
	homeB               string
	port                int
	server              *upProc
	client              *upProc
	links               map[string]string
	aliceProfile        string
	bobProfile          string
	allSecrets          []string
	serverOutputSecrets []string
	serverCleanupSet    *e2eTrustTunnelProcessCleanup
	profiles            *e2eTrustTunnelSubMux
}

// setupTrustTunnelServer writes A's fixture and adds the TrustTunnel edge that
// alice and bob share.
func setupTrustTunnelServer(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	const pass = "trusttunnel-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
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
	st.server = startUp(t, st.homeA, "--port", "0")
	st.port = freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "trusttunnel", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass, hash)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eTrustTunnelWaitStatus(t, st.ctx, st.homeA, true, pass, hash)
	if err := waitServerPort(st.ctx, st.port, true); err != nil {
		t.Fatal("TrustTunnel listener did not open")
	}
}

// getTrustTunnelLink returns the mihomo profile of account and records it.
func getTrustTunnelLink(t *testing.T, st *e2eTrustTunnelState, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", "edge", "--account", account, "--format", "mihomo")
	if code != 0 {
		t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.pass, st.hash)
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("server link response did not contain a profile")
	}
	st.links[account] = env.Data.Link
	return env.Data.Link
}

// readTrustTunnelLinks reads both profiles and registers A's process cleanup
// with their secrets.
func readTrustTunnelLinks(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	st.links = make(map[string]string, 2)
	st.aliceProfile, st.bobProfile = getTrustTunnelLink(t, st, "alice"), getTrustTunnelLink(t, st, "bob")
	st.allSecrets = []string{st.pass, st.hash}
	st.allSecrets = append(st.allSecrets, e2eTrustTunnelCredentials(t, st.links)...)
	st.serverOutputSecrets = append([]string(nil), st.allSecrets...)
	st.serverCleanupSet = &e2eTrustTunnelProcessCleanup{proc: st.server, secrets: &st.serverOutputSecrets}
	t.Cleanup(func() { e2eTrustTunnelProcessCleanupRun(st.serverCleanupSet) })
}

// startTrustTunnelSubscriptions serves both profiles on the loopback and returns
// the listener address.
func startTrustTunnelSubscriptions(t *testing.T, st *e2eTrustTunnelState) string {
	t.Helper()
	st.profiles = &e2eTrustTunnelSubMux{body: map[string]string{"alice": st.aliceProfile + "\n", "bob": st.bobProfile + "\n"}}
	subServer := &http.Server{Handler: st.profiles, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for loopback subscription")
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
	go func() { defer close(subDone); _ = subServer.Serve(subListener) }()
	return subListener.Addr().String()
}

// setupTrustTunnelClient imports both subscriptions into B, adds one account per
// provider and starts B's angel.
func setupTrustTunnelClient(t *testing.T, st *e2eTrustTunnelState, subAddr string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		url := "http://" + subAddr + "/" + account
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", url, "--name", "edge-"+account)
		if code != 0 {
			t.Fatalf("subscription add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.allSecrets...)
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", "edge-"+account)
		if code != 0 {
			t.Fatalf("account add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.allSecrets...)
	}
	st.client = startUp(t, st.homeB, "--port", "0")
	t.Cleanup(func() {
		st.client.Stop()
		assertNoSecrets(t, st.client.Stdout(), st.client.Stderr(), st.allSecrets...)
	})
}

// restartTrustTunnelClient restarts B's angel. B can retain dead-node backoff,
// so the restart makes fresh probes.
func restartTrustTunnelClient(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "down")
	if code != 0 {
		t.Fatalf("angel B down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	st.client.Stop()
	assertNoSecrets(t, st.client.Stdout(), st.client.Stderr(), st.allSecrets...)
	st.client = startUp(t, st.homeB, "--port", "0")
}

// probeTrustTunnel sends one echo through a TrustTunnel profile, optionally with
// a wrong password.
func probeTrustTunnel(t *testing.T, st *e2eTrustTunnelState, raw, payload string, wrong bool) error {
	t.Helper()
	proxy, err := e2eTrustTunnelAdapter(raw)
	if err != nil {
		return errors.New("invalid TrustTunnel profile")
	}
	if wrong {
		proxy["password"] = "known-incorrect-trusttunnel-password"
	}
	parsed, err := adapter.ParseProxy(proxy)
	if err != nil {
		return errors.New("TrustTunnel profile parse failed")
	}
	outbound := parsed.Adapter()
	defer func() { _ = outbound.Close() }()
	host, text, err := net.SplitHostPort(st.backend)
	if err != nil {
		return errors.New("invalid echo address")
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return errors.New("invalid echo port")
	}
	probeCtx, stop := context.WithTimeout(st.ctx, 3*time.Second)
	defer stop()
	started := time.Now()
	// The classified error hides the cause; the log (shown only when the
	// test fails) names the stage, the error type and its redacted text.
	classify := func(stage string, err error) error {
		t.Logf("TrustTunnel probe %q failed at %s after %s: %T: %s", payload, stage, time.Since(started).Round(time.Millisecond), err, e2eRedactDiagnostic(err.Error()))
		return e2eTrustTunnelClassify(err, probeCtx)
	}
	conn, err := outbound.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: e2eUint16(t, n)})
	if err != nil {
		return classify("dial", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return classify("deadline", err)
	}
	if _, err := io.WriteString(conn, payload); err != nil {
		return classify("write", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return classify("read", err)
	}
	if string(got) != payload {
		return errors.New("unexpected echo")
	}
	return nil
}

// driveTrustTunnel sends one echo through B as login and checks that it reached
// that account's recorder only. With retry it keeps trying for 30 s.
func driveTrustTunnel(t *testing.T, st *e2eTrustTunnelState, login string, retry bool) {
	t.Helper()
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	deadline := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if !retry && st.ctx.Err() != nil {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s drive context expired", login)
		}
		if retry && (st.ctx.Err() != nil || !time.Now().Before(deadline)) {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s retry deadline exceeded", login)
		}
		attemptCtx := st.ctx
		cancelAttempt := func() {}
		if retry {
			attemptCtx, cancelAttempt = context.WithDeadline(st.ctx, deadline)
		}
		err := e2eTrustTunnelSocksEcho(attemptCtx, st.client.Status().Data.Listen, login, st.pass, st.backend)
		cancelAttempt()
		if st.ctx.Err() != nil {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s drive context expired", login)
		}
		if retry && !time.Now().Before(deadline) {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s retry deadline exceeded", login)
		}
		if err == nil {
			break
		}
		if !retry {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s client echo failed", login)
		}
		e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
		select {
		case <-st.ctx.Done():
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatalf("%s retry deadline exceeded", login)
		case <-ticker.C:
		}
	}
	if login == "alice" {
		if st.aliceUp.count.Load() <= beforeAlice || st.bobUp.count.Load() != beforeBob {
			e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
			t.Fatal("Alice recorder routing mismatch")
		}
	} else if st.bobUp.count.Load() <= beforeBob || st.aliceUp.count.Load() != beforeAlice {
		e2eTrustTunnelDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("Bob recorder routing mismatch")
	}
}

// checkTrustTunnelWrongCredentials checks that a wrong password is answered with
// an authentication response without reaching a recorder, then drives both accounts.
func checkTrustTunnelWrongCredentials(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := probeTrustTunnel(t, st, st.aliceProfile, "wrong-credentials", true); err == nil || e2eTrustTunnelIsTimeout(err) || !errors.Is(err, errE2ETrustTunnelRejected) {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("wrong TrustTunnel credentials were not rejected with an authentication response")
	}
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() != beforeBob {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("wrong credentials reached an account recorder")
	}
	driveTrustTunnel(t, st, "alice", false)
	driveTrustTunnel(t, st, "bob", false)
}

// rotateTrustTunnelAlice rotates alice's password, checks that the old profile
// is rejected and publishes the new profile to her subscription.
func rotateTrustTunnelAlice(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	oldAlice := st.aliceProfile
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotate failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	if err := probeTrustTunnel(t, st, oldAlice, "old-profile", false); !errors.Is(err, errE2ETrustTunnelRejected) {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("old Alice profile was not rejected after rotation")
	}
	st.aliceProfile = getTrustTunnelLink(t, st, "alice")
	st.allSecrets = append(st.allSecrets, e2eTrustTunnelCredentials(t, map[string]string{"alice": st.aliceProfile})...)
	st.serverOutputSecrets = append([]string(nil), st.allSecrets...)
	st.profiles.set("alice", st.aliceProfile+"\n")
}

// updateTrustTunnelSubscription refreshes alice's subscription on B and drives
// both accounts through it.
func updateTrustTunnelSubscription(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "edge-alice")
	if code != 0 {
		t.Fatalf("subscription update failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveTrustTunnel(t, st, "alice", false)
	driveTrustTunnel(t, st, "bob", false)
}

// restrictTrustTunnelAlice limits alice to 192.0.2.1: her profile is denied on
// the loopback, and bob still routes after B restarts.
func restrictTrustTunnelAlice(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	beforeDropped, present := e2eTrustTunnelDropped(t.Context(), t, st.homeA, st.allSecrets)
	if !present {
		t.Fatal("TrustTunnel status unavailable before account policy")
	}
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.1")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	beforeAlice := st.aliceUp.count.Load()
	if err := probeTrustTunnel(t, st, st.aliceProfile, "restricted-valid-credentials", false); err == nil || e2eTrustTunnelIsTimeout(err) || !errors.Is(err, errE2ETrustTunnelRejected) {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("loopback Alice traffic was not denied by the account policy")
	}
	if st.aliceUp.count.Load() != beforeAlice {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("disallowed Alice traffic reached her recorder")
	}
	e2eTrustTunnelWaitDropped(t, st.ctx, st.homeA, beforeDropped+1, st.allSecrets)
	restartTrustTunnelClient(t, st)
	driveTrustTunnel(t, st, "bob", true)
}

// clearTrustTunnelRestriction restores alice's client policy and drives her
// after B restarts.
func clearTrustTunnelRestriction(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("restore Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	restartTrustTunnelClient(t, st)
	driveTrustTunnel(t, st, "alice", true)
}

// restartTrustTunnelServer restarts A's angel, checks that the listener closes
// and reopens, that both saved profiles still work and that both accounts route.
func restartTrustTunnelServer(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	st.server.Stop()
	assertNoSecrets(t, st.server.Stdout(), st.server.Stderr(), st.allSecrets...)
	st.serverCleanupSet.proc = nil
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatal("TrustTunnel listener did not close at restart")
	}
	st.server = startUp(t, st.homeA, "--port", "0")
	st.serverCleanupSet.proc = st.server
	st.serverCleanupSet.secrets = &st.serverOutputSecrets
	restartTrustTunnelClient(t, st)
	if err := probeTrustTunnel(t, st, st.aliceProfile, "restart-alice-profile", false); err != nil {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("same Alice profile failed after restart")
	}
	if err := probeTrustTunnel(t, st, st.bobProfile, "restart-bob-profile", false); err != nil {
		e2eTrustTunnelDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatal("same Bob profile failed after restart")
	}
	driveTrustTunnel(t, st, "alice", true)
	driveTrustTunnel(t, st, "bob", true)
}

// removeTrustTunnelEdge removes the TrustTunnel edge and checks that its
// listener closes.
func removeTrustTunnelEdge(t *testing.T, st *e2eTrustTunnelState) {
	t.Helper()
	baselineReload := e2eTrustTunnelReloadCount(t, st.ctx, st.homeA, st.allSecrets)
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, baselineReload+1)
	e2eTrustTunnelWaitStatus(t, st.ctx, st.homeA, false, st.allSecrets...)
	if err := waitServerPort(st.ctx, st.port, false); err != nil {
		t.Fatal("removed TrustTunnel listener remains open")
	}
}

type e2eTrustTunnelProcessCleanup struct {
	proc    *upProc
	secrets *[]string
}

func e2eTrustTunnelProcessCleanupRun(c *e2eTrustTunnelProcessCleanup) {
	if c.proc == nil {
		return
	}
	c.proc.Stop()
	assertNoSecrets(c.proc.t, c.proc.Stdout(), c.proc.Stderr(), (*c.secrets)...)
}

func e2eTrustTunnelClassify(err error, ctx context.Context) error {
	if err == nil {
		return nil
	}
	if e2eTrustTunnelIsTimeout(err) || ctx.Err() != nil {
		return errors.New("TrustTunnel operation timed out")
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "unexpected status code: 407") || e2eTrustTunnelNetworkReject(err) {
		return fmt.Errorf("%w", errE2ETrustTunnelRejected)
	}
	return errors.New("TrustTunnel operation failed")
}

func e2eTrustTunnelIsTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

func e2eTrustTunnelNetworkReject(err error) bool {
	if err == nil || e2eTrustTunnelIsTimeout(err) {
		return false
	}
	// The server answers 407 and closes the stream at once; the transport then
	// closes the request body, so a write that comes after it fails on a closed
	// pipe: the same rejection, seen from the write side (the PR 105 e2e run).
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == 10053 || errno == 10054) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, phrase := range []string{"forcibly closed", "connection reset", "connection was aborted", "broken pipe", "closed pipe", "unexpected status code: 407", "eof"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func e2eTrustTunnelCredentials(t *testing.T, profiles map[string]string) []string {
	t.Helper()
	var secrets []string
	for _, raw := range profiles {
		proxy, err := e2eTrustTunnelAdapter(raw)
		if err != nil {
			t.Fatal("decode generated TrustTunnel profile")
		}
		for _, key := range []string{"password"} {
			if value, ok := proxy[key].(string); ok && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	return secrets
}

func e2eTrustTunnelAdapter(raw string) (map[string]any, error) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil || len(doc.Proxies) != 1 {
		return nil, errors.New("invalid profile")
	}
	return doc.Proxies[0], nil
}

type e2eTrustTunnelSubMux struct {
	mu   sync.RWMutex
	body map[string]string
}

func (m *e2eTrustTunnelSubMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, _ = io.WriteString(w, m.body[strings.TrimPrefix(r.URL.Path, "/")])
}

func (m *e2eTrustTunnelSubMux) set(user, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.body[user] = body
}

func e2eTrustTunnelSocksEcho(ctx context.Context, proxy, user, pass, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS method negotiation failed")
	}
	userLen := len(user)
	if userLen > math.MaxUint8 {
		return errors.New("SOCKS credentials too long")
	}
	passLen := len(pass)
	if passLen > math.MaxUint8 {
		return errors.New("SOCKS credentials too long")
	}
	request := append([]byte{1, byte(userLen)}, []byte(user)...)
	request = append(request, byte(passLen))
	request = append(request, []byte(pass)...)
	if _, err = conn.Write(request); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil || authReply[1] != 0 {
		return errors.New("SOCKS authentication failed")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil {
		return errors.New("invalid echo target")
	}
	if port < 1 {
		return errors.New("invalid echo target")
	}
	if port > math.MaxUint16 {
		return errors.New("invalid echo target")
	}
	req := make([]byte, 0, 4+len(ip)+2)
	req = append(req, 5, 1, 0, 1)
	req = append(req, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err = conn.Write(req); err != nil {
		return err
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[1] != 0 {
		return errors.New("SOCKS connect failed")
	}
	payload := []byte("trusttunnel-account-route")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return errors.New("unexpected SOCKS echo")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func e2eTrustTunnelWaitStatus(t *testing.T, ctx context.Context, home string, want bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name     string   `json:"name"`
						Protocol string   `json:"protocol"`
						Listen   string   `json:"listen"`
						Accounts []string `json:"accounts"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) != nil {
				t.Fatal("decode TrustTunnel status")
			}
			present := false
			for _, s := range env.Data.Servers {
				if s.Name == "edge" {
					present = true
					if want && (s.Protocol != "trusttunnel" || s.Listen == "" || len(s.Accounts) != 2) {
						t.Fatal("TrustTunnel server status is incomplete")
					}
				}
			}
			assertNoSecrets(t, out, stderr, secrets...)
			if present == want {
				return
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "TrustTunnel status wait expired")
			t.Fatalf("TrustTunnel status did not become present=%t", want)
		case <-ticker.C:
		}
	}
}

func e2eTrustTunnelReloadCount(t *testing.T, ctx context.Context, home string, secrets []string) uint64 {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var status liveStatus
			if json.Unmarshal([]byte(out), &status) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				return status.Data.Reload.Count
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "TrustTunnel reload status wait expired")
			t.Fatal("angel status unavailable after restart")
		case <-ticker.C:
		}
	}
}

func e2eTrustTunnelDropped(ctx context.Context, t *testing.T, home string, secrets []string) (uint64, bool) {
	t.Helper()
	code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
	if code != 0 {
		return 0, false
	}
	var env struct {
		Data struct {
			Servers []struct {
				Name    string `json:"name"`
				Dropped uint64 `json:"dropped_clients"`
			} `json:"servers"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil {
		t.Fatal("decode TrustTunnel drop status")
	}
	assertNoSecrets(t, out, stderr, secrets...)
	for _, s := range env.Data.Servers {
		if s.Name == "edge" {
			return s.Dropped, true
		}
	}
	return 0, false
}

func e2eTrustTunnelWaitDropped(t *testing.T, ctx context.Context, home string, minimum uint64, secrets []string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if dropped, present := e2eTrustTunnelDropped(context.WithoutCancel(ctx), t, home, secrets); present && dropped >= minimum {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "TrustTunnel dropped status wait expired")
			t.Fatal("TrustTunnel dropped-client counter did not increment")
		case <-ticker.C:
		}
	}
}

func e2eTrustTunnelDiagnostics(ctx context.Context, t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	for _, item := range []struct {
		home string
		args []string
	}{{homeA, []string{"status"}}, {homeA, []string{"logs", "--since", "2m", "--tail", "20"}}, {homeB, []string{"status", "--full"}}} {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, item.home, item.args...)
		if code != 0 {
			t.Logf("diagnostic %v: %s", item.args, e2eTrustTunnelRedact(serverErrorSummary(out, stderr), secrets))
			continue
		}
		var decoded map[string]any
		if json.Unmarshal([]byte(out), &decoded) != nil {
			t.Logf("diagnostic %v: response unavailable", item.args)
			continue
		}
		data, _ := decoded["data"].(map[string]any)
		switch {
		case strings.Contains(item.args[0], "logs"):
			t.Logf("A logs: %s", e2eTrustTunnelRedact(fmt.Sprint(data), secrets))
		case item.home == homeA:
			t.Logf("A servers: %s", e2eTrustTunnelRedact(fmt.Sprint(data["servers"]), secrets))
		default:
			t.Logf("B warm health: %s", e2eTrustTunnelRedact(fmt.Sprint(data["warm"]), secrets))
		}
	}
}

func e2eTrustTunnelRedact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

// A rejected probe fails in whichever step the rejection reaches first: the
// write after the server closed the stream says "closed pipe", the read says
// 407 or EOF. All of them are the rejection; a timeout or an unknown error is not.
func TestE2ETrustTunnelClassifyRejections(t *testing.T) {
	live := context.Background()
	for name, err := range map[string]error{
		"closed pipe":      io.ErrClosedPipe,
		"wrapped pipe":     fmt.Errorf("write: %w", io.ErrClosedPipe),
		"closed pipe text": errors.New("io: read/write on closed pipe"),
		"407":              errors.New("unexpected status code: 407"),
		"eof":              io.EOF,
		"connection reset": syscall.ECONNRESET,
	} {
		if got := e2eTrustTunnelClassify(err, live); !errors.Is(got, errE2ETrustTunnelRejected) {
			t.Errorf("%s: classified as %v, want the rejection", name, got)
		}
	}
	for name, err := range map[string]error{
		"unknown":  errors.New("tls: handshake failure"),
		"deadline": context.DeadlineExceeded,
	} {
		if got := e2eTrustTunnelClassify(err, live); errors.Is(got, errE2ETrustTunnelRejected) {
			t.Errorf("%s: taken for a rejection: %v", name, got)
		}
	}
}
