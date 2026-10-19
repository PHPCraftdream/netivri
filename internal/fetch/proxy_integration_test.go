//go:build integration

package fetch

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// handlerReadTimeout bounds every blocking read in the fake proxies so a
// wedged peer can never leak a handler goroutine past the test.
const handlerReadTimeout = 10 * time.Second

// pipe copies between the two connections; as soon as one direction
// ends, both connections are closed so the other direction unblocks.
func pipe(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(a, b)
		a.Close()
		b.Close()
	}()
	_, _ = io.Copy(b, a)
	a.Close()
	b.Close()
	<-done
}

// connSet tracks every accepted client and upstream connection and the
// handler goroutines, so Cleanup can close everything and wait.
type connSet struct {
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
	wg      sync.WaitGroup
}

func newConnSet() *connSet {
	return &connSet{conns: make(map[net.Conn]struct{})}
}

// track registers c; once cleanup has started, c is closed immediately so
// a late connection can never outlive the test.
func (cs *connSet) track(c net.Conn) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closing {
		c.Close()
		return
	}
	cs.conns[c] = struct{}{}
}

func (cs *connSet) forget(c net.Conn) {
	cs.mu.Lock()
	delete(cs.conns, c)
	cs.mu.Unlock()
}

func (cs *connSet) cleanup() {
	cs.mu.Lock()
	cs.closing = true
	for c := range cs.conns {
		c.Close()
	}
	cs.mu.Unlock()
	cs.wg.Wait()
}

// goHandle runs handle in a tracked goroutine.
func (cs *connSet) goHandle(handle func()) {
	cs.wg.Add(1)
	go func() {
		defer cs.wg.Done()
		handle()
	}()
}

// httpProxy is a minimal HTTP CONNECT + absolute-form forward proxy that
// counts accepted connections and records what it was asked to do.
type httpProxy struct {
	mu       sync.Mutex
	ln       net.Listener
	cs       *connSet
	conns    int
	connects int // CONNECT requests seen
	dests    []string
	auths    []string // Proxy-Authorization values seen (CONNECT and GET)
	wantAuth string   // when non-empty, requests without it are rejected
}

func newHTTPProxy(t *testing.T) *httpProxy {
	t.Helper()
	p := &httpProxy{cs: newConnSet()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p.ln = ln
	t.Cleanup(func() {
		ln.Close()
		p.cs.cleanup()
	})
	p.cs.goHandle(p.serve)
	return p
}

func (p *httpProxy) addr() string { return p.ln.Addr().String() }

// setWantAuth makes the proxy reject requests whose Proxy-Authorization
// differs from v (empty disables the check).
func (p *httpProxy) setWantAuth(v string) {
	p.mu.Lock()
	p.wantAuth = v
	p.mu.Unlock()
}

func (p *httpProxy) snapshot() (conns, connects int, dests, auths []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns, p.connects, append([]string(nil), p.dests...), append([]string(nil), p.auths...)
}

func (p *httpProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conns++
		p.mu.Unlock()
		p.cs.track(conn)
		p.cs.goHandle(func() {
			defer conn.Close()
			defer p.cs.forget(conn)
			p.handle(conn)
		})
	}
}

func (p *httpProxy) handle(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(handlerReadTimeout))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	auth := req.Header.Get("Proxy-Authorization")
	if req.Method == http.MethodConnect {
		p.mu.Lock()
		p.connects++
		p.mu.Unlock()
		p.record(req.Host)
	} else {
		p.record(req.URL.Host)
	}
	p.mu.Lock()
	p.auths = append(p.auths, auth)
	p.mu.Unlock()
	p.mu.Lock()
	want := p.wantAuth
	p.mu.Unlock()
	if want != "" && auth != want {
		res := &http.Response{StatusCode: http.StatusProxyAuthRequired, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("auth required"))}
		res.Header.Set("Proxy-Authenticate", "Basic")
		_ = res.Write(conn)
		return
	}
	if req.Method == http.MethodConnect {
		target, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
		if err != nil {
			res := &http.Response{StatusCode: http.StatusBadGateway, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no dial"))}
			_ = res.Write(conn)
			return
		}
		p.cs.track(target)
		defer p.cs.forget(target)
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			target.Close()
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		pipe(conn, target)
		return
	}
	// Absolute-form forward request, bounded in time.
	ctx, cancel := context.WithTimeout(context.Background(), handlerReadTimeout)
	defer cancel()
	_ = conn.SetWriteDeadline(time.Now().Add(handlerReadTimeout))
	out := req.Clone(ctx)
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	resp, err := tr.RoundTrip(out)
	tr.CloseIdleConnections()
	if err != nil {
		res := &http.Response{StatusCode: http.StatusBadGateway, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("upstream failed"))}
		_ = res.Write(conn)
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(conn)
}

func (p *httpProxy) record(dest string) {
	p.mu.Lock()
	p.dests = append(p.dests, dest)
	p.mu.Unlock()
}

// socksProxy is a minimal SOCKS5 server (no-auth and username/password)
// that records the destination host exactly as the client sent it.
type socksProxy struct {
	mu      sync.Mutex
	ln      net.Listener
	cs      *connSet
	conns   int
	dests   []string // recorded destination hosts (domain or IP), as received
	badAuth int
	user    string // empty = no-auth only
	pass    string
	resolve map[string]string // destination name -> IP to dial locally
}

