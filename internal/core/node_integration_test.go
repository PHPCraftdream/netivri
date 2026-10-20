//go:build integration

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// fakeUpstream is a minimal no-auth SOCKS5 proxy used as the routed node.
// It records every CONNECT destination exactly as the client sent it
// (IP literal or domain name), maps recorded domains to real addresses,
// dials and pipes both ways.
type fakeUpstream struct {
	mu      sync.Mutex
	targets []string
	mapping map[string]string // domain -> real address
}

func (f *fakeUpstream) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.targets))
	copy(out, f.targets)
	return out
}

// mapDomain registers a domain -> address mapping used by the fake upstream
// when a client sends a domain-name CONNECT.
func (f *fakeUpstream) mapDomain(domain, addr string) {
	f.mu.Lock()
	f.mapping[domain] = addr
	f.mu.Unlock()
}

func (f *fakeUpstream) record(target string) {
	f.mu.Lock()
	f.targets = append(f.targets, target)
	f.mu.Unlock()
}

// startFakeUpstream runs the fake on 127.0.0.1:0 and returns it with its
// address. All connections get deadlines and are closed when done.
func startFakeUpstream(t *testing.T) (*fakeUpstream, string) {
	t.Helper()
	f := &fakeUpstream{mapping: map[string]string{}}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake upstream listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, l.Addr().String()
}

// readN reads exactly n bytes; on any error the caller closes the conn.
func readN(c net.Conn, n int) ([]byte, bool) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, false
	}
	return buf, true
}

func (f *fakeUpstream) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(testTimeout))

	head, ok := readN(c, 2)
	if !ok || head[0] != 5 {
		return
	}
	if _, ok := readN(c, int(head[1])); !ok {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}

	req, ok := readN(c, 4)
	if !ok {
		return
	}
	if req[1] != 1 { // CONNECT only
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 1:
		ip, ok := readN(c, 4)
		if !ok {
			return
		}
		host = net.IP(ip).String()
	case 3:
		ln, ok := readN(c, 1)
		if !ok {
			return
		}
		d, ok := readN(c, int(ln[0]))
		if !ok {
			return
		}
		host = string(d)
	case 4:
		ip, ok := readN(c, 16)
		if !ok {
			return
		}
		host = net.IP(ip).String()
	default:
		_, _ = c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	port, ok := readN(c, 2)
	if !ok {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1])))
	f.record(dest)

	dialAddr := dest
	if req[3] == 3 {
		f.mu.Lock()
		mapped, ok := f.mapping[host]
		f.mu.Unlock()
		if !ok {
			// Never resolve or dial an unmapped name: stay on loopback.
			_, _ = c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		dialAddr = mapped
	}
	up, err := net.DialTimeout("tcp", dialAddr, testTimeout)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(testTimeout))
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	// One direction ended: close both sockets so the other one unblocks.
	_ = c.Close()
	_ = up.Close()
	<-done
}

// startNodeInstance starts one instance routing through the given node.
func startNodeInstance(t *testing.T, authenticator auth.Authenticator, node map[string]any) *Instance {
	t.Helper()
	inst, err := Start(context.Background(), Options{
		HomeDir:       testutil.TempHome(t),
		Authenticator: authenticator,
		Node:          node,
	})
	if err != nil {
		t.Fatalf("Start with node: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if got := inst.Route(); got != "node" {
		t.Fatalf("Route() = %q, want node", got)
	}
	return inst
}

// nodeMap builds a socks5 node mapping for the fake upstream.
func nodeMap(addr string) map[string]any {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		panic("nodeMap: " + err.Error())
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic("nodeMap: " + err.Error())
	}
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": port}
}

// countEchoServer is an echo server that counts accepted connections.
func countEchoServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var count atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(testTimeout))
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return l.Addr().String(), &count
}

// socks5ConnectDomain runs a no-auth SOCKS5 CONNECT with an ATYP=3 domain
// target and returns the established tunnel.
func socks5ConnectDomain(t *testing.T, proxyAddr, domain string, port int) net.Conn {
	t.Helper()
	c := dialProxy(t, proxyAddr)
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	greeting := readFull(t, c, 2)
	if greeting[0] != 5 || greeting[1] != 0 {
		t.Fatalf("greeting reply = %v, want {5, 0}", greeting)
	}
	req := []byte{5, 1, 0, 3, byte(len(domain))}
	req = append(req, domain...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("socks5 request write: %v", err)
	}
	reply := readFull(t, c, 4)
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("socks5 connect reply = %v, want rep 0x00", reply)
	}
	if reply[3] != 1 {
		t.Fatalf("socks5 connect reply atyp = %#x, want 1 (IPv4)", reply[3])
	}
	readFull(t, c, 4+2)
	return c
}

// assertUpstreamSaw asserts the fake upstream recorded exactly the given
// CONNECT destinations.
func assertUpstreamSaw(t *testing.T, f *fakeUpstream, want []string) {
	t.Helper()
	got := f.recorded()
	if len(got) != len(want) {
		t.Fatalf("upstream saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("upstream saw %v, want %v", got, want)
		}
	}
}

// TestNodeSocks5RoundTrip: client -> netivri -> fake upstream -> echo.
func TestNodeSocks5RoundTrip(t *testing.T) {
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, nil, nodeMap(nodeAddr))
	backend := echoServer(t)

	c := socks5ConnectNoAuth(t, inst.Addr(), backend)
	defer c.Close()
	echoRoundTrip(t, c, c)
	assertUpstreamSaw(t, f, []string{backend})
}

