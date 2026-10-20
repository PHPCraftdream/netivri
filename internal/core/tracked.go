package core

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// closeDrainTimeout bounds how long Close waits for connection handlers to
// return after their connections were closed.
const closeDrainTimeout = 2 * time.Second

// trackedShards is the number of independent mutex/map shards; a
// connection lands in shard id % trackedShards.
const trackedShards = 16

// trackedShard is one shard of the tracked connection set. closed is
// per-shard: closeAll sets it under the shard lock, so an insert either
// happens before closeAll visits the shard (and is closed by it) or sees
// the flag and closes the connection itself.
type trackedShard struct {
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// trackedTunnel wraps Mihomo's tunnel so an Instance knows every TCP
// connection it is serving. Close can then close them all and wait for the
// handler goroutines, so no connection outlives its instance and nothing
// still touches Mihomo's process-global state (logger level, ...) when the
// next instance is applied.
type trackedTunnel struct {
	inner  C.Tunnel
	shards [trackedShards]trackedShard
	wg     sync.WaitGroup
	next   atomic.Uint64
}

func newTrackedTunnel(inner C.Tunnel) *trackedTunnel {
	t := &trackedTunnel{inner: inner}
	for i := range t.shards {
		t.shards[i].conns = make(map[net.Conn]struct{})
	}
	return t
}

// HandleTCPConn implements C.Tunnel. It blocks for the life of the
// connection, like the wrapped tunnel.
func (t *trackedTunnel) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	id := t.next.Add(1) - 1
	shard := &t.shards[id%trackedShards]
	shard.mu.Lock()
	if shard.closed {
		shard.mu.Unlock()
		_ = conn.Close()
		return
	}
	shard.conns[conn] = struct{}{}
	// Under the same lock as the insert: closeAll's later wg.Wait must
	// not miss this handler.
	t.wg.Add(1)
	shard.mu.Unlock()

	defer func() {
		shard.mu.Lock()
		delete(shard.conns, conn)
		shard.mu.Unlock()
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
	for i := range t.shards {
		shard := &t.shards[i]
		shard.mu.Lock()
		shard.closed = true
		for c := range shard.conns {
			_ = c.Close()
		}
		shard.mu.Unlock()
	}

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
