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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	tuicv5 "github.com/metacubex/mihomo/transport/tuic/v5"
	"github.com/metacubex/quic-go"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

var errE2ETUICAuthenticationFailure = errors.New("TUIC authentication rejected")

func TestE2EServerTUICLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eTUICState{ctx: ctx}
	setupTUICServerA(t, st)
	subAddr := startTUICSubscriptions(t, st)
	setupTUICClientB(t, st, subAddr)
	checkTUICInitialRoutes(t, st)
	newAlice := rotateTUICAlice(t, st)
	refreshTUICAliceSubscription(t, st)
	restrictTUICAlice(t, st, newAlice)
	clearTUICRestriction(t, st)
	restartTUICServer(t, st)
	removeTUICServices(t, st)
}

// e2eTUICState is the shared state of TestE2EServerTUICLifecycle. secrets grows
// as links are created; cleanupSecrets and serverAngel are read by the cleanups.
type e2eTUICState struct {
	ctx            context.Context
	alicePass      string
	bobPass        string
	aliceHash      string
	bobHash        string
	backend        string
	aliceUp        *accountsUpstream
	bobUp          *accountsUpstream
	homeA          string
	homeB          string
	alicePort      int
	bobPort        int
	reload         uint64
	secrets        []string
	cleanupSecrets []string
	links          map[string]string
	current        map[string]string
	mu             sync.RWMutex
	serverAngel    *upProc
	clientAngel    *upProc
}

// setupTUICServerA writes A's fixture and adds one TUIC edge per account, each on
// its own UDP port.
func setupTUICServerA(t *testing.T, st *e2eTUICState) {
	t.Helper()
	const alicePass = "tuic-e2e-alice-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	const bobPass = "tuic-e2e-bob-account-password"     //nolint:gosec // G101: fixture password of a test account, not a credential
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
	writeConfig(t, st.homeA, func(c *config.Config) {
		c.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "alice-route"}, {Login: "bob", Password: bobHash, Provider: "bob-route"}}
		c.Subscriptions = []config.Subscription{{Name: "alice-route", URL: accountsSubServer(t, e2eTUICSubscriptionBody("alice-route-node", st.aliceUp))}, {Name: "bob-route", URL: accountsSubServer(t, e2eTUICSubscriptionBody("bob-route-node", st.bobUp))}}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", st.bobUp)})
	st.secrets = make([]string, 0, 4)
	st.secrets = append(st.secrets, alicePass, bobPass, aliceHash, bobHash)
	st.cleanupSecrets = append([]string(nil), st.secrets...)
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	t.Cleanup(func() {
		st.serverAngel.Stop()
		assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.cleanupSecrets...)
	})
	st.alicePort = e2eTUICFreeUDPPort(t, 0)
	st.links = map[string]string{}
	for _, account := range []string{"alice", "bob"} {
		port := st.alicePort
		if account == "bob" {
			st.bobPort = e2eTUICFreeUDPPort(t, st.alicePort)
			port = st.bobPort
		}
		name := "tuic-" + account
		code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "tuic", "--name", name, "--account", account, "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
		if code != 0 {
			t.Fatalf("TUIC add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		st.reload++
		waitStatusReload(t, st.ctx, st.homeA, st.reload)
		st.links[account] = e2eTUICMihomoLink(t, st.homeA, name, account, st.secrets)
		st.secrets = append(st.secrets, e2eTUICLinkSecrets(t, st.homeA, name, account)...)
		st.cleanupSecrets = append([]string(nil), st.secrets...)
	}
	if err := e2eTUICWaitStatus(t, st.ctx, st.homeA, true); err != nil {
		t.Fatalf("TUIC status did not report both servers: %v", err)
	}
}

// startTUICSubscriptions serves each account's current link on the loopback and
// returns the listener address.
func startTUICSubscriptions(t *testing.T, st *e2eTUICState) string {
	t.Helper()
	st.current = map[string]string{"alice": st.links["alice"], "bob": st.links["bob"]}
	mux := http.NewServeMux()
	mux.HandleFunc("/alice", func(w http.ResponseWriter, _ *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		_, _ = io.WriteString(w, st.current["alice"])
	})
	mux.HandleFunc("/bob", func(w http.ResponseWriter, _ *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		_, _ = io.WriteString(w, st.current["bob"])
	})
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen local subscription")
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

// setupTUICClientB imports both subscriptions into B, claims each account with its
// own password and starts B's angel.
func setupTUICClientB(t *testing.T, st *e2eTUICState, subAddr string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", "http://"+subAddr+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("local subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
	}
	clientConfig := loadConfigFile(t, st.homeB)
	for i := range clientConfig.Accounts {
		switch clientConfig.Accounts[i].Login {
		case "alice":
			clientConfig.Accounts[i].Password = st.aliceHash
		case "bob":
			clientConfig.Accounts[i].Password = st.bobHash
		}
	}
	if err := config.Save(config.ConfigPath(st.homeB), clientConfig); err != nil {
		t.Fatal("save client account credentials")
	}
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	t.Cleanup(func() {
		st.clientAngel.Stop()
		assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.cleanupSecrets...)
	})
}

