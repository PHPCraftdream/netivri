package core

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type unitListener struct {
	mu       sync.Mutex
	closed   bool
	accepted chan net.Conn
	stopped  chan struct{}
	once     sync.Once
}

func newUnitListener() *unitListener {
	return &unitListener{accepted: make(chan net.Conn), stopped: make(chan struct{})}
}
func (l *unitListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accepted:
		return c, nil
	case <-l.stopped:
		return nil, net.ErrClosed
	}
}
func (l *unitListener) Close() error {
	l.once.Do(func() { l.mu.Lock(); l.closed = true; l.mu.Unlock(); close(l.stopped) })
	return nil
}
func (*unitListener) Addr() net.Addr { return unitAddr("fake") }

type unitAddr string

func (a unitAddr) Network() string { return "fake" }
func (a unitAddr) String() string  { return string(a) }

func seedUnitEndpoint(b *socketBroker, key endpointKey, l *unitListener) *socketEndpoint {
	ep := &socketEndpoint{broker: b, key: key, listener: l, leases: make(map[*socketLease]struct{}), changed: make(chan struct{})}
	b.endpoints[key] = ep
	return ep
}
func TestSocketBrokerLeasesAndTrackedConnections(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	key := endpointKey{"tcp", "fake-address"}
	listener := newUnitListener()
	seedUnitEndpoint(b, key, listener)
	s := b.stage()
	networkListener, err := s.Listen(t.Context(), key.network, key.address)
	if err != nil {
		t.Fatal("lease existing endpoint:", err)
	}
	if _, err := s.Listen(t.Context(), key.network, key.address); err == nil {
		t.Fatal("duplicate stage endpoint accepted")
	}
	if err := s.commit(); err != nil {
		t.Fatal("commit:", err)
	}
	lease := networkListener.(*socketLease)
	server, client := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal("set server deadline:", err)
	}
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal("set client deadline:", err)
	}
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	trackedInput := &brokerConn{Conn: server, broker: b}
	b.mu.Lock()
	b.conns[trackedInput] = struct{}{}
	b.mu.Unlock()
	lease.tcp <- trackedInput
	tracked, err := lease.Accept()
	if err != nil {
		t.Fatal("accept:", err)
	}
	if _, ok := tracked.(*brokerConn); !ok {
		t.Fatalf("accepted connection type = %T", tracked)
	}
	if err := tracked.Close(); err != nil {
		t.Fatal("tracked close:", err)
	}
	b.mu.Lock()
	count := len(b.conns)
	b.mu.Unlock()
	if count != 0 {
		t.Fatalf("tracked connection map has %d entries", count)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Accept(); err != net.ErrClosed {
		t.Fatalf("closed lease Accept error = %v", err)
	}
	listener.mu.Lock()
	closed := listener.closed
	listener.mu.Unlock()
	if !closed {
		t.Fatal("last lease did not close its endpoint")
	}
	b.close()
	b.close()
}
func TestSocketStageRollbackAndPacketUnsupported(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	listener := newUnitListener()
	key := endpointKey{"tcp", "rollback-address"}
	ep := seedUnitEndpoint(b, key, listener)
	s := b.stage()
	if _, err := s.Listen(t.Context(), key.network, key.address); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListenPacket(t.Context(), "udp", "anything"); err == nil {
		t.Fatal("UDP listener unexpectedly supported")
	}
	s.rollback()
	s.rollback()
	listener.mu.Lock()
	closed := listener.closed
	listener.mu.Unlock()
	if !closed || len(ep.leases) != 0 {
		t.Fatal("rollback did not close staged lease and endpoint")
	}
	if _, err := s.Listen(t.Context(), key.network, key.address); err == nil {
		t.Fatal("finished stage accepted a listener")
	}
	b.close()
}
func TestSocketStageCommitClosedBrokerAndQueuedDrain(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	key := endpointKey{"tcp", "closed-address"}
	listener := newUnitListener()
	seedUnitEndpoint(b, key, listener)
	s := b.stage()
	ln, err := s.Listen(t.Context(), key.network, key.address)
	if err != nil {
		t.Fatal(err)
	}
	lease := ln.(*socketLease)
	brokerSide, peer := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	if err := brokerSide.SetDeadline(deadline); err != nil {
		t.Fatal("set broker-side deadline:", err)
	}
	if err := peer.SetDeadline(deadline); err != nil {
		t.Fatal("set peer deadline:", err)
	}
	t.Cleanup(func() { _ = brokerSide.Close(); _ = peer.Close() })
	queued := &brokerConn{Conn: brokerSide, broker: b}
	b.mu.Lock()
	b.conns[queued] = struct{}{}
	b.mu.Unlock()
	lease.tcp <- queued
	b.close()
	if err := s.commit(); err != net.ErrClosed {
		t.Fatalf("commit on closed broker error = %v", err)
	}
	if _, err := peer.Write([]byte("x")); err != io.ErrClosedPipe {
		t.Fatalf("peer write after queued drain error = %v, want %v", err, io.ErrClosedPipe)
	}
	b.mu.Lock()
	count := len(b.conns)
	b.mu.Unlock()
	if count != 0 {
		t.Fatalf("closed broker retained %d queued connections", count)
	}
	s.rollback()
}
