package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// testPoolAddr is the pooled address of the test pools; dials to any other
// address bypass the pool.
const testPoolAddr = "127.0.0.1:9"

// fakeTimeout is a pure timeout error, like a read that hit its deadline.
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "i/o timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

// gateConn is a fake pooled socket: the first Read blocks until the
// gate is released and then reports a read timeout (a healthy idle
// socket), so a test can observe that the pool mutex is not held
// across the socket-liveness check. Deadlines are accepted and ignored.
type gateConn struct {
	release chan struct{}
	entered chan struct{}
	closed  atomic.Bool
}

func (c *gateConn) Read([]byte) (int, error) {
	c.entered <- struct{}{}
	<-c.release
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return 0, fakeTimeout{}
}
func (c *gateConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *gateConn) Close() error                     { c.closed.Store(true); return nil }
func (c *gateConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *gateConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *gateConn) SetDeadline(time.Time) error      { return nil }
func (c *gateConn) SetReadDeadline(time.Time) error  { return nil }
func (c *gateConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "tcp" }
func (dummyAddr) String() string  { return "127.0.0.1:0" }

// errDialer never succeeds: pool fallbacks in unit tests must not dial.
type errDialer struct{}

func (errDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unit test: unexpected dial")
}
func (errDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, errors.New("unit test: unexpected listen")
}

// stubDialer hands out a fixed conn without any network activity.
type stubDialer struct{ conn net.Conn }

func (s stubDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return s.conn, nil
}
func (stubDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, errors.New("unit test: unexpected listen")
}

// newTestPool builds a poolDialer without the refill loop.
func newTestPool(t *testing.T, idle []idleTCP, underlying C.Dialer) *poolDialer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &poolDialer{
		addr:       testPoolAddr,
		size:       len(idle) + 1,
		underlying: underlying,
		idle:       idle,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		owner:      &warmer{},
	}
}

// deadConn is a pipe whose peer is closed: its liveness read returns
// EOF, so poolSocketAlive is deterministically false.
func deadConn(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	_ = server.Close()
	return client
}

// aliveConn is an open pipe: its liveness read times out, so
// poolSocketAlive is deterministically true.
func aliveConn(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client
}

func TestPoolHitDoesNotBlockOtherDialersDuringLivenessCheck(t *testing.T) {
	gate := &gateConn{release: make(chan struct{}), entered: make(chan struct{}, 1)}
	alive := aliveConn(t)
	pool := newTestPool(t, []idleTCP{
		{conn: deadConn(t), at: time.Now()},
		{conn: alive, at: time.Now()},
		{conn: gate, at: time.Now()},
	}, errDialer{})

	addr := testPoolAddr
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	gotA := make(chan net.Conn, 1)
	errA := make(chan error, 1)
	go func() {
		conn, err := pool.DialContext(ctx, "tcp", addr)
		if err == nil {
			gotA <- conn
		} else {
			errA <- err
		}
	}()
	// The gated conn is the newest entry: its liveness check runs first
	// and must run without the mutex.
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("liveness check of the gated conn never started")
	}

	// While the gated check is still blocked, a second dialer must get
	// the next candidate immediately (a mutex-held check would block it
	// until the gate is released and then time out).
	connB, err := pool.DialContext(ctx, "tcp", addr)
	if err != nil {
		close(gate.release)
		t.Fatalf("second dialer blocked by the first liveness check: %v", err)
	}
	if connB != alive {
		close(gate.release)
		t.Fatal("second dialer did not receive the alive candidate")
	}

	close(gate.release)
	var connA net.Conn
	select {
	case connA = <-gotA:
	case err := <-errA:
		t.Fatalf("first dialer failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("first dialer never finished")
	}
	if connA != gate {
		t.Fatal("first dialer did not receive the gated conn")
	}
	if pool.owner.poolHits.Load() != 2 || pool.owner.poolMisses.Load() != 0 {
		t.Fatalf("unexpected pool metrics: hits=%d misses=%d",
			pool.owner.poolHits.Load(), pool.owner.poolMisses.Load())
	}
}

