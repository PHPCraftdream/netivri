//go:build integration

package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	auth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

const testTimeout = 10 * time.Second

// startInstance starts one core instance on 127.0.0.1:0 with a temp home and
// registers its Close as cleanup. Mihomo holds process-global state, so tests
// must never run in parallel (no t.Parallel anywhere in this file).
func startInstance(t *testing.T, authenticator auth.Authenticator) *Instance {
	t.Helper()
	inst, err := Start(context.Background(), Options{
		HomeDir:       testutil.TempHome(t),
		Authenticator: authenticator,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// echoServer starts a TCP echo backend on 127.0.0.1:0 and returns its address.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(c)
		}
	}()
	return l.Addr().String()
}

func dialProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, testTimeout)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	return c
}

func readFull(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func echoRoundTrip(t *testing.T, w io.Writer, r io.Reader) {
	t.Helper()
	payload := []byte("netivri-echo-payload")
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	got := readFull(t, r, len(payload))
	if string(got) != string(payload) {
		t.Fatalf("echo round-trip: got %q, want %q", got, payload)
	}
}

// socks5SendAuth performs the RFC 1929 username/password subnegotiation and
// returns the 2-byte auth reply.
func socks5SendAuth(t *testing.T, c net.Conn, user, pass string) []byte {
	t.Helper()
	buf := []byte{1, byte(len(user))}
	buf = append(buf, user...)
	buf = append(buf, byte(len(pass)))
	buf = append(buf, pass...)
	if _, err := c.Write(buf); err != nil {
		t.Fatalf("socks5 auth write: %v", err)
	}
	return readFull(t, c, 2)
}

func socks5ConnectRequest(t *testing.T, c net.Conn, target string) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	ip := net.ParseIP(host)
	req := []byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}
	if ip4 := ip.To4(); ip4 != nil {
		copy(req[4:8], ip4)
	} else {
		t.Fatalf("target %q is not an IPv4 literal", target)
	}
	req[8], req[9] = byte(port>>8), byte(port)
	if _, err := c.Write(req); err != nil {
		t.Fatalf("socks5 request write: %v", err)
	}
	reply := readFull(t, c, 4)
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("socks5 connect reply = %v, want rep 0x00", reply)
	}
	switch reply[3] {
	case 1:
		readFull(t, c, 4+2)
	case 3:
		n := readFull(t, c, 1)[0]
		readFull(t, c, int(n)+2)
	case 4:
		readFull(t, c, 16+2)
	default:
		t.Fatalf("socks5 connect reply atyp = %#x", reply[3])
	}
}

// socks5ConnectNoAuth runs a full no-auth SOCKS5 CONNECT to target and
// returns the established tunnel.
func socks5ConnectNoAuth(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	c := dialProxy(t, proxyAddr)
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	greeting := readFull(t, c, 2)
	if greeting[0] != 5 || greeting[1] != 0 {
		t.Fatalf("greeting reply = %v, want {5, 0}", greeting)
	}
	socks5ConnectRequest(t, c, target)
	return c
}

// socks4Connect sends a SOCKS4 request. If targetHost is non-empty it sends a
// SOCKS4a request (IP bytes 0.0.0.1 + hostname). It returns the conn and the
// reply code byte.
func socks4Connect(t *testing.T, proxyAddr, targetIP, targetHost string, port int, user string) (net.Conn, byte) {
	t.Helper()
	c := dialProxy(t, proxyAddr)
	req := []byte{4, 1, byte(port >> 8), byte(port), 0, 0, 0, 0}
	if targetHost != "" {
		req[4], req[5], req[6], req[7] = 0, 0, 0, 1
	} else {
		ip := net.ParseIP(targetIP).To4()
		if ip == nil {
			t.Fatalf("target %q is not an IPv4 literal", targetIP)
		}
		copy(req[4:8], ip)
	}
	req = append(req, []byte(user)...)
	req = append(req, 0)
	if targetHost != "" {
		req = append(req, []byte(targetHost)...)
		req = append(req, 0)
	}
	if _, err := c.Write(req); err != nil {
		t.Fatalf("socks4 write: %v", err)
	}
	reply := readFull(t, c, 8)
	return c, reply[1]
}