func newSocksProxy(t *testing.T, user, pass string) *socksProxy {
	t.Helper()
	p := &socksProxy{cs: newConnSet(), user: user, pass: pass, resolve: map[string]string{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p.ln = ln
	t.Cleanup(func() {
		ln.Close()
		p.cs.cleanup()
	})
	p.cs.goHandle(p.serve)
	return p
}

func (p *socksProxy) addr() string { return p.ln.Addr().String() }

// setResolve maps a destination name the proxy receives to the IP it
// dials locally, simulating the proxy's own DNS.
func (p *socksProxy) setResolve(name, ip string) {
	p.mu.Lock()
	p.resolve[name] = ip
	p.mu.Unlock()
}

func (p *socksProxy) snapshot() (conns int, dests []string, badAuth int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns, append([]string(nil), p.dests...), p.badAuth
}

func (p *socksProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conns++
		p.mu.Unlock()
		p.cs.track(conn)
		p.cs.goHandle(func() {
			defer conn.Close()
			defer p.cs.forget(conn)
			p.handle(conn)
		})
	}
}

func (p *socksProxy) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(handlerReadTimeout))
	br := bufio.NewReader(conn)
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 0x05 {
		return
	}
	nm := int(hdr[1])
	if nm > 8 {
		return
	}
	methods := make([]byte, nm)
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	hasUserPass := p.user != ""
	choose := byte(0xFF)
	for _, m := range methods {
		if m == 0x00 && !hasUserPass {
			choose = 0x00
		}
		if m == 0x02 && hasUserPass {
			choose = 0x02
		}
	}
	if _, err := conn.Write([]byte{0x05, choose}); err != nil {
		return
	}
	if choose == 0xFF {
		return
	}
	if choose == 0x02 {
		// RFC 1929: VER ULEN UNAME PLEN PASSWD.
		var ver [1]byte
		if _, err := io.ReadFull(br, ver[:]); err != nil || ver[0] != 0x01 {
			return
		}
		var ul [1]byte
		if _, err := io.ReadFull(br, ul[:]); err != nil {
			return
		}
		user := make([]byte, ul[0])
		if _, err := io.ReadFull(br, user); err != nil {
			return
		}
		var pl [1]byte
		if _, err := io.ReadFull(br, pl[:]); err != nil {
			return
		}
		pass := make([]byte, pl[0])
		if _, err := io.ReadFull(br, pass); err != nil {
			return
		}
		if string(user) != p.user || string(pass) != p.pass {
			p.mu.Lock()
			p.badAuth++
			p.mu.Unlock()
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		_, _ = conn.Write([]byte{0x01, 0x00})
	}

	var req [4]byte
	if _, err := io.ReadFull(br, req[:]); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x01 {
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		var b [4]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(br, l[:]); err != nil {
			return
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(br, name); err != nil {
			return
		}
		host = string(name)
	case 0x04:
		var b [16]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	default:
		return
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(br, portBytes[:]); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes[:]))
	p.mu.Lock()
	p.dests = append(p.dests, host)
	dialHost := host
	if ip, ok := p.resolve[host]; ok {
		dialHost = ip
	}
	p.mu.Unlock()

	target, err := net.DialTimeout("tcp", net.JoinHostPort(dialHost, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	p.cs.track(target)
	defer p.cs.forget(target)
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		target.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	pipe(conn, target)
}

// silentSocksProxy accepts TCP connections but never answers the SOCKS
// greeting, to prove the fetcher respects its timeout against a wedged
// proxy. Client connections are tracked in a connSet whose wait group
// covers the handlers only; the accept loop has its own signal.
type silentSocksProxy struct {
	ln           net.Listener
	cs           *connSet
	serveDone    chan struct{}
	accepted     chan struct{} // closed once the first connection is registered
	acceptedOnce sync.Once
}

func newSilentSocksProxy(t *testing.T) *silentSocksProxy {
	t.Helper()
	p := &silentSocksProxy{cs: newConnSet(), serveDone: make(chan struct{}), accepted: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p.ln = ln
	t.Cleanup(func() {
		ln.Close()
		<-p.serveDone
		p.cs.cleanup()
	})
	go p.serve()
	return p
}

func (p *silentSocksProxy) addr() string { return p.ln.Addr().String() }

// waitHandlers reports whether the connection's handler returned within d
// (it returns when the client closes its side). It first waits until the
// connection is registered, so WaitGroup.Add never races with Wait.
func (p *silentSocksProxy) waitHandlers(d time.Duration) bool {
	deadline := time.After(d)
	select {
	case <-p.accepted:
	case <-deadline:
		return false
	}
	ch := make(chan struct{})
	go func() {
		p.cs.wg.Wait()
		close(ch)
	}()
	select {
	case <-ch:
		return true
	case <-deadline:
		return false
	}
}

func (p *silentSocksProxy) serve() {
	defer close(p.serveDone)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.cs.track(conn)
		p.cs.goHandle(func() {
			defer conn.Close()
			defer p.cs.forget(conn)
			// Bounded even if the fetcher never closes its side.
			_ = conn.SetDeadline(time.Now().Add(handlerReadTimeout))
			_, _ = io.Copy(io.Discard, conn)
		})
		p.acceptedOnce.Do(func() { close(p.accepted) })
	}
}