func TestPoolDiscardsDeadEntryAndFallsThroughToUnderlying(t *testing.T) {
	underlying := aliveConn(t)
	pool := newTestPool(t, []idleTCP{{conn: deadConn(t), at: time.Now()}}, stubDialer{conn: underlying})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pool.DialContext(ctx, "tcp", testPoolAddr)
	if err != nil {
		t.Fatal(err)
	}
	if conn != underlying {
		t.Fatal("dial did not fall through to the underlying dialer")
	}
	if pool.owner.poolDiscards.Load() != 1 || pool.owner.poolMisses.Load() != 1 {
		t.Fatalf("unexpected pool metrics: discards=%d misses=%d",
			pool.owner.poolDiscards.Load(), pool.owner.poolMisses.Load())
	}
	pool.mu.Lock()
	empty := len(pool.idle) == 0
	pool.mu.Unlock()
	if !empty {
		t.Fatal("pool kept an entry after the miss")
	}
}

func TestPoolExpireDropsDeadKeepsAlive(t *testing.T) {
	alive := aliveConn(t)
	pool := newTestPool(t, []idleTCP{
		{conn: deadConn(t), at: time.Now()},
		{conn: alive, at: time.Now()},
	}, errDialer{})

	pool.expire()
	if pool.owner.poolDiscards.Load() != 1 {
		t.Fatalf("dead entry not discarded: discards=%d", pool.owner.poolDiscards.Load())
	}
	pool.mu.Lock()
	idle := append([]idleTCP(nil), pool.idle...)
	pool.mu.Unlock()
	if len(idle) != 1 || idle[0].conn != alive {
		t.Fatalf("alive entry not kept: %#v", idle)
	}
}

// TestPoolExpireWhileClosingClosesKeptConns: stop() runs while expire is
// inside a liveness check; the connection kept afterwards must be closed
// instead of being put back into the stopped pool.
func TestPoolExpireWhileClosingClosesKeptConns(t *testing.T) {
	gate := &gateConn{release: make(chan struct{}), entered: make(chan struct{}, 1)}
	pool := newTestPool(t, []idleTCP{{conn: gate, at: time.Now()}}, errDialer{})

	done := make(chan struct{})
	go func() {
		pool.expire()
		close(done)
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("expire never started the liveness check")
	}
	pool.stop()
	close(gate.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expire did not return")
	}
	if !gate.closed.Load() {
		t.Fatal("a connection kept by expire escaped the stopped pool")
	}
	pool.mu.Lock()
	empty := len(pool.idle) == 0
	pool.mu.Unlock()
	if !empty {
		t.Fatal("expire put a connection back into a stopped pool")
	}
}

// TestPoolExpireCapDropsOldest: more live entries than the size cap leaves
// the newest ones; the dropped connection is closed and counted.
func TestPoolExpireCapDropsOldest(t *testing.T) {
	oldest := aliveConn(t)
	newest := aliveConn(t)
	pool := newTestPool(t, []idleTCP{
		{conn: oldest, at: time.Now()},
		{conn: newest, at: time.Now()},
	}, errDialer{})
	pool.size = 1
	pool.expire()
	pool.mu.Lock()
	idle := append([]idleTCP(nil), pool.idle...)
	pool.mu.Unlock()
	if len(idle) != 1 || idle[0].conn != newest {
		t.Fatalf("the cap must keep the newest entry: %#v", idle)
	}
	if pool.owner.poolDiscards.Load() != 1 {
		t.Fatalf("dropped entry not counted: discards=%d", pool.owner.poolDiscards.Load())
	}
	if _, err := oldest.Write([]byte("x")); err == nil {
		t.Fatal("the dropped connection was not closed")
	}
}

func TestPoolExpireAfterCloseClosesKeptConns(t *testing.T) {
	alive := aliveConn(t)
	pool := newTestPool(t, []idleTCP{{conn: alive, at: time.Now()}}, errDialer{})
	pool.mu.Lock()
	pool.closed = true
	pool.mu.Unlock()

	pool.expire()
	if pool.owner.poolDiscards.Load() != 1 {
		t.Fatalf("kept conn not discarded after close: %d", pool.owner.poolDiscards.Load())
	}
	if _, err := alive.Write([]byte("x")); err == nil {
		t.Fatal("kept conn was not closed after the pool was closed")
	}
	pool.mu.Lock()
	empty := len(pool.idle) == 0
	pool.mu.Unlock()
	if !empty {
		t.Fatal("kept conn was put back into a closed pool")
	}
}