// httpConnect sends CONNECT to the proxy and returns the tunneled conn and a
// buffered reader holding any bytes after the response headers.
func httpConnect(t *testing.T, proxyAddr, target, basicCred string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c := dialProxy(t, proxyAddr)
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if basicCred != "" {
		req += "Proxy-Authorization: Basic " + basicCred + "\r\n"
	}
	req += "\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatalf("CONNECT write: %v", err)
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("CONNECT response: %v", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("CONNECT status = %q, want HTTP/1.1 200", strings.TrimRight(line, "\r\n"))
	}
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("CONNECT headers: %v", err)
		}
		if h == "\r\n" {
			break
		}
	}
	return c, br
}

func proxyHTTPClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy url %q: %v", proxyURL, err)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: testTimeout, Transport: tr}
}

// assertPortClosed expects the listener to be gone. Callers wait for Close to
// return (or for inst.done) first, so a single dial is enough, no polling.
func assertPortClosed(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, testTimeout)
	if err == nil {
		_ = c.Close()
		t.Fatalf("port %s still accepts connections after Close", addr)
	}
}

func TestMixedPortSocks5ConnectNoAuth(t *testing.T) {
	inst := startInstance(t, nil)
	backend := echoServer(t)
	c := socks5ConnectNoAuth(t, inst.Addr(), backend)
	defer c.Close()
	echoRoundTrip(t, c, c)
}

func TestMixedPortHTTPConnectSamePort(t *testing.T) {
	inst := startInstance(t, nil)
	backend := echoServer(t)
	c, br := httpConnect(t, inst.Addr(), backend, "")
	defer c.Close()
	echoRoundTrip(t, c, br)
}

// One running instance serves SOCKS5, HTTP CONNECT and plain HTTP on the same
// port, the protocol being sniffed from the first byte.
func TestMixedPortAllProtocolsOneInstance(t *testing.T) {
	inst := startInstance(t, nil)
	backend := echoServer(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "one-port-body")
	}))
	t.Cleanup(ts.Close)

	c1 := socks5ConnectNoAuth(t, inst.Addr(), backend)
	echoRoundTrip(t, c1, c1)

	c2, br := httpConnect(t, inst.Addr(), backend, "")
	echoRoundTrip(t, c2, br)

	resp, err := proxyHTTPClient(t, "http://"+inst.Addr()).Get(ts.URL + "/x")
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "one-port-body" {
		t.Fatalf("body = %q, err = %v, want one-port-body", body, err)
	}

	// The first tunnel is still alive after the other protocols were served.
	echoRoundTrip(t, c1, c1)
}

func TestMixedPortPlainHTTPGet(t *testing.T) {
	inst := startInstance(t, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "netivri-body")
	}))
	t.Cleanup(ts.Close)

	client := proxyHTTPClient(t, "http://"+inst.Addr())
	resp, err := client.Get(ts.URL + "/hello")
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "netivri-body" {
		t.Fatalf("body = %q, want %q", body, "netivri-body")
	}
}

func TestMixedPortSocks4(t *testing.T) {
	inst := startInstance(t, nil)
	backend := echoServer(t)
	_, portStr, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", backend, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	ip := strings.Split(backend, ":")[0]

	t.Run("socks4-ip-literal", func(t *testing.T) {
		c, code := socks4Connect(t, inst.Addr(), ip, "", port, "tester")
		defer c.Close()
		if code != 90 {
			t.Fatalf("socks4 reply code = %d, want 90 (granted)", code)
		}
		echoRoundTrip(t, c, c)
	})

	// SOCKS4a: the 0.0.0.1 marker plus a host string. The host is an IP
	// literal, so no resolver is involved (hermetic, no ::1 vs 127.0.0.1
	// ambiguity), but the 4a parsing path is still exercised.
	t.Run("socks4a-hostname", func(t *testing.T) {
		c, code := socks4Connect(t, inst.Addr(), "", ip, port, "tester")
		defer c.Close()
		if code != 90 {
			t.Fatalf("socks4a reply code = %d, want 90 (granted)", code)
		}
		echoRoundTrip(t, c, c)
	})
}

