//go:build e2e

package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
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
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/anytls/padding"
	"github.com/metacubex/mihomo/transport/vmess"
	M "github.com/metacubex/sing/common/metadata"
)

var e2eAnyTLSRejected = errors.New("AnyTLS authentication or policy rejection")

func TestE2EAnyTLSRealCLILifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const alicePass = "anytls-e2e-alice-account-password"
	const bobPass = "anytls-e2e-bob-account-password"
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
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: aliceHash, Provider: "alice-route"},
			{Login: "bob", Password: bobHash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", bobUp)})
	secrets := []string{alicePass, bobPass, aliceHash, bobHash}
	serverAngel := startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverAngel.Status())
	initialServerAngel := serverAngel
	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "anytls", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eAnyTLSWaitStatus(t, ctx, homeA, true, secrets...)
	links := map[string]string{"alice": e2eAnyTLSLink(t, homeA, "alice"), "bob": e2eAnyTLSLink(t, homeA, "bob")}
	secrets = append(secrets, links["alice"], links["bob"])
	secrets = append(secrets, e2eAnyTLSPasswords(links)...)
	var linkMu sync.RWMutex
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		account := account
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			linkMu.RLock()
			defer linkMu.RUnlock()
			_, _ = io.WriteString(w, links[account]+"\n")
		})
	}
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
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
	go func() { defer close(subDone); _ = subServer.Serve(subListener) }()
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-edge"
		code, out, stderr = accountsRun(t, homeB, "sub", "add", "http://"+subListener.Addr().String()+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
	}
	clientAngel := startUp(t, homeB, "--port", "0")
	assertStatusSuccess(t, clientAngel.Status())
	restartB := func() {
		t.Helper()
		code, out, stderr := accountsRun(t, homeB, "down")
		if code != 0 {
			t.Fatalf("B down failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		clientAngel.Stop()
		assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), secrets...)
		clientAngel = startUp(t, homeB, "--port", "0")
		assertStatusSuccess(t, clientAngel.Status())
		assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), secrets...)
	}
	// Restart B after A changes: probes may be dropped or A's core replaced, leaving dead-node health/backoff cached on B.
	drive := func(account, pass string, own, other *accountsUpstream, retry bool) {
		t.Helper()
		baseOwn, baseOther := own.count.Load(), other.count.Load()
		var retryDeadline time.Time
		if retry {
			retryDeadline = time.Now().Add(20 * time.Second)
		}
		var last error
		for {
			attemptCtx := ctx
			cancelAttempt := func() {}
			if retry {
				attemptCtx, cancelAttempt = context.WithDeadline(ctx, retryDeadline)
			}
			err := e2eAnyTLSClientEcho(attemptCtx, clientAngel.Status().Data.Listen, account, pass, backend)
			cancelAttempt()
			if err == nil {
				if own.count.Load() > baseOwn && other.count.Load() == baseOther {
					return
				}
				last = fmt.Errorf("recorder routing mismatch")
			} else {
				last = err
			}
			if !retry || !time.Now().Before(retryDeadline) {
				break
			}
			t.Logf("AnyTLS %s echo attempt failed: %v", account, last)
			select {
			case <-ctx.Done():
				break
			case <-time.After(100 * time.Millisecond):
			}
			if ctx.Err() != nil {
				break
			}
		}
		t.Logf("AnyTLS %s echo failure: %v; %s", account, last, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatalf("AnyTLS %s echo/routing failed", account)
	}
	baselineA, baselineB := aliceUp.count.Load(), bobUp.count.Load()
	drive("alice", alicePass, aliceUp, bobUp, false)
	drive("bob", bobPass, bobUp, aliceUp, false)
	if aliceUp.count.Load() != baselineA+1 || bobUp.count.Load() != baselineB+1 {
		t.Logf("initial recorder count mismatch; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("initial recorder increments mismatch")
	}
	if err := e2eAnyTLSWireProbe(ctx, links["alice"], "wrong-anytls-password", backend, false); !errors.Is(err, e2eAnyTLSRejected) {
		t.Logf("wrong-password AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("wrong AnyTLS password was not rejected by the server")
	}
	if aliceUp.count.Load() != baselineA+1 || bobUp.count.Load() != baselineB+1 {
		t.Logf("failed-auth recorder count mismatch; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("failed AnyTLS authentication reached a recorder")
	}
	oldAlice := links["alice"]
	if err := e2eAnyTLSPositive(ctx, oldAlice, backend); err != nil {
		t.Logf("AnyTLS pre-rotation echo failure: %v; %s", err, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("Alice link pre-rotation echo failed")
	}
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 2)
	oldPasswordAlice, oldPasswordBob := aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eAnyTLSWireProbe(ctx, oldAlice, "", backend, false); !errors.Is(err, e2eAnyTLSRejected) {
		t.Logf("rotated-password AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("old Alice password was not rejected after rotation")
	}
	if aliceUp.count.Load() != oldPasswordAlice || bobUp.count.Load() != oldPasswordBob {
		t.Logf("rotated-password recorder changed; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("old Alice password reached an account recorder")
	}
	freshAlice := e2eAnyTLSLink(t, homeA, "alice")
	secrets = append(secrets, freshAlice)
	secrets = append(secrets, e2eAnyTLSPasswords(map[string]string{"alice": freshAlice})...)
	linkMu.Lock()
	links["alice"] = freshAlice
	linkMu.Unlock()
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeB, 1)
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	drive("alice", alicePass, aliceUp, bobUp, false)
	if aliceUp.count.Load() != beforeAlice+1 || bobUp.count.Load() != beforeBob {
		t.Logf("refreshed Alice route mismatch; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("refreshed Alice subscription routed incorrectly")
	}
	beforeAlice, beforeBob = aliceUp.count.Load(), bobUp.count.Load()
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 3)
	restartB()
	if err := e2eAnyTLSWireProbe(ctx, freshAlice, "", backend, true); !errors.Is(err, e2eAnyTLSRejected) {
		t.Logf("allowed-client AnyTLS probe failure: %v; %s", err, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("disallowed Alice client was not refused")
	}
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		t.Logf("disallowed route counter changed; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("disallowed client reached a recorder")
	}
	e2eAnyTLSWaitDropped(t, ctx, homeA, 1, secrets...)
	drive("bob", bobPass, bobUp, aliceUp, true)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 4)
	restartB()
	beforeAlice, beforeBob = aliceUp.count.Load(), bobUp.count.Load()
	drive("alice", alicePass, aliceUp, bobUp, true)
	if aliceUp.count.Load() <= beforeAlice || bobUp.count.Load() != beforeBob {
		t.Logf("Alice route did not recover; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("Alice route did not recover after policy clear")
	}
	drive("bob", bobPass, bobUp, aliceUp, true)
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("down failed: %s", serverErrorSummary(out, stderr))
	}
	serverAngel.Stop()
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), secrets...)
	initialServerAngel.Stop()
	assertNoSecrets(t, initialServerAngel.Stdout(), initialServerAngel.Stderr(), secrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("AnyTLS port stayed open after down: %v", err)
	}
	serverAngel = startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverAngel.Status())
	if err := waitServerPort(ctx, port, true); err != nil {
		t.Fatalf("AnyTLS port did not reopen: %v", err)
	}
	if e2eAnyTLSLink(t, homeA, "alice") != freshAlice {
		t.Logf("post-restart AnyTLS link changed; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("Alice server link changed across restart")
	}
	if e2eAnyTLSLink(t, homeA, "bob") != links["bob"] {
		t.Logf("post-restart Bob link changed; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("Bob server link changed across restart")
	}
	restartB()
	beforeAlice, beforeBob = aliceUp.count.Load(), bobUp.count.Load()
	drive("alice", alicePass, aliceUp, bobUp, true)
	if aliceUp.count.Load() <= beforeAlice || bobUp.count.Load() != beforeBob {
		t.Logf("post-restart Alice routing failure; %s", e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("same Alice subscription failed after restart")
	}
	drive("bob", bobPass, bobUp, aliceUp, true)
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eAnyTLSWaitStatus(t, ctx, homeA, false, secrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("removed AnyTLS port remains open: %v", err)
	}
	serverAngel.Stop()
	clientAngel.Stop()
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), secrets...)
	assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), secrets...)
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
		return nil, fmt.Errorf("AnyTLS URI parse failed")
	}
	n := nodes[0]
	password, _ := n.Raw["password"].(string)
	pin, _ := n.Raw["fingerprint"].(string)
	if n.Server == "" || n.Port < 1 || password == "" || pin == "" {
		return nil, fmt.Errorf("AnyTLS link missing pinned endpoint")
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
	defer client.Close()
	host, pt, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("echo endpoint invalid")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return fmt.Errorf("echo port invalid")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	clientCtx, clientCancel := context.WithTimeout(probeCtx, 4*time.Second)
	defer clientCancel()
	conn, err := client.DialContext(clientCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		return fmt.Errorf("AnyTLS positive dial failed")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("anytls-real-cli-echo")
	if _, err = conn.Write(payload); err != nil {
		return fmt.Errorf("AnyTLS echo write failed")
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		return fmt.Errorf("AnyTLS echo failed")
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
		return fmt.Errorf("client proxy dial")
	}
	defer c.Close()
	_ = c.SetDeadline(deadline)
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return fmt.Errorf("SOCKS greeting write")
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		return fmt.Errorf("SOCKS auth method missing")
	}
	if _, err = c.Write(append([]byte{1, byte(len(user))}, append(append([]byte(user), byte(len(pass))), []byte(pass)...)...)); err != nil {
		return fmt.Errorf("SOCKS auth write")
	}
	var authReply [2]byte
	if _, err = io.ReadFull(c, authReply[:]); err != nil || authReply[1] != 0 {
		return fmt.Errorf("SOCKS auth rejected")
	}
	host, pt, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("echo address invalid")
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("echo host is not IPv4")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return fmt.Errorf("echo port invalid")
	}
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = append(req, byte(p>>8), byte(p))
	if _, err = c.Write(req); err != nil {
		return fmt.Errorf("SOCKS connect write")
	}
	var reply [10]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		return fmt.Errorf("SOCKS connect rejected")
	}
	payload := []byte("anytls-routed-echo")
	if _, err = c.Write(payload); err != nil {
		return fmt.Errorf("routed echo write")
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
		return fmt.Errorf("routed echo failed")
	}
	return nil
}
func e2eAnyTLSWireProbe(ctx context.Context, uri, passwordOverride, backend string, policy bool) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		return fmt.Errorf("AnyTLS URI parse failed")
	}
	n := nodes[0]
	password, _ := n.Raw["password"].(string)
	pin, _ := n.Raw["fingerprint"].(string)
	if passwordOverride != "" {
		password = passwordOverride
	}
	if password == "" || pin == "" {
		return fmt.Errorf("AnyTLS URI credentials or pin missing")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", net.JoinHostPort(n.Server, strconv.Itoa(n.Port)))
	if err != nil {
		return fmt.Errorf("AnyTLS TCP dial failed")
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(4 * time.Second))
	tlsHost := e2eAnyTLSString(n.Raw["sni"])
	if tlsHost == "" {
		tlsHost = n.Server
	}
	tlsConn, err := vmess.StreamTLSConn(probeCtx, raw, &vmess.TLSConfig{Host: tlsHost, FingerPrint: pin, ClientFingerprint: "chrome"})
	if err != nil {
		return fmt.Errorf("pinned TLS handshake failed")
	}
	defer tlsConn.Close()
	digest := sha256.Sum256([]byte(password))
	if _, err = tlsConn.Write(append(append([]byte(nil), digest[:]...), 0, 0)); err != nil {
		return fmt.Errorf("AnyTLS auth prelude write failed")
	}
	if !policy {
		var b [1]byte
		_, err = tlsConn.Read(b[:])
		if err == nil {
			return fmt.Errorf("rejected credential received data")
		}
		if e2eAnyTLSIsTimeout(err) {
			return fmt.Errorf("AnyTLS rejection timed out")
		}
		if !e2eAnyTLSIsReject(err) {
			return fmt.Errorf("AnyTLS auth rejection had unexpected class")
		}
		return fmt.Errorf("%w", e2eAnyTLSRejected)
	}
	settings := []byte(fmt.Sprintf("v=2\nclient=netivri-test\npadding-md5=%x", md5.Sum(padding.DefaultPaddingScheme)))
	if err = e2eAnyTLSWriteFrame(tlsConn, 4, 0, settings); err != nil {
		return fmt.Errorf("AnyTLS settings write failed")
	}
	if err = e2eAnyTLSWriteFrame(tlsConn, 1, 1, nil); err != nil {
		return fmt.Errorf("AnyTLS stream open failed")
	}
	var dest bytes.Buffer
	host, pt, err := net.SplitHostPort(backend)
	if err != nil {
		return fmt.Errorf("echo endpoint invalid")
	}
	p, err := strconv.Atoi(pt)
	if err != nil {
		return fmt.Errorf("echo port invalid")
	}
	if err = M.SocksaddrSerializer.WriteAddrPort(&dest, M.ParseSocksaddrHostPort(host, uint16(p))); err != nil {
		return fmt.Errorf("AnyTLS destination encode failed")
	}
	if err = e2eAnyTLSWriteFrame(tlsConn, 2, 1, dest.Bytes()); err != nil {
		return fmt.Errorf("AnyTLS destination write failed")
	}
	seenSynAck, seenFin := false, false
	for !seenFin {
		var h [7]byte
		if _, err = io.ReadFull(tlsConn, h[:]); err != nil {
			if e2eAnyTLSIsTimeout(err) {
				return fmt.Errorf("AnyTLS policy response timed out")
			}
			if e2eAnyTLSIsReject(err) {
				return fmt.Errorf("policy probe closed before SYNACK and FIN")
			}
			return fmt.Errorf("policy response read failed")
		}
		cmd, sid, nbytes := h[0], binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint16(h[5:])
		if nbytes > 4096 {
			return fmt.Errorf("AnyTLS response frame oversized")
		}
		payload := make([]byte, int(nbytes))
		if _, err = io.ReadFull(tlsConn, payload); err != nil {
			return fmt.Errorf("AnyTLS response frame incomplete")
		}
		if sid != 1 {
			continue
		}
		switch cmd {
		case 7:
			seenSynAck = true
		case 2:
			return fmt.Errorf("policy probe reached a routed stream")
		case 3:
			if !seenSynAck {
				return fmt.Errorf("policy FIN arrived before SYNACK")
			}
			seenFin = true
		}
	}
	if !seenSynAck {
		return fmt.Errorf("policy response missed SYNACK")
	}
	return fmt.Errorf("%w", e2eAnyTLSRejected)
}