// TestNodeHTTPConnectRoundTrip: an HTTP CONNECT client uses the same path.
func TestNodeHTTPConnectRoundTrip(t *testing.T) {
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, nil, nodeMap(nodeAddr))
	backend := echoServer(t)

	c, br := httpConnect(t, inst.Addr(), backend, "")
	defer c.Close()
	echoRoundTrip(t, c, br)
	assertUpstreamSaw(t, f, []string{backend})
}

// TestNodePlainHTTPGet: a plain HTTP GET via the proxy also goes through
// the upstream.
func TestNodePlainHTTPGet(t *testing.T) {
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, nil, nodeMap(nodeAddr))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("via-node-body"))
	}))
	t.Cleanup(ts.Close)

	client := proxyHTTPClient(t, "http://"+inst.Addr())
	resp, err := client.Get(ts.URL + "/via")
	if err != nil {
		t.Fatalf("GET via node: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "via-node-body" {
		t.Fatalf("body = %q, want %q", body, "via-node-body")
	}
	assertUpstreamSaw(t, f, []string{ts.Listener.Addr().String()})
}

// TestNodeRemoteResolution: a SOCKS5 CONNECT with a domain name reaches the
// upstream as a domain name (remote resolution); the fake maps it to the
// echo server and the round-trip works.
func TestNodeRemoteResolution(t *testing.T) {
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, nil, nodeMap(nodeAddr))
	backend := echoServer(t)
	_, portStr, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", backend, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	f.mapDomain("target.invalid", backend)

	c := socks5ConnectDomain(t, inst.Addr(), "target.invalid", port)
	defer c.Close()
	echoRoundTrip(t, c, c)
	assertUpstreamSaw(t, f, []string{"target.invalid:" + portStr})
}

// TestNodeNoDirectFallback: with the node pointing at a closed port the
// client request fails, neither the fake upstream nor the target server saw
// a connection, and the instance keeps serving.
func TestNodeNoDirectFallback(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	deadAddr := dead.Addr().String()
	if err := dead.Close(); err != nil {
		t.Fatalf("close dead listener: %v", err)
	}

	inst := startNodeInstance(t, nil, nodeMap(deadAddr))
	backend, conns := countEchoServer(t)

	c := dialProxy(t, inst.Addr())
	defer c.Close()
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	greeting := readFull(t, c, 2)
	if greeting[0] != 5 || greeting[1] != 0 {
		t.Fatalf("greeting reply = %v, want {5, 0}", greeting)
	}
	_, portStr, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", backend, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	// The target is the real echo server, so a direct fallback would be
	// observable as an accepted connection there.
	req := []byte{5, 1, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if _, err := c.Write(req); err != nil {
		t.Fatalf("request write: %v", err)
	}

	// Mihomo answers the SOCKS5 CONNECT with rep 0 during the handshake,
	// before dialing the node (transport/socks5 ServerHandshake). The
	// failure only surfaces when the tunnel dies.
	reply := readFull(t, c, 10)
	if reply[0] != 5 || (reply[1] != 0 && reply[1] != 5) {
		t.Fatalf("socks5 reply = %v, want rep 0x00 (pre-dial) or 0x05", reply)
	}
	// The tunnel must fail: any payload echo attempt ends in an error or
	// connection close, never a round-trip.
	payload := []byte("must-not-roundtrip")
	_, _ = c.Write(payload) // may fail when the tunnel is already closed
	_ = c.SetReadDeadline(time.Now().Add(testTimeout))
	buf := make([]byte, len(payload))
	n, err := io.ReadFull(c, buf)
	if err == nil {
		t.Fatalf("payload round-tripped despite the dead node: %q", buf[:n])
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("tunnel to the dead node neither failed nor closed within %v", testTimeout)
	}

	if conns.Load() != 0 {
		t.Fatalf("target server saw %d connections, want 0", conns.Load())
	}

	// The instance keeps serving: a fresh client gets a normal greeting.
	c2 := dialProxy(t, inst.Addr())
	defer c2.Close()
	if _, err := c2.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting write after failure: %v", err)
	}
	g2 := readFull(t, c2, 2)
	if g2[0] != 5 || g2[1] != 0 {
		t.Fatalf("greeting after failure = %v, want {5, 0}", g2)
	}
}

// TestNodeWithAuthentication: the auth rules from the plain mixed-port tests
// still hold when a node is configured.
func TestNodeWithAuthentication(t *testing.T) {
	const (
		user = "niv"
		pass = "s3cret"
	)
	authn := auth.NewAuthenticator([]auth.AuthUser{{User: user, Pass: pass}})
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, authn, nodeMap(nodeAddr))
	backend := echoServer(t)
	addr := inst.Addr()

	t.Run("socks5-auth-ok-roundtrip", func(t *testing.T) {
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
		assertUpstreamSaw(t, f, []string{backend})
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

	t.Run("http-correct-credentials", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("authed-via-node"))
		}))
		t.Cleanup(ts.Close)
		client := proxyHTTPClient(t, "http://"+user+":"+pass+"@"+addr)
		resp, err := client.Get(ts.URL + "/ok")
		if err != nil {
			t.Fatalf("GET with credentials: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "authed-via-node" {
			t.Fatalf("body = %q, err = %v", body, err)
		}
	})

	t.Run("http-no-credentials-407", func(t *testing.T) {
		client := proxyHTTPClient(t, "http://"+addr)
		resp, err := client.Get("http://target.invalid/x")
		if err != nil {
			t.Fatalf("GET without credentials: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("status = %d, want 407", resp.StatusCode)
		}
	})
}

// TestRouteDirect: without a node the instance reports "direct".
func TestRouteDirect(t *testing.T) {
	inst := startInstance(t, nil)
	if got := inst.Route(); got != "direct" {
		t.Fatalf("Route() = %q, want direct", got)
	}
}
