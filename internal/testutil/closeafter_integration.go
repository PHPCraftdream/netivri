//go:build integration

// A TCP forwarder that closes idle connections after a fixed delay, used to
// test idle-survival of prepared transports. Network-touching helpers stay
// behind the integration build tag.
package testutil

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// CloseAfterProxy is a TCP forwarder that closes both sides of every
// forwarded connection after a fixed delay, measured from accept.
type CloseAfterProxy struct {
	ln net.Listener
	cw countWaiter

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Addr returns the listening address host:port.
func (p *CloseAfterProxy) Addr() string {
	return p.ln.Addr().String()
}

// WaitAccepts blocks until n connections have been accepted.
func (p *CloseAfterProxy) WaitAccepts(t testing.TB, n int) {
	p.cw.wait(t, n, 5*time.Second)
}

// Accepts returns the number of accepted connections so far.
func (p *CloseAfterProxy) Accepts() int {
	return p.cw.value()
}

// track registers a live connection; false when the proxy is already closed
// (the caller then closes the connection itself).
func (p *CloseAfterProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *CloseAfterProxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// close stops the listener, closes every live connection and waits for the
// handlers.
func (p *CloseAfterProxy) close() {
	_ = p.ln.Close()
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// StartCloseAfterProxy listens on 127.0.0.1:0 and forwards every accepted
// connection to target, closing both sides after the given delay starting at
// accept. Everything is closed via t.Cleanup.
func StartCloseAfterProxy(t testing.TB, target string, after time.Duration) *CloseAfterProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen close-after proxy: %v", err)
	}
	p := &CloseAfterProxy{ln: ln, conns: make(map[net.Conn]struct{})}
	t.Cleanup(p.close)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted := time.Now()
			p.cw.add()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.handle(conn, target, after, accepted)
			}()
		}
	}()
	return p
}

// handle forwards one accepted connection until either side ends or the
// delay (counted from accept) fires.
func (p *CloseAfterProxy) handle(client net.Conn, target string, after time.Duration, accepted time.Time) {
	if !p.track(client) {
		_ = client.Close()
		return
	}
	defer p.untrack(client)
	defer client.Close()

	dst, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	if !p.track(dst) {
		_ = dst.Close()
		return
	}
	defer p.untrack(dst)
	defer dst.Close()

	stop := time.AfterFunc(after-time.Since(accepted), func() {
		_ = client.Close()
		_ = dst.Close()
	})
	defer stop.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, dst)
		_ = client.Close()
	}()
	_, _ = io.Copy(dst, client)
	_ = dst.Close()
	wg.Wait()
}