func e2eAnyTLSWriteFrame(w io.Writer, cmd byte, sid uint32, payload []byte) error {
	if len(payload) > 65535 {
		return fmt.Errorf("frame too large")
	}
	h := []byte{cmd, byte(sid >> 24), byte(sid >> 16), byte(sid >> 8), byte(sid), byte(len(payload) >> 8), byte(len(payload))}
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

func e2eAnyTLSDiagnostics(t *testing.T, homeA, homeB string, secrets ...string) string {
	t.Helper()
	var b strings.Builder
	for _, item := range []struct{ label, home string }{{"A", homeA}, {"B", homeB}} {
		statusArgs := []string{"status"}
		if item.label == "B" {
			statusArgs = append(statusArgs, "--full")
		}
		code, out, stderr := accountsRun(t, item.home, statusArgs...)
		if code != 0 {
			fmt.Fprintf(&b, " %s-status=%s", item.label, serverErrorSummary(out, stderr))
		} else {
			assertNoSecrets(t, out, stderr, secrets...)
			var env struct {
				Data struct {
					Servers []struct {
						Name, Protocol, Listen string
						Connections            int
						Dropped                uint64 `json:"dropped_clients"`
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
		code, out, stderr = accountsRun(t, item.home, "logs", "--since", "2m", "--tail", "8")
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
		code, out, stderr := accountsRun(t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name, Protocol, Listen string
						Accounts               []string
						Connections            int
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
			serverE2EFailureDiagnostics(t, home, "AnyTLS status wait expired")
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
		code, out, stderr := accountsRun(t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name    string
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
			serverE2EFailureDiagnostics(t, home, "AnyTLS dropped status wait expired")
			t.Fatal("AnyTLS dropped-client counter did not increment")
		case <-ticker.C:
		}
	}
}
