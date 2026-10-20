package core

import (
	"net"
	"sync"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// closeDrainTimeout bounds how long Close waits for connection handlers to
// return after their connections were closed.
const closeDrainTimeout = 2 * time.Second

// trackedTunnel wraps Mihomo's tunnel so an Instance knows every TCP
// connection it is serving. Close can then close them all and wait for the
// handler goroutines, so no connection outlives its instance and nothing
// still touches Mihomo's process-global state (logger level, ...) when the
// next instance is applied.
type trackedTunnel struct {
	inner C.Tunnel

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func newTrackedTunnel(inner C.Tunnel) *trackedTunnel {
	return &trackedTunnel{inner: inner, conns: make(map[net.Conn]struct{})}
}

// HandleTCPConn implements C.Tunnel. It blocks for the life of the
// connection, like the wrapped tunnel.
func (t *trackedTunnel) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = conn.Close()
		return
	}
	t.conns[conn] = struct{}{}
	t.wg.Add(1)
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.conns, conn)
		t.mu.Unlock()
		t.wg.Done()
	}()
	t.inner.HandleTCPConn(conn, metadata)
}

// HandleUDPPacket implements C.Tunnel (non-blocking, not tracked).
func (t *trackedTunnel) HandleUDPPacket(packet C.UDPPacket, metadata *C.Metadata) {
	t.inner.HandleUDPPacket(packet, metadata)
}

// NatTable implements C.Tunnel.
func (t *trackedTunnel) NatTable() C.NatTable { return t.inner.NatTable() }

// closeAll refuses new connections, closes the tracked ones and waits for
// their handlers, at most timeout.
func (t *trackedTunnel) closeAll(timeout time.Duration) {
	t.mu.Lock()
	t.closed = true
	for c := range t.conns {
		_ = c.Close()
	}
	t.mu.Unlock()

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