func TestMixedPortAuth(t *testing.T) {
	const (
		user = "niv"
		pass = "s3cret"
	)
	authn := auth.NewAuthenticator([]auth.AuthUser{{User: user, Pass: pass}})
	inst := startInstance(t, authn)
	addr := inst.Addr()
	backend := echoServer(t)
	_, portStr, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", backend, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	ip := strings.Split(backend, ":")[0]

	t.Run("socks5-username-password-ok", func(t *testing.T) {
		c := dialProxy(t, addr)
		defer c.Close()
		if _, err := c.Write([]byte{5, 1, 2}); err != nil {
			t.Fatalf("greeting write: %v", err)
		}
		greeting := readFull(t, c, 2)
		if greeting[0] != 5 || greeting[1] != 2 {
			t.Fatalf("greeting reply = %v, want {5, 2}", greeting)
		}
		authReply := socks5SendAuth(t, c, user, pass)
		if authReply[0] != 1 || authReply[1] != 0 {
			t.Fatalf("auth reply = %v, want {1, 0}", authReply)
		}
		socks5ConnectRequest(t, c, backend)
		echoRoundTrip(t, c, c)
	})

	t.Run("socks5-wrong-password", func(t *testing.T) {
		c := dialProxy(t, addr)
		defer c.Close()
		if _, err := c.Write([]byte{5, 1, 2}); err != nil {
			t.Fatalf("greeting write: %v", err)
		}
		greeting := readFull(t, c, 2)
		if greeting[0] != 5 || greeting[1] != 2 {
			t.Fatalf("greeting reply = %v, want {5, 2}", greeting)
		}
		authReply := socks5SendAuth(t, c, user, "wrong-pass")
		if authReply[0] != 1 || authReply[1] != 1 {
			t.Fatalf("auth reply = %v, want {1, 1} (rejected)", authReply)
		}
	})

	// Offering only no-auth while the server requires user/pass: Mihomo
	// answers method 0x02 (not offered); the client cannot proceed, so this
	// is a protocol mismatch, and the server never reaches a CONNECT.
	t.Run("socks5-noauth-offer-refused", func(t *testing.T) {
		c := dialProxy(t, addr)
		defer c.Close()
		if _, err := c.Write([]byte{5, 1, 0}); err != nil {
			t.Fatalf("greeting write: %v", err)
		}
		greeting := readFull(t, c, 2)
		if greeting[0] != 5 || greeting[1] != 2 {
			t.Fatalf("greeting reply = %v, want {5, 2} (server demands auth)", greeting)
		}
	})

	t.Run("http-correct-credentials", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "authed-body")
		}))
		t.Cleanup(ts.Close)
		client := proxyHTTPClient(t, "http://"+user+":"+pass+"@"+addr)
		resp, err := client.Get(ts.URL + "/secure")
		if err != nil {
			t.Fatalf("GET via proxy with credentials: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "authed-body" {
			t.Fatalf("body = %q, want %q", body, "authed-body")
		}
	})

	t.Run("http-missing-credentials-407", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(ts.Close)
		client := proxyHTTPClient(t, "http://"+addr)
		resp, err := client.Get(ts.URL + "/secure")
		if err != nil {
			t.Fatalf("GET via proxy without credentials: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("status = %d, want 407", resp.StatusCode)
		}
		if got := resp.Header.Get("Proxy-Authenticate"); got != "Basic" {
			t.Fatalf("Proxy-Authenticate = %q, want Basic", got)
		}
	})

	// Mihomo answers a present-but-wrong credential with 403, not 407
	// (listener/http/proxy.go: only an empty credential yields 407).
	t.Run("http-wrong-credentials-403", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(ts.Close)
		client := proxyHTTPClient(t, "http://"+user+":not-the-password@"+addr)
		resp, err := client.Get(ts.URL + "/secure")
		if err != nil {
			t.Fatalf("GET via proxy with wrong credentials: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	// SOCKS4 carries no password; netivri treats an empty password as
	// invalid, so Mihomo grants with code 93 (identd mismatch) and closes.
	t.Run("socks4-rejected-when-auth-enabled", func(t *testing.T) {
		c, code := socks4Connect(t, addr, ip, "", port, "tester")
		defer c.Close()
		if code != 93 {
			t.Fatalf("socks4 reply code = %d, want 93 (identd mismatch / rejected)", code)
		}
	})
}

func TestMixedPortLifecycle(t *testing.T) {
	home := testutil.TempHome(t)

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := Start(ctx, Options{HomeDir: home})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { cancel(); _ = inst.Close() })
	addr := inst.Addr()

	// Second Start while one runs fails before touching the network.
	if _, err := Start(context.Background(), Options{HomeDir: home}); err == nil {
		t.Fatal("second Start while running: got nil error, want failure")
	}

	// Cancelling the Start context stops the instance; done is closed after
	// the listener is closed and the claim is released.
	cancel()
	select {
	case <-inst.done:
	case <-time.After(testTimeout):
		t.Fatal("instance did not stop within the timeout after ctx cancel")
	}
	assertPortClosed(t, addr)

	// A port held by another listener: PORT_IN_USE / ExitConflict, and the
	// failed Start must not keep the claim.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	heldPort := held.Addr().(*net.TCPAddr).Port
	_, err = Start(context.Background(), Options{HomeDir: home, Port: heldPort})
	var cliErr *cli.Error
	if !errors.As(err, &cliErr) {
		t.Fatalf("Start on held port: err = %v (%T), want *cli.Error", err, err)
	}
	if cliErr.Code != "PORT_IN_USE" || cliErr.Exit != cli.ExitConflict {
		t.Fatalf("Start on held port: code = %q, exit = %d, want PORT_IN_USE/%d", cliErr.Code, cliErr.Exit, cli.ExitConflict)
	}

	// After the failure a new Start on a free port succeeds.
	inst2, err := Start(context.Background(), Options{HomeDir: home})
	if err != nil {
		t.Fatalf("restart after failed Start: %v", err)
	}
	t.Cleanup(func() { _ = inst2.Close() })
	addr2 := inst2.Addr()
	if addr2 == "" {
		t.Fatal("restarted instance has empty address")
	}
	c, err := net.DialTimeout("tcp", addr2, testTimeout)
	if err != nil {
		t.Fatalf("dial restarted instance: %v", err)
	}
	_ = c.Close()

	// Close is idempotent.
	if err := inst2.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := inst2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	assertPortClosed(t, addr2)
}

func TestMixedPortRobustness(t *testing.T) {
	inst := startInstance(t, nil)
	addr := inst.Addr()
	backend := echoServer(t)

	// Connect and immediately close: the server must survive.
	c0, err := net.DialTimeout("tcp", addr, testTimeout)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c0.Close()

	// Garbage first bytes: the server must answer or close, never hang.
	garbage := [][]byte{
		{0x00, 0x01, 0x02, 0x03, 0x0d, 0x0a, 0x0d, 0x0a},
		[]byte("this is not a request\r\n\r\n"),
	}
	for i, g := range garbage {
		c := dialProxy(t, addr)
		if _, err := c.Write(g); err != nil {
			t.Fatalf("garbage[%d] write: %v", i, err)
		}
		buf := make([]byte, 64)
		_ = c.SetReadDeadline(time.Now().Add(testTimeout))
		if _, err := c.Read(buf); errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("garbage[%d]: server neither answered nor closed within %v", i, testTimeout)
		}
		// EOF, reset or a reply are all acceptable; the server must not hang.
		_ = c.Close()
	}

	// A normal request still works afterwards.
	c := socks5ConnectNoAuth(t, addr, backend)
	defer c.Close()
	echoRoundTrip(t, c, c)
}
