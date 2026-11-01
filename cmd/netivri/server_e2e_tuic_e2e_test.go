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
	"regexp"
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
	tuicv5 "github.com/metacubex/mihomo/transport/tuic/v5"
	"github.com/metacubex/quic-go"
)

var e2eTUICAuthenticationFailure = errors.New("TUIC authentication rejected")

func TestE2EServerTUICLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const alicePass = "tuic-e2e-alice-account-password"
	const bobPass = "tuic-e2e-bob-account-password"
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
	writeConfig(t, homeA, func(c *config.Config) {
		c.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "alice-route"}, {Login: "bob", Password: bobHash, Provider: "bob-route"}}
		c.Subscriptions = []config.Subscription{{Name: "alice-route", URL: accountsSubServer(t, e2eTUICSubscriptionBody("alice-route-node", aliceUp))}, {Name: "bob-route", URL: accountsSubServer(t, e2eTUICSubscriptionBody("bob-route-node", bobUp))}}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", bobUp)})
	secrets := []string{alicePass, bobPass, aliceHash, bobHash}
	cleanupSecrets := append([]string(nil), secrets...)
	serverAngel := startUp(t, homeA, "--port", "0")
	t.Cleanup(func() {
		serverAngel.Stop()
		assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), cleanupSecrets...)
	})
	alicePort := e2eTUICFreeUDPPort(t, 0)
	bobPort := 0
	reload := uint64(0)
	links := map[string]string{}
	for _, account := range []string{"alice", "bob"} {
		port := alicePort
		if account == "bob" {
			bobPort = e2eTUICFreeUDPPort(t, alicePort)
			port = bobPort
		}
		name := "tuic-" + account
		code, out, stderr := serverRun(t, homeA, "add", "--protocol", "tuic", "--name", name, "--account", account, "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
		if code != 0 {
			t.Fatalf("TUIC add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		reload++
		waitStatusReload(t, ctx, homeA, reload)
		links[account] = e2eTUICMihomoLink(t, homeA, name, account, secrets)
		secrets = append(secrets, e2eTUICLinkSecrets(t, homeA, name, account)...)
		cleanupSecrets = append([]string(nil), secrets...)
	}
	if err := e2eTUICWaitStatus(t, ctx, homeA, true); err != nil {
		t.Fatalf("TUIC status did not report both servers: %v", err)
	}
	var mu sync.RWMutex
	current := map[string]string{"alice": links["alice"], "bob": links["bob"]}
	mux := http.NewServeMux()
	mux.HandleFunc("/alice", func(w http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		_, _ = io.WriteString(w, current["alice"])
	})
	mux.HandleFunc("/bob", func(w http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		_, _ = io.WriteString(w, current["bob"])
	})
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
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
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr := accountsRun(t, homeB, "sub", "add", "http://"+subListener.Addr().String()+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("local subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
	}
	clientConfig := loadConfigFile(t, homeB)
	for i := range clientConfig.Accounts {
		switch clientConfig.Accounts[i].Login {
		case "alice":
			clientConfig.Accounts[i].Password = aliceHash
		case "bob":
			clientConfig.Accounts[i].Password = bobHash
		}
	}
	if err := config.Save(config.ConfigPath(homeB), clientConfig); err != nil {
		t.Fatal("save client account credentials")
	}
	clientAngel := startUp(t, homeB, "--port", "0")
	t.Cleanup(func() {
		clientAngel.Stop()
		assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), cleanupSecrets...)
	})
	drive := func(account, pass string, own, other *accountsUpstream) {
		t.Helper()
		a, b := own.count.Load(), other.count.Load()
		if err := e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, account, pass, backend); err != nil {
			e2eTUICDiagnostics(t, homeA, homeB, secrets)
			t.Fatalf("%s echo failed: %v", account, err)
		}
		if own.count.Load() != a+1 || other.count.Load() != b {
			e2eTUICDiagnostics(t, homeA, homeB, secrets)
			t.Fatalf("%s recorder routing mismatch", account)
		}
	}
	drive("alice", alicePass, aliceUp, bobUp)
	drive("bob", bobPass, bobUp, aliceUp)
	beforeA, beforeB := aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eTUICDirectEcho(ctx, links["alice"], backend, "wrong-password", true); !errors.Is(err, e2eTUICAuthenticationFailure) {
		e2eTUICDiagnostics(t, homeA, homeB, secrets)
		t.Fatalf("wrong password not rejected as TUIC auth: %v", err)
	}
	if aliceUp.count.Load() != beforeA || bobUp.count.Load() != beforeB {
		t.Fatal("wrong-password reached recorder")
	}
	oldAlice := links["alice"]
	if err := e2eTUICDirectEcho(ctx, oldAlice, backend, "rotation-before", false); err != nil {
		e2eTUICDiagnostics(t, homeA, homeB, secrets)
		t.Fatalf("pre-rotation echo failed: %v", err)
	}
	code, out, stderr := serverRun(t, homeA, "rotate", "tuic-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	reload++
	waitStatusReload(t, ctx, homeA, reload)
	if err := e2eTUICDirectEcho(ctx, oldAlice, backend, "rotation-after", false); !errors.Is(err, e2eTUICAuthenticationFailure) {
		e2eTUICDiagnostics(t, homeA, homeB, secrets)
		t.Fatalf("old link did not return auth rejection: %v", err)
	}
	newAlice := e2eTUICMihomoLink(t, homeA, "tuic-alice", "alice", secrets)
	secrets = append(secrets, e2eTUICLinkSecrets(t, homeA, "tuic-alice", "alice")...)
	cleanupSecrets = append([]string(nil), secrets...)
	mu.Lock()
	current["alice"] = newAlice
	mu.Unlock()
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("sub update failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeB, 1)
	drive("alice", alicePass, aliceUp, bobUp)
	baseA, baseB := aliceUp.count.Load(), bobUp.count.Load()
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("set clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	reload++
	waitStatusReload(t, ctx, homeA, reload)
	if err := e2eTUICDirectEcho(ctx, newAlice, backend, "blocked", false); !e2eTUICRejected(err) {
		e2eTUICDiagnostics(t, homeA, homeB, secrets)
		t.Fatalf("allowlist not promptly rejected: %v", err)
	}
	if aliceUp.count.Load() != baseA || bobUp.count.Load() != baseB {
		t.Fatal("blocked attempt changed recorder counters")
	}
	e2eTUICWaitDropIncrease(t, ctx, homeA, secrets)
	e2eTUICRecover(t, ctx, func() error { return e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, "bob", bobPass, backend) }, bobUp, aliceUp, baseB, baseA, "Bob under Alice restriction", homeA, homeB, secrets)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	reload++
	waitStatusReload(t, ctx, homeA, reload)
	baseA, baseB = aliceUp.count.Load(), bobUp.count.Load()
	e2eTUICRecover(t, ctx, func() error {
		return e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, "alice", alicePass, backend)
	}, aliceUp, bobUp, baseA, baseB, "Alice after clear", homeA, homeB, secrets)
	baseA, baseB = aliceUp.count.Load(), bobUp.count.Load()
	e2eTUICRecover(t, ctx, func() error { return e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, "bob", bobPass, backend) }, bobUp, aliceUp, baseB, baseA, "Bob after clear", homeA, homeB, secrets)
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("down failed: %s", serverErrorSummary(out, stderr))
	}
	serverAngel.Stop()
	if err := e2eTUICWaitUDPReleased(ctx, alicePort); err != nil {
		t.Fatal("Alice UDP remained after down")
	}
	if err := e2eTUICWaitUDPReleased(ctx, bobPort); err != nil {
		t.Fatal("Bob UDP remained after down")
	}
	serverAngel = startUp(t, homeA, "--port", "0")
	reload = 0
	if err := e2eTUICWaitStatus(t, ctx, homeA, true); err != nil {
		t.Fatalf("TUIC status after restart: %v", err)
	}
	baseA, baseB = aliceUp.count.Load(), bobUp.count.Load()
	e2eTUICRecover(t, ctx, func() error {
		return e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, "alice", alicePass, backend)
	}, aliceUp, bobUp, baseA, baseB, "Alice after restart", homeA, homeB, secrets)
	baseA, baseB = aliceUp.count.Load(), bobUp.count.Load()
	e2eTUICRecover(t, ctx, func() error { return e2eTUICClientEcho(ctx, clientAngel.Status().Data.Listen, "bob", bobPass, backend) }, bobUp, aliceUp, baseB, baseA, "Bob after restart", homeA, homeB, secrets)
	for _, name := range []string{"tuic-alice", "tuic-bob"} {
		code, out, stderr = serverRun(t, homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		reload++
		waitStatusReload(t, ctx, homeA, reload)
	}
	if err := e2eTUICWaitStatus(t, ctx, homeA, false); err != nil {
		t.Fatal("TUIC servers remain in status")
	}
	for _, p := range []int{alicePort, bobPort} {
		if err := e2eTUICWaitUDPReleased(ctx, p); err != nil {
			t.Fatalf("removed UDP not released: %v", err)
		}
	}
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), secrets...)
	serverAngel.Stop()
	clientAngel.Stop()
	assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), secrets...)
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
	for i := 0; i < 40; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("reserve TCP port")
		}
		p := l.Addr().(*net.TCPAddr).Port
		if p == exclude {
			_ = l.Close()
			continue
		}
		pc, e := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if e == nil {
			_ = pc.Close()
			_ = l.Close()
			return p
		}
		_ = l.Close()
	}
	t.Fatal("no TCP+UDP port available")
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
	defer c.Close()
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
	b := []byte{1, byte(len(user))}
	b = append(b, user...)
	b = append(b, byte(len(pass)))
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
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = append(req, byte(p>>8), byte(p))
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
		password = "known-wrong-tuic-password"
	}
	fp, _ := m["fingerprint"].(string)
	client, err := outbound.NewTuic(outbound.TuicOption{Name: "tuic-e2e", Server: nodes[0].Server, Port: nodes[0].Port, UUID: uuid, Password: password, SNI: fmt.Sprint(m["sni"]), ALPN: []string{"h3"}, CongestionController: fmt.Sprint(m["congestion-controller"]), Fingerprint: fp})
	if err != nil {
		return err
	}
	defer client.Close()
	host, ps, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	p, err := strconv.Atoi(ps)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c, err := client.DialContext(dctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		if e2eTUICIsAuth(err) {
			return e2eTUICAuthenticationFailure
		}
		return err
	}
	defer c.Close()
	if deadline, ok := dctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err = io.WriteString(c, payload); err != nil {
		if e2eTUICIsAuth(err) {
			return e2eTUICAuthenticationFailure
		}
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		if e2eTUICIsAuth(err) {
			return e2eTUICAuthenticationFailure
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
		pc, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
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
		code, out, stderr := accountsRun(t, h, "status")
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
			serverE2EFailureDiagnostics(t, h, "TUIC status wait expired")
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
		code, out, stderr := accountsRun(t, h, "status")
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
			serverE2EFailureDiagnostics(t, h, "TUIC dropped status wait expired")
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
			e2eTUICDiagnostics(t, a, b, secrets)
		}
		if err == nil && own.count.Load() > ownBase && other.count.Load() == otherBase {
			return
		}
		select {
		case <-end.C:
			e2eTUICDiagnostics(t, a, b, secrets)
			t.Fatalf("%s did not recover in 60 seconds", label)
		case <-ctx.Done():
			e2eTUICDiagnostics(t, a, b, secrets)
			t.Fatalf("%s context expired", label)
		case <-tick.C:
		}
	}
}
func e2eTUICDiagnostics(t *testing.T, a, b string, secrets []string) {
	t.Helper()
	for _, h := range []string{a, b} {
		code, out, stderr := accountsRun(t, h, "status", "--full")
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
		code, out, stderr = accountsRun(t, h, "logs", "--since", "2m", "--tail", "12")
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
