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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

var e2eTrustTunnelRejectedError = errors.New("TrustTunnel authentication rejected")

func TestE2EServerTrustTunnelLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const pass = "trusttunnel-e2e-account-password"
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
	server := startUp(t, homeA, "--port", "0")
	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "trusttunnel", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass, hash)
	waitStatusReload(t, ctx, homeA, 1)
	e2eTrustTunnelWaitStatus(t, ctx, homeA, true, pass, hash)
	if err := waitServerPort(ctx, port, true); err != nil {
		t.Fatal("TrustTunnel listener did not open")
	}
	links := make(map[string]string, 2)
	getLink := func(account string) string {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, "link", "edge", "--account", account, "--format", "mihomo")
		if code != 0 {
			t.Fatalf("server link failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, pass, hash)
		var env struct {
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
			t.Fatal("server link response did not contain a profile")
		}
		links[account] = env.Data.Link
		return env.Data.Link
	}
	aliceProfile, bobProfile := getLink("alice"), getLink("bob")
	allSecrets := []string{pass, hash}
	allSecrets = append(allSecrets, e2eTrustTunnelCredentials(t, links)...)
	serverOutputSecrets := append([]string(nil), allSecrets...)
	serverCleanupSet := &e2eTrustTunnelProcessCleanup{proc: server, secrets: &serverOutputSecrets}
	t.Cleanup(func() { e2eTrustTunnelProcessCleanupRun(serverCleanupSet) })
	profiles := &e2eTrustTunnelSubMux{body: map[string]string{"alice": aliceProfile + "\n", "bob": bobProfile + "\n"}}
	subServer := &http.Server{Handler: profiles, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
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
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		url := "http://" + subListener.Addr().String() + "/" + account
		code, out, stderr = accountsRun(t, homeB, "sub", "add", url, "--name", "edge-"+account)
		if code != 0 {
			t.Fatalf("subscription add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, allSecrets...)
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", "edge-"+account)
		if code != 0 {
			t.Fatalf("account add failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, allSecrets...)
	}
	client := startUp(t, homeB, "--port", "0")
	t.Cleanup(func() {
		client.Stop()
		assertNoSecrets(t, client.Stdout(), client.Stderr(), allSecrets...)
	})
	// B can retain dead-node backoff, so restart it to make fresh probes.
	restartB := func() {
		code, out, stderr := accountsRun(t, homeB, "down")
		if code != 0 {
			t.Fatalf("angel B down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, allSecrets...)
		client.Stop()
		assertNoSecrets(t, client.Stdout(), client.Stderr(), allSecrets...)
		client = startUp(t, homeB, "--port", "0")
	}
	probe := func(raw, payload string, wrong bool) error {
		t.Helper()
		proxy, err := e2eTrustTunnelAdapter(raw)
		if err != nil {
			return fmt.Errorf("invalid TrustTunnel profile")
		}
		if wrong {
			proxy["password"] = "known-incorrect-trusttunnel-password"
		}
		parsed, err := adapter.ParseProxy(proxy)
		if err != nil {
			return fmt.Errorf("TrustTunnel profile parse failed")
		}
		outbound := parsed.Adapter()
		defer outbound.Close()
		host, text, err := net.SplitHostPort(backend)
		if err != nil {
			return fmt.Errorf("invalid echo address")
		}
		n, err := strconv.Atoi(text)
		if err != nil {
			return fmt.Errorf("invalid echo port")
		}
		probeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		started := time.Now()
		// The classified error hides the cause; the log (shown only when the
		// test fails) names the stage, the error type and its redacted text.
		classify := func(stage string, err error) error {
			t.Logf("TrustTunnel probe %q failed at %s after %s: %T: %s", payload, stage, time.Since(started).Round(time.Millisecond), err, e2eRedactDiagnostic(err.Error()))
			return e2eTrustTunnelClassify(err, probeCtx)
		}
		conn, err := outbound.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(n)})
		if err != nil {
			return classify("dial", err)
		}
		defer conn.Close()
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
			return fmt.Errorf("unexpected echo")
		}
		return nil
	}
	drive := func(login string, retry bool) {
		t.Helper()
		beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
		deadline := time.Now().Add(30 * time.Second)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if !retry && ctx.Err() != nil {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s drive context expired", login)
			}
			if retry && (ctx.Err() != nil || !time.Now().Before(deadline)) {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s retry deadline exceeded", login)
			}
			attemptCtx := ctx
			cancelAttempt := func() {}
			if retry {
				attemptCtx, cancelAttempt = context.WithDeadline(ctx, deadline)
			}
			err := e2eTrustTunnelSocksEcho(attemptCtx, client.Status().Data.Listen, login, pass, backend)
			cancelAttempt()
			if ctx.Err() != nil {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s drive context expired", login)
			}
			if retry && !time.Now().Before(deadline) {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s retry deadline exceeded", login)
			}
			if err == nil {
				break
			}
			if !retry {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s client echo failed", login)
			}
			e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
			select {
			case <-ctx.Done():
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatalf("%s retry deadline exceeded", login)
			case <-ticker.C:
			}
		}
		if login == "alice" {
			if aliceUp.count.Load() <= beforeAlice || bobUp.count.Load() != beforeBob {
				e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
				t.Fatal("Alice recorder routing mismatch")
			}
		} else if bobUp.count.Load() <= beforeBob || aliceUp.count.Load() != beforeAlice {
			e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
			t.Fatal("Bob recorder routing mismatch")
		}
	}
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	if err := probe(aliceProfile, "wrong-credentials", true); err == nil || e2eTrustTunnelIsTimeout(err) || !errors.Is(err, e2eTrustTunnelRejectedError) {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("wrong TrustTunnel credentials were not rejected with an authentication response")
	}
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("wrong credentials reached an account recorder")
	}
	drive("alice", false)
	drive("bob", false)
	oldAlice := aliceProfile
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotate failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 2)
	if err := probe(oldAlice, "old-profile", false); !errors.Is(err, e2eTrustTunnelRejectedError) {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("old Alice profile was not rejected after rotation")
	}
	aliceProfile = getLink("alice")
	allSecrets = append(allSecrets, e2eTrustTunnelCredentials(t, map[string]string{"alice": aliceProfile})...)
	serverOutputSecrets = append([]string(nil), allSecrets...)
	profiles.set("alice", aliceProfile+"\n")
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "edge-alice")
	if code != 0 {
		t.Fatalf("subscription update failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeB, 1)
	drive("alice", false)
	drive("bob", false)
	beforeDropped, present := e2eTrustTunnelDropped(t, ctx, homeA, allSecrets)
	if !present {
		t.Fatal("TrustTunnel status unavailable before account policy")
	}
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.1")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 3)
	beforeAlice = aliceUp.count.Load()
	if err := probe(aliceProfile, "restricted-valid-credentials", false); err == nil || e2eTrustTunnelIsTimeout(err) || !errors.Is(err, e2eTrustTunnelRejectedError) {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("loopback Alice traffic was not denied by the account policy")
	}
	if aliceUp.count.Load() != beforeAlice {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("disallowed Alice traffic reached her recorder")
	}
	e2eTrustTunnelWaitDropped(t, ctx, homeA, beforeDropped+1, allSecrets)
	restartB()
	drive("bob", true)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("restore Alice clients failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 4)
	restartB()
	drive("alice", true)
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	server.Stop()
	assertNoSecrets(t, server.Stdout(), server.Stderr(), allSecrets...)
	serverCleanupSet.proc = nil
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatal("TrustTunnel listener did not close at restart")
	}
	server = startUp(t, homeA, "--port", "0")
	serverCleanupSet.proc = server
	serverCleanupSet.secrets = &serverOutputSecrets
	restartB()
	if err := probe(aliceProfile, "restart-alice-profile", false); err != nil {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("same Alice profile failed after restart")
	}
	if err := probe(bobProfile, "restart-bob-profile", false); err != nil {
		e2eTrustTunnelDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatal("same Bob profile failed after restart")
	}
	drive("alice", true)
	drive("bob", true)
	baselineReload := e2eTrustTunnelReloadCount(t, ctx, homeA, allSecrets)
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed (exit %d): %s", code, serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, baselineReload+1)
	e2eTrustTunnelWaitStatus(t, ctx, homeA, false, allSecrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
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
		return fmt.Errorf("TrustTunnel operation timed out")
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "unexpected status code: 407") || e2eTrustTunnelNetworkReject(err) {
		return fmt.Errorf("%w", e2eTrustTunnelRejectedError)
	}
	return fmt.Errorf("TrustTunnel operation failed")
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
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == 10053 || errno == 10054) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, phrase := range []string{"forcibly closed", "connection reset", "connection was aborted", "broken pipe", "unexpected status code: 407", "eof"} {
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
		return nil, fmt.Errorf("invalid profile")
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
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 2} {
		return fmt.Errorf("SOCKS method negotiation failed")
	}
	request := append([]byte{1, byte(len(user))}, []byte(user)...)
	request = append(request, byte(len(pass)))
	request = append(request, []byte(pass)...)
	if _, err = conn.Write(request); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil || authReply[1] != 0 {
		return fmt.Errorf("SOCKS authentication failed")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid echo target")
	}
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err = conn.Write(req); err != nil {
		return err
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[1] != 0 {
		return fmt.Errorf("SOCKS connect failed")
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
		return fmt.Errorf("unexpected SOCKS echo")
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
		code, out, stderr := accountsRun(t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name, Protocol, Listen string
						Accounts               []string
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
			serverE2EFailureDiagnostics(t, home, "TrustTunnel status wait expired")
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
		code, out, stderr := accountsRun(t, home, "status")
		if code == 0 {
			var status liveStatus
			if json.Unmarshal([]byte(out), &status) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				return status.Data.Reload.Count
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, "TrustTunnel reload status wait expired")
			t.Fatal("angel status unavailable after restart")
		case <-ticker.C:
		}
	}
}

func e2eTrustTunnelDropped(t *testing.T, ctx context.Context, home string, secrets []string) (uint64, bool) {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "status")
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
		if dropped, present := e2eTrustTunnelDropped(t, ctx, home, secrets); present && dropped >= minimum {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, "TrustTunnel dropped status wait expired")
			t.Fatal("TrustTunnel dropped-client counter did not increment")
		case <-ticker.C:
		}
	}
}

func e2eTrustTunnelDiagnostics(t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	for _, item := range []struct {
		home string
		args []string
	}{{homeA, []string{"status"}}, {homeA, []string{"logs", "--since", "2m", "--tail", "20"}}, {homeB, []string{"status", "--full"}}} {
		code, out, stderr := accountsRun(t, item.home, item.args...)
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
		if strings.Contains(item.args[0], "logs") {
			t.Logf("A logs: %s", e2eTrustTunnelRedact(fmt.Sprint(data), secrets))
		} else if item.home == homeA {
			t.Logf("A servers: %s", e2eTrustTunnelRedact(fmt.Sprint(data["servers"]), secrets))
		} else {
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