// driveTUIC sends one echo through B as account and checks that it reached own
// and not other.
func driveTUIC(t *testing.T, st *e2eTUICState, account, pass string, own, other *accountsUpstream) {
	t.Helper()
	a, b := own.count.Load(), other.count.Load()
	if err := e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, account, pass, st.backend); err != nil {
		e2eTUICDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("%s echo failed: %v", account, err)
	}
	if own.count.Load() != a+1 || other.count.Load() != b {
		e2eTUICDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("%s recorder routing mismatch", account)
	}
}

// checkTUICInitialRoutes drives both accounts through B and checks that a wrong
// password is refused as a TUIC authentication failure without reaching a recorder.
func checkTUICInitialRoutes(t *testing.T, st *e2eTUICState) {
	t.Helper()
	driveTUIC(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
	driveTUIC(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp)
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eTUICDirectEcho(st.ctx, st.links["alice"], st.backend, "wrong-password", true); !errors.Is(err, errE2ETUICAuthenticationFailure) {
		e2eTUICDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("wrong password not rejected as TUIC auth: %v", err)
	}
	if st.aliceUp.count.Load() != beforeA || st.bobUp.count.Load() != beforeB {
		t.Fatal("wrong-password reached recorder")
	}
}

// rotateTUICAlice rotates alice's credential, checks that the old link is refused
// and publishes the new link to the subscription server. It returns the new link.
func rotateTUICAlice(t *testing.T, st *e2eTUICState) string {
	t.Helper()
	oldAlice := st.links["alice"]
	if err := e2eTUICDirectEcho(st.ctx, oldAlice, st.backend, "rotation-before", false); err != nil {
		e2eTUICDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("pre-rotation echo failed: %v", err)
	}
	code, out, stderr := serverRun(t, st.homeA, "rotate", "tuic-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.reload++
	waitStatusReload(t, st.ctx, st.homeA, st.reload)
	if err := e2eTUICDirectEcho(st.ctx, oldAlice, st.backend, "rotation-after", false); !errors.Is(err, errE2ETUICAuthenticationFailure) {
		e2eTUICDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("old link did not return auth rejection: %v", err)
	}
	newAlice := e2eTUICMihomoLink(t, st.homeA, "tuic-alice", "alice", st.secrets)
	st.secrets = append(st.secrets, e2eTUICLinkSecrets(t, st.homeA, "tuic-alice", "alice")...)
	st.cleanupSecrets = append([]string(nil), st.secrets...)
	st.mu.Lock()
	st.current["alice"] = newAlice
	st.mu.Unlock()
	return newAlice
}

// refreshTUICAliceSubscription refreshes alice's subscription on B and drives
// alice through it.
func refreshTUICAliceSubscription(t *testing.T, st *e2eTUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("sub update failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveTUIC(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp)
}

// restrictTUICAlice limits alice to 192.0.2.0/24: her new link is refused promptly
// without touching the recorders, and bob still routes once the drop is counted.
func restrictTUICAlice(t *testing.T, st *e2eTUICState, newAlice string) {
	t.Helper()
	baseA, baseB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("set clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.reload++
	waitStatusReload(t, st.ctx, st.homeA, st.reload)
	if err := e2eTUICDirectEcho(st.ctx, newAlice, st.backend, "blocked", false); !e2eTUICRejected(err) {
		e2eTUICDiagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets)
		t.Fatalf("allowlist not promptly rejected: %v", err)
	}
	if st.aliceUp.count.Load() != baseA || st.bobUp.count.Load() != baseB {
		t.Fatal("blocked attempt changed recorder counters")
	}
	e2eTUICWaitDropIncrease(t, st.ctx, st.homeA, st.secrets)
	e2eTUICRecover(t, st.ctx, func() error {
		return e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	}, st.bobUp, st.aliceUp, baseB, baseA, "Bob under Alice restriction", st.homeA, st.homeB, st.secrets)
}

// clearTUICRestriction clears alice's allowlist and checks that both accounts
// route again.
func clearTUICRestriction(t *testing.T, st *e2eTUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.reload++
	waitStatusReload(t, st.ctx, st.homeA, st.reload)
	baseA, baseB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	e2eTUICRecover(t, st.ctx, func() error {
		return e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, "alice", st.alicePass, st.backend)
	}, st.aliceUp, st.bobUp, baseA, baseB, "Alice after clear", st.homeA, st.homeB, st.secrets)
	baseA, baseB = st.aliceUp.count.Load(), st.bobUp.count.Load()
	e2eTUICRecover(t, st.ctx, func() error {
		return e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	}, st.bobUp, st.aliceUp, baseB, baseA, "Bob after clear", st.homeA, st.homeB, st.secrets)
}

// restartTUICServer restarts A's angel, checks that both UDP ports are released
// and bound again, and that both accounts route afterwards.
func restartTUICServer(t *testing.T, st *e2eTUICState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("down failed: %s", serverErrorSummary(out, stderr))
	}
	st.serverAngel.Stop()
	if err := e2eTUICWaitUDPReleased(st.ctx, st.alicePort); err != nil {
		t.Fatal("Alice UDP remained after down")
	}
	if err := e2eTUICWaitUDPReleased(st.ctx, st.bobPort); err != nil {
		t.Fatal("Bob UDP remained after down")
	}
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	st.reload = 0
	if err := e2eTUICWaitStatus(t, st.ctx, st.homeA, true); err != nil {
		t.Fatalf("TUIC status after restart: %v", err)
	}
	baseA, baseB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	e2eTUICRecover(t, st.ctx, func() error {
		return e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, "alice", st.alicePass, st.backend)
	}, st.aliceUp, st.bobUp, baseA, baseB, "Alice after restart", st.homeA, st.homeB, st.secrets)
	baseA, baseB = st.aliceUp.count.Load(), st.bobUp.count.Load()
	e2eTUICRecover(t, st.ctx, func() error {
		return e2eTUICClientEcho(st.ctx, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	}, st.bobUp, st.aliceUp, baseB, baseA, "Bob after restart", st.homeA, st.homeB, st.secrets)
}

// removeTUICServices removes both TUIC edges, checks that their UDP ports are
// released and stops both angels.
func removeTUICServices(t *testing.T, st *e2eTUICState) {
	t.Helper()
	for _, name := range []string{"tuic-alice", "tuic-bob"} {
		code, out, stderr := serverRun(t, st.homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		st.reload++
		waitStatusReload(t, st.ctx, st.homeA, st.reload)
	}
	if err := e2eTUICWaitStatus(t, st.ctx, st.homeA, false); err != nil {
		t.Fatal("TUIC servers remain in status")
	}
	for _, p := range []int{st.alicePort, st.bobPort} {
		if err := e2eTUICWaitUDPReleased(st.ctx, p); err != nil {
			t.Fatalf("removed UDP not released: %v", err)
		}
	}
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.secrets...)
	st.serverAngel.Stop()
	st.clientAngel.Stop()
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.secrets...)
}

