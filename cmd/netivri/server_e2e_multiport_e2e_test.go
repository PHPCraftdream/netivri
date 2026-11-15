//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestE2EMultiportTrojanAnyTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eMultiportState{ctx: ctx}
	setupMultiportServerA(t, st)
	defer st.serverA.Stop()
	addMultiportEdges(t, st)
	subAddr := startMultiportSubscriptions(t, st.links)
	setupMultiportClientB(t, st, subAddr)
	defer func() { st.clientB.Stop() }()
	driveMultiportRoute(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
	driveMultiportRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
	checkMultiportAmbiguousAdd(t, st)
	checkMultiportRemoval(t, st)
}

// e2eMultiportState is the two-angel fixture of TestE2EMultiportTrojanAnyTLS.
type e2eMultiportState struct {
	ctx       context.Context
	alicePass string
	bobPass   string
	backend   string
	secrets   []string
	homeA     string
	homeB     string
	serverA   *upProc
	clientB   *upProc
	aliceUp   *accountsUpstream
	bobUp     *accountsUpstream
	port      int
	links     map[string]string
}

// setupMultiportServerA writes A's fixture, with each account on its own
// upstream, and starts A's angel.
func setupMultiportServerA(t *testing.T, st *e2eMultiportState) {
	t.Helper()
	const alicePass, bobPass = "multiport-e2e-alice-password", "multiport-e2e-bob-password" //nolint:gosec // G101: fixture passwords of test accounts, not credentials
	st.alicePass, st.bobPass = alicePass, bobPass
	st.secrets = make([]string, 0, 6)
	st.secrets = append(st.secrets, alicePass, bobPass)
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob password")
	}
	st.secrets = append(st.secrets, aliceHash, bobHash)
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "alice-route"}, {Login: "bob", Password: bobHash, Provider: "bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "alice-route", URL: "http://alice.invalid/sub"}, {Name: "bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", st.bobUp)})
	st.serverA = startUp(t, st.homeA, "--port", "0")
}

// addMultiportEdges adds a Trojan and an AnyTLS edge on one port and reads
// both links.
func addMultiportEdges(t *testing.T, st *e2eMultiportState) {
	t.Helper()
	st.port = freePort(t)
	for i, args := range [][]string{
		{"--protocol", "trojan", "--name", "trojan-edge", "--account", "alice", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=trojan.local"},
		{"--protocol", "anytls", "--name", "anytls-edge", "--account", "bob", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=anytls.local"},
	} {
		cmd := append([]string{"add"}, args...)
		code, out, stderr := serverRun(t, st.homeA, cmd...)
		if code != 0 {
			t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
		}
		waitStatusReload(t, st.ctx, st.homeA, uint64(i+1))
	}
	st.links = map[string]string{"alice": e2eMultiportLink(t, st.homeA, "trojan-edge", "alice"), "bob": e2eMultiportLink(t, st.homeA, "anytls-edge", "bob")}
	st.secrets = append(st.secrets, st.links["alice"], st.links["bob"])
}

// startMultiportSubscriptions serves each account's link on the loopback and
// returns the listener address.
func startMultiportSubscriptions(t *testing.T, links map[string]string) string {
	t.Helper()
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, links[account]+"\n") })
	}
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for local subscriptions")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(done); _ = subServer.Serve(ln) }()
	return ln.Addr().String()
}

// setupMultiportClientB adds both subscriptions and one account per provider
// to B, then starts B's angel.
func setupMultiportClientB(t *testing.T, st *e2eMultiportState, subAddr string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-edge"
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", "http://"+subAddr+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
	}
	st.clientB = startUp(t, st.homeB, "--port", "0")
}

// driveMultiportRoute retries one echo through B until it reaches own and not
// other, or fails after 30 s.
func driveMultiportRoute(t *testing.T, st *e2eMultiportState, login, pass string, own, other *accountsUpstream) {
	t.Helper()
	beforeOwn, beforeOther := own.count.Load(), other.count.Load()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		attemptCtx, stop := context.WithDeadline(st.ctx, deadline)
		err := e2eAnyTLSClientEcho(attemptCtx, st.clientB.Status().Data.Listen, login, pass, st.backend)
		stop()
		if err == nil && own.count.Load() > beforeOwn && other.count.Load() == beforeOther {
			return
		}
		if err != nil {
			last = err
		} else {
			last = errors.New("account routing mismatch")
		}
		if !time.Now().Before(deadline) || st.ctx.Err() != nil {
			break
		}
		select {
		case <-st.ctx.Done():
		case <-time.After(150 * time.Millisecond):
		}
		if st.ctx.Err() != nil {
			break
		}
	}
	t.Logf("proxy echo failed: %v; %s", last, e2eAnyTLSDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.secrets...))
	t.Fatal("proxy echo or account routing failed")
}

// checkMultiportAmbiguousAdd checks that a second edge on the same port is refused.
func checkMultiportAmbiguousAdd(t *testing.T, st *e2eMultiportState) {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "trojan", "--name", "ambiguous", "--account", "alice", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--param", "sni=trojan.local")
	assertNoSecrets(t, out, stderr, st.secrets...)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(stderr), &envelope)
	if code != 6 || envelope.Error.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("ambiguous add returned unexpected result: %s", serverErrorSummary(out, stderr))
	}
}

// checkMultiportRemoval removes the Trojan edge, restarts B, and checks that
// the surviving AnyTLS route still reaches only bob's upstream.
func checkMultiportRemoval(t *testing.T, st *e2eMultiportState) {
	t.Helper()
	if code, out, stderr := serverRun(t, st.homeA, "remove", "trojan-edge"); code != 0 {
		t.Fatalf("remove Trojan: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeA, 3)
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if !st.serverA.Status().OK {
		t.Fatal("A did not remain running after server removal")
	}
	// B may have cached the removed endpoint; restart it before checking the surviving route.
	if code, out, stderr := accountsRun(t.Context(), t, st.homeB, "down"); code != 0 {
		t.Fatalf("B down: %s", serverErrorSummary(out, stderr))
	}
	st.clientB.Stop()
	st.clientB = startUp(t, st.homeB, "--port", "0")
	driveMultiportRoute(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() <= beforeBob {
		t.Fatal("remaining Bob route reached the wrong account")
	}
}

func e2eMultiportLink(t *testing.T, home, name, login string) string {
	t.Helper()
	code, out, stderr := serverRun(t, home, "link", name, "--account", login, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || strings.TrimSpace(env.Data.Link) == "" {
		t.Fatal("invalid server link response")
	}
	return env.Data.Link
}
