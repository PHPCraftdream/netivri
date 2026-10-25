//go:build integration

package core

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/inbound"
	mihauth "github.com/metacubex/mihomo/component/auth"
	authStore "github.com/metacubex/mihomo/listener/auth"
	mihhttp "github.com/metacubex/mihomo/listener/http"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// startInstanceAllowed starts one core instance with an allowed-clients rule
// on 127.0.0.1:0. Like startInstance, it must never run in parallel.
func startInstanceAllowed(t *testing.T, clients []netip.Prefix, authenticator mihauth.Authenticator) *Instance {
	t.Helper()
	inst, err := Start(context.Background(), Options{
		HomeDir:        testutil.TempHome(t),
		Authenticator:  authenticator,
		AllowedClients: clients,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// fakeRemoteAuth verifies only through VerifyFrom: Verify always fails, so a
// successful authenticated connect proves the live path called VerifyFrom.
type fakeRemoteAuth struct{}

var (
	_ mihauth.Authenticator  = fakeRemoteAuth{}
	_ mihauth.RemoteVerifier = fakeRemoteAuth{}
)

func (fakeRemoteAuth) Verify(user, pass string) bool { return false }

func (fakeRemoteAuth) VerifyFrom(user, pass string, remote net.Addr) bool {
	if pass != "pw" {
		return false
	}
	ta, ok := remote.(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return false
	}
	if user == "alice" {
		// alice is refused from loopback only.
		return !ta.IP.IsLoopback()
	}
	return user == "bob"
}

func (fakeRemoteAuth) Users() []string { return []string{"alice", "bob"} }

// socks5Greeting writes the method-selection greeting and reads the reply,
// returning the chosen method byte.
func socks5Greeting(t *testing.T, c net.Conn, method byte) byte {
	t.Helper()
	if _, err := c.Write([]byte{5, 1, method}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	reply := readFull(t, c, 2)
	if reply[0] != 5 {
		t.Fatalf("greeting reply = %v, want ver 0x05", reply)
	}
	return reply[1]
}

func TestAllowedClientsDefaultAllowsAll(t *testing.T) {
	inst := startInstanceAllowed(t, nil, nil)
	c := dialProxy(t, inst.Addr())
	defer c.Close()
	if m := socks5Greeting(t, c, 0); m != 0 {
		t.Fatalf("method = %#x, want 0x00 (allow all)", m)
	}
}

func TestAllowedClientsLoopbackAccepted(t *testing.T) {
	clients := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	inst := startInstanceAllowed(t, clients, nil)
	c := dialProxy(t, inst.Addr())
	defer c.Close()
	if m := socks5Greeting(t, c, 0); m != 0 {
		t.Fatalf("method = %#x, want 0x00 (loopback accepted)", m)
	}
}

func TestAllowedClientsForeignNetRefused(t *testing.T) {
	clients := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	inst := startInstanceAllowed(t, clients, nil)
	c := dialProxy(t, inst.Addr())
	defer c.Close()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	buf := make([]byte, 2)
	n, err := c.Read(buf)
	if n != 0 || err == nil {
		t.Fatalf("read = (%d, %v), want (0, error)", n, err)
	}
	// The connection must be closed promptly, not hang until the deadline.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connection was not closed promptly: %v", err)
	}
	if !errors.Is(err, io.EOF) && !strings.Contains(strings.ToLower(err.Error()), "reset") {
		t.Fatalf("read error = %v, want EOF or connection reset", err)
	}
}

func TestAllowedClientsVerifyFromOnLivePath(t *testing.T) {
	inst := startInstanceAllowed(t, nil, fakeRemoteAuth{})
	backend := echoServer(t)
	addr := inst.Addr()

	t.Run("refused-from-loopback-via-verifyfrom", func(t *testing.T) {
		c := dialProxy(t, addr)
		defer c.Close()
		if m := socks5Greeting(t, c, 2); m != 2 {
			t.Fatalf("method = %#x, want 0x02", m)
		}
		authReply := socks5SendAuth(t, c, "alice", "pw")
		if authReply[0] != 1 || authReply[1] != 1 {
			t.Fatalf("auth reply = %v, want {1, 1} (VerifyFrom refused loopback)", authReply)
		}
	})

	t.Run("accepted-via-verifyfrom", func(t *testing.T) {
		c := dialProxy(t, addr)
		defer c.Close()
		if m := socks5Greeting(t, c, 2); m != 2 {
			t.Fatalf("method = %#x, want 0x02", m)
		}
		authReply := socks5SendAuth(t, c, "bob", "pw")
		if authReply[0] != 1 || authReply[1] != 0 {
			t.Fatalf("auth reply = %v, want {1, 0} (VerifyFrom accepted)", authReply)
		}
		socks5ConnectRequest(t, c, backend)
		echoRoundTrip(t, c, c)
	})
}

// mustMulti builds a real multi authenticator for the integration tests.
func mustMulti(t *testing.T, specs []auth.AccountSpec, persist func(string, string) error) mihauth.Authenticator {
	t.Helper()
	a, err := auth.NewMulti(auth.MultiOptions{Accounts: specs, PersistAccount: persist})
	if err != nil {
		t.Fatalf("NewMulti: %v", err)
	}
	return a
}

// TestAllowedClientsAccountRuleBeforeInitClaim drives ONE real multi
// authenticator (whose state is preserved throughout) over two live
// instances: first the per-instance GLOBAL allowed-clients rule
// (core.Options.AllowedClients) refuses the loopback client in the accept
// loop, then a global rule of nil (allow all) lets the same client claim the
// still-unclaimed init account. The account's own rule is fixed at loopback
// and does NOT change between instances — what switches is the global rule.
// (The account-rule analogue — a refused peer first, an allowed claim later,
// persist 0 then 1, on one authenticator — is covered by
// TestVerifyFromAccountRule in internal/auth/multi_remote_test.go.)
// Every refusal below uses a NON-EMPTY password from a loopback peer, so the
// transport cannot reject it (transport/socks5 rejects a zero-length
// password before VerifyWithRemote is ever called).
func TestAllowedClientsAccountRuleBeforeInitClaim(t *testing.T) {
	loopback, err := config.ParseAllowedClients("loopback")
	if err != nil {
		t.Fatalf("ParseAllowedClients: %v", err)
	}
	var persistCalls atomic.Int32
	persist := func(string, string) error {
		persistCalls.Add(1)
		return nil
	}
	// One authenticator for the whole test: whatever the refused attempt
	// does (or does not do) to the account state is observed on the SAME
	// object in the second instance, not on a fresh copy.
	a := mustMulti(t, []auth.AccountSpec{{
		Login:          "alice",
		Password:       auth.InitPlaceholder,
		AllowedClients: loopback,
	}}, persist)

	// Instance 1: the GLOBAL rule 10.0.0.0/8 excludes the loopback client,
	// so the connection is closed in the accept loop before any handshake:
	// the greeting write succeeds but no reply ever arrives.
	inst := startInstanceAllowed(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, a)
	c := dialProxy(t, inst.Addr())
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	buf := make([]byte, 2)
	n, rerr := c.Read(buf)
	if n != 0 || rerr == nil {
		t.Fatalf("read = (%d, %v), want (0, error)", n, rerr)
	}
	// The connection must be closed promptly, not hang until the deadline.
	var ne net.Error
	if errors.As(rerr, &ne) && ne.Timeout() {
		t.Fatalf("connection was not closed promptly: %v", rerr)
	}
	if !errors.Is(rerr, io.EOF) && !strings.Contains(strings.ToLower(rerr.Error()), "reset") {
		t.Fatalf("read error = %v, want EOF or connection reset", rerr)
	}
	if persistCalls.Load() != 0 {
		t.Fatalf("persist calls = %d, want 0 (connection refused before any auth)", persistCalls.Load())
	}
	_ = c.Close()
	// Mihomo is process-global: close and wait before starting the next instance.
	if err := inst.Close(); err != nil {
		t.Fatalf("close first instance: %v", err)
	}

	// Instance 2: the SAME authenticator object, global rule nil (allow
	// all). This proves the refused connections above consumed or corrupted
	// nothing: the unclaimed init placeholder is intact and the loopback
	// peer (allowed by the account's own loopback rule) claims it exactly
	// once — persist fires only after this allowed attempt (0 then 1).
	inst2 := startInstanceAllowed(t, nil, a)
	backend := echoServer(t)
	c2 := dialProxy(t, inst2.Addr())
	if m := socks5Greeting(t, c2, 2); m != 2 {
		t.Fatalf("method = %#x, want 0x02", m)
	}
	if reply := socks5SendAuth(t, c2, "alice", "some-valid-looking-pass"); reply[1] != 0 {
		t.Fatalf("auth status = %#x, want 0x00 (allowed peer claims init)", reply[1])
	}
	socks5ConnectRequest(t, c2, backend)
	echoRoundTrip(t, c2, c2)
	if persistCalls.Load() != 1 {
		t.Fatalf("persist calls = %d, want 1 (claimed exactly once)", persistCalls.Load())
	}
}

// TestHTTPKeepAliveRevokesTrust drives the vendored HTTP listener directly:
// after alice authenticates on a keep-alive connection, a second request
// naming bob (whose rule rejects this client) must be answered 403 and never
// routed, even though the connection is already trusted from request one; a
// third request with a wrong password gets the same 403, so the refusal is
// indistinguishable from a wrong-password failure.
func TestHTTPKeepAliveRevokesTrust(t *testing.T) {
	loop, err := config.ParseAllowedClients("loopback")
	if err != nil {
		t.Fatalf("ParseAllowedClients: %v", err)
	}
	rejected, err := config.ParseAllowedClients("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseAllowedClients: %v", err)
	}
	hashAlice, err := auth.Hash("pw-alice")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	hashBob, err := auth.Hash("pw-bob")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	a := mustMulti(t, []auth.AccountSpec{
		{Login: "alice", Password: hashAlice, AllowedClients: loop},
		{Login: "bob", Password: hashBob, AllowedClients: rejected},
	}, func(string, string) error { return nil })

	// A running instance applies the direct-mode tunnel configuration that
	// HandleConn's routing needs; the listener under test is our own.
	startInstanceAllowed(t, nil, a)

	// A plain HTTP backend so a routed request gets a real 200 response.
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("backend-ok"))
	})}
	bln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	go func() { _ = backend.Serve(bln) }()
	t.Cleanup(func() { _ = backend.Close() })
	backendAddr := bln.Addr().String()

	store := authStore.NewAuthStore(a)
	pln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = pln.Close() })
	go func() {
		for {
			conn, err := pln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				// tunnel.Tunnel is a struct value (the same value core.Start
				// wraps); a copy implements C.Tunnel.
				tt := tunnel.Tunnel
				mihhttp.HandleConn(c, tt, store,
					inbound.WithInName("TEST"), inbound.WithSpecialRules(""))
				_ = c.Close()
			}(conn)
		}
	}()

	conn, err := net.Dial("tcp", pln.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	br := bufio.NewReader(conn)

	cred := base64.StdEncoding.EncodeToString([]byte("alice:pw-alice"))
	req := "GET http://" + backendAddr + "/ HTTP/1.1\r\n" +
		"Host: " + backendAddr + "\r\n" +
		"Proxy-Connection: keep-alive\r\n" +
		"Proxy-Authorization: Basic " + cred + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("first request write: %v", err)
	}
	// readHTTPResponse is still fine for the first 200; use http.ReadResponse
	// for the two 403s so headers can be compared too.
	resp1, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("first response: %v", err)
	}
	defer func() { _ = resp1.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp1.Body); err != nil {
		t.Fatalf("first body: %v", err)
	}
	if resp1.StatusCode != 200 {
		t.Fatalf("first response status = %d, want 200", resp1.StatusCode)
	}

	// Second request on the SAME connection: bob's rule rejects loopback.
	cred = base64.StdEncoding.EncodeToString([]byte("bob:pw-bob"))
	req = "GET http://" + backendAddr + "/ HTTP/1.1\r\n" +
		"Host: " + backendAddr + "\r\n" +
		"Proxy-Connection: keep-alive\r\n" +
		"Proxy-Authorization: Basic " + cred + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("second request write: %v", err)
	}
	resp2, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("second response: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp2.Body); err != nil {
		t.Fatalf("second body: %v", err)
	}
	if resp2.StatusCode != 403 {
		t.Fatalf("second response status = %d, want 403 (trust revoked)", resp2.StatusCode)
	}

	// Third request on the SAME connection: alice with a WRONG password.
	// The status must also be 403 — identical to the account-rule refusal
	// above — proving a client cannot tell a disallowed address from a
	// wrong password when credentials were sent (407 is only for missing
	// credentials).
	cred = base64.StdEncoding.EncodeToString([]byte("alice:not-her-pass"))
	req = "GET http://" + backendAddr + "/ HTTP/1.1\r\n" +
		"Host: " + backendAddr + "\r\n" +
		"Proxy-Connection: keep-alive\r\n" +
		"Proxy-Authorization: Basic " + cred + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("third request write: %v", err)
	}
	resp3, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("third response: %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp3.Body); err != nil {
		t.Fatalf("third body: %v", err)
	}
	if resp3.StatusCode != 403 {
		t.Fatalf("third response status = %d, want 403 (indistinguishable from wrong password)", resp3.StatusCode)
	}
	// The two refusals must carry identical headers, so a client cannot
	// tell an account-rule refusal from a wrong-password refusal. "Date" is
	// ignored because it is the wall-clock time of each response, and
	// "Content-Length" because it may legitimately differ between responses.
	h2, h3 := resp2.Header.Clone(), resp3.Header.Clone()
	h2.Del("Date")
	h2.Del("Content-Length")
	h3.Del("Date")
	h3.Del("Content-Length")
	if !reflect.DeepEqual(h2, h3) {
		t.Fatalf("403 headers differ:\nsecond: %v\nthird:  %v", h2, h3)
	}
}