func e2eTUICSubscriptionBody(name string, up *accountsUpstream) string {
	host, p, err := net.SplitHostPort(up.listener.Addr().String())
	if err != nil {
		panic("invalid recorder address")
	}
	return fmt.Sprintf("proxies:\n  - {name: %q, type: socks5, server: %q, port: %s}\n", name, "::ffff:"+host, p)
}
func e2eTUICFreeUDPPort(t *testing.T, exclude int) int {
	t.Helper()
	var lastErr error
	for range 400 {
		l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("reserve TCP port")
		}
		p := listenerPort(t, l)
		if p == exclude {
			_ = l.Close()
			continue
		}
		pc, e := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if e == nil {
			_ = pc.Close()
			_ = l.Close()
			return p
		}
		lastErr = e
		_ = l.Close()
	}
	t.Fatalf("no TCP+UDP port available: %v", lastErr)
	return 0
}
func e2eTUICLinkSecrets(t *testing.T, h, n, a string) []string {
	t.Helper()
	code, out, stderr := serverRun(t, h, "link", n, "--account", a, "--format", "mihomo")
	if code != 0 {
		t.Fatalf("TUIC link failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, "", stderr)
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("link response absent")
	}
	nodes, err := subscription.Parse([]byte(env.Data.Link))
	if err != nil || len(nodes) != 1 {
		t.Fatal("Mihomo TUIC config parse failed")
	}
	m := nodes[0].Raw
	return []string{env.Data.Link, fmt.Sprint(m["uuid"]), fmt.Sprint(m["password"]), fmt.Sprint(m["fingerprint"])}
}
func e2eTUICMihomoLink(t *testing.T, h, n, a string, secrets []string) string {
	t.Helper()
	code, out, stderr := serverRun(t, h, "link", n, "--account", a, "--format", "mihomo")
	if code != 0 {
		t.Fatalf("TUIC link failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, "", stderr, secrets...)
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("link response absent")
	}
	nodes, err := subscription.Parse([]byte(env.Data.Link))
	if err != nil || len(nodes) != 1 {
		t.Fatal("Mihomo TUIC config parse failed")
	}
	m := nodes[0].Raw
	if m["skip-cert-verify"] != false || m["udp"] != false || m["fingerprint"] == nil {
		t.Fatal("TUIC TLS pin or verify setting missing")
	}
	return env.Data.Link
}
func e2eTUICClientEcho(ctx context.Context, proxy, user, pass, target string) error {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(dctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if deadline, ok := dctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var m [2]byte
	if _, err = io.ReadFull(c, m[:]); err != nil || m != [2]byte{5, 2} {
		return errors.New("SOCKS negotiation rejected")
	}
	userLen := len(user)
	if userLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	passLen := len(pass)
	if passLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	b := make([]byte, 0, 2+len(user)+1+len(pass))
	b = append(b, 1, byte(userLen))
	b = append(b, user...)
	b = append(b, byte(passLen))
	b = append(b, pass...)
	if _, err = c.Write(b); err != nil {
		return err
	}
	var ar [2]byte
	if _, err = io.ReadFull(c, ar[:]); err != nil || ar[1] != 0 {
		return errors.New("SOCKS auth rejected")
	}
	host, ps, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	p, err := strconv.Atoi(ps)
	if err != nil || ip == nil {
		return errors.New("invalid echo endpoint")
	}
	if p < 0 {
		return errors.New("invalid echo endpoint")
	}
	if p > math.MaxUint16 {
		return errors.New("invalid echo endpoint")
	}
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(p))
	if _, err = c.Write(req); err != nil {
		return err
	}
	var rep [4]byte
	if _, err = io.ReadFull(c, rep[:]); err != nil || rep[1] != 0 {
		return errors.New("SOCKS connect rejected")
	}
	var addrLen int
	switch rep[3] {
	case 1:
		addrLen = 4
	case 4:
		addrLen = 16
	case 3:
		var n [1]byte
		if _, err = io.ReadFull(c, n[:]); err != nil {
			return err
		}
		addrLen = int(n[0])
	default:
		return errors.New("unknown SOCKS address type")
	}
	if _, err = io.ReadFull(c, make([]byte, addrLen+2)); err != nil {
		return err
	}
	payload := []byte("tuic-routed-echo")
	if _, err = c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return errors.New("echo mismatch")
	}
	return nil
}
func e2eTUICDirectEcho(ctx context.Context, snippet, target, payload string, wrong bool) error {
	nodes, err := subscription.Parse([]byte(snippet))
	if err != nil || len(nodes) != 1 {
		return errors.New("TUIC config parse failed")
	}
	m := nodes[0].Raw
	uuid, _ := m["uuid"].(string)
	password, _ := m["password"].(string)
	if wrong {
		password = "known-wrong-tuic-password" //nolint:gosec // G101: fixed wrong secret for the rejection probe, not a credential
	}
	fp, _ := m["fingerprint"].(string)
	client, err := outbound.NewTuic(outbound.TuicOption{Name: "tuic-e2e", Server: nodes[0].Server, Port: nodes[0].Port, UUID: uuid, Password: password, SNI: fmt.Sprint(m["sni"]), ALPN: []string{"h3"}, CongestionController: fmt.Sprint(m["congestion-controller"]), Fingerprint: fp})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	host, ps, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	p, err := strconv.Atoi(ps)
	if err != nil {
		return err
	}
	if p < 0 {
		return errors.New("invalid echo port")
	}
	if p > math.MaxUint16 {
		return errors.New("invalid echo port")
	}
	dctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c, err := client.DialContext(dctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		if e2eTUICIsAuth(err) {
			return errE2ETUICAuthenticationFailure
		}
		return err
	}
	defer func() { _ = c.Close() }()
	if deadline, ok := dctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err = io.WriteString(c, payload); err != nil {
		if e2eTUICIsAuth(err) {
			return errE2ETUICAuthenticationFailure
		}
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		if e2eTUICIsAuth(err) {
			return errE2ETUICAuthenticationFailure
		}
		return err
	}
	if wrong {
		return errors.New("wrong password unexpectedly echoed")
	}
	if string(got) != payload {
		return errors.New("TUIC echo mismatch")
	}
	return nil
}
func e2eTUICIsAuth(err error) bool {
	var a *quic.ApplicationError
	return errors.As(err, &a) && a.ErrorCode == tuicv5.AuthenticationFailed
}
func e2eTUICRejected(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Timeout() {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, x := range []string{"forcibly closed", "connection reset", "connection was aborted", "broken pipe"} {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
func e2eTUICWaitUDPReleased(ctx context.Context, p int) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		pc, err := (&net.ListenConfig{}).ListenPacket(context.WithoutCancel(ctx), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err == nil {
			_ = pc.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func e2eTUICWaitStatus(t *testing.T, ctx context.Context, h string, present bool) error {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, h, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name     string `json:"name"`
						Protocol string `json:"protocol"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) == nil {
				seen := map[string]bool{}
				for _, s := range env.Data.Servers {
					if s.Protocol == "tuic" {
						seen[s.Name] = true
					}
				}
				if present && seen["tuic-alice"] && seen["tuic-bob"] {
					return nil
				}
				if !present && !seen["tuic-alice"] && !seen["tuic-bob"] {
					return nil
				}
			}
		} else {
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(stderr), &e) != nil || e.Error.Code != "ANGEL_NOT_RUNNING" {
				return errors.New("status failed unexpectedly")
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, h, "TUIC status wait expired")
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func e2eTUICWaitDropIncrease(t *testing.T, ctx context.Context, h string, secrets []string) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, h, "status")
		assertNoSecrets(t, out, stderr, secrets...)
		if code == 0 {
			var e struct {
				Data struct {
					Servers []struct {
						Name    string `json:"name"`
						Dropped uint64 `json:"dropped_clients"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &e) == nil {
				for _, s := range e.Data.Servers {
					if s.Name == "tuic-alice" && s.Dropped > 0 {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, h, "TUIC dropped status wait expired")
			t.Fatal("Alice drop counter did not increase")
		case <-tick.C:
		}
	}
}
func e2eTUICRecover(t *testing.T, ctx context.Context, echo func() error, own, other *accountsUpstream, ownBase, otherBase int64, label, a, b string, secrets []string) {
	t.Helper()
	end := time.NewTimer(60 * time.Second)
	defer end.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var err error
	for {
		err = echo()
		if err != nil {
			e2eTUICDiagnostics(context.WithoutCancel(ctx), t, a, b, secrets)
		}
		if err == nil && own.count.Load() > ownBase && other.count.Load() == otherBase {
			return
		}
		select {
		case <-end.C:
			e2eTUICDiagnostics(context.WithoutCancel(ctx), t, a, b, secrets)
			t.Fatalf("%s did not recover in 60 seconds", label)
		case <-ctx.Done():
			e2eTUICDiagnostics(context.WithoutCancel(ctx), t, a, b, secrets)
			t.Fatalf("%s context expired", label)
		case <-tick.C:
		}
	}
}
func e2eTUICDiagnostics(ctx context.Context, t *testing.T, a, b string, secrets []string) {
	t.Helper()
	for _, h := range []string{a, b} {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, h, "status", "--full")
		if code == 0 {
			assertNoSecrets(t, out, stderr, secrets...)
			var e struct {
				Data struct {
					Servers json.RawMessage `json:"servers"`
					Nodes   json.RawMessage `json:"nodes"`
					Warm    json.RawMessage `json:"warm"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &e) == nil {
				t.Logf("status servers=%s node-health=%s warm=%s", e.Data.Servers, e.Data.Nodes, e.Data.Warm)
			}
		}
		code, out, stderr = accountsRun(context.WithoutCancel(ctx), t, h, "logs", "--since", "2m", "--tail", "12")
		if code == 0 {
			assertNoSecrets(t, out, stderr, secrets...)
			var e struct {
				Data struct {
					Entries []struct {
						Message string `json:"message"`
					} `json:"entries"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &e) == nil {
				for _, x := range e.Data.Entries {
					line := x.Message
					for _, s := range secrets {
						if s != "" {
							line = strings.ReplaceAll(line, s, "[redacted]")
						}
					}
					line = regexp.MustCompile(`\S*://\S+`).ReplaceAllString(line, "[redacted-url]")
					t.Logf("redacted log: %s", line)
				}
			}
		}
	}
}
