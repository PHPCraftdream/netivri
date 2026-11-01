package core

import (
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

type innerUnitTunnel struct {
	calls        int
	conn         net.Conn
	meta         *C.Metadata
	connections  *atomic.Int64
	activeAtCall int64
}

func (t *innerUnitTunnel) HandleTCPConn(c net.Conn, m *C.Metadata) {
	t.calls++
	t.conn = c
	t.meta = m
	if t.connections != nil {
		t.activeAtCall = t.connections.Load()
	}
}
func (*innerUnitTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (*innerUnitTunnel) NatTable() C.NatTable                     { return nil }

func TestServerTunnelInnerBypassesClientPolicy(t *testing.T) {
	counters := &serverCounters{}
	inner := &innerUnitTunnel{connections: &counters.connections}
	counters.connections.Store(7)
	counters.dropped.Store(11)
	policy := &atomic.Pointer[clientPolicy]{}
	policy.Store(newClientPolicy([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil))
	tw := &serverTunnel{inner: inner, entry: &serverEntry{counters: counters}, policy: policy}

	conn := &closeTrackingConn{}
	meta := &C.Metadata{Type: C.INNER}
	tw.HandleTCPConn(conn, meta)
	if inner.calls != 1 || inner.conn != conn || inner.meta != meta {
		t.Fatal("INNER metadata or connection was not forwarded unchanged exactly once")
	}
	if conn.closed {
		t.Fatal("INNER connection was closed")
	}
	if counters.connections.Load() != 7 || inner.activeAtCall != 7 || counters.dropped.Load() != 11 {
		t.Fatal("INNER changed server counters")
	}
}

type closeTrackingConn struct{ closed bool }

func (c *closeTrackingConn) Read([]byte) (int, error)       { return 0, nil }
func (c *closeTrackingConn) Write([]byte) (int, error)      { return 0, nil }
func (c *closeTrackingConn) Close() error                   { c.closed = true; return nil }
func (*closeTrackingConn) LocalAddr() net.Addr              { return nil }
func (*closeTrackingConn) RemoteAddr() net.Addr             { return nil }
func (*closeTrackingConn) SetDeadline(time.Time) error      { return nil }
func (*closeTrackingConn) SetReadDeadline(time.Time) error  { return nil }
func (*closeTrackingConn) SetWriteDeadline(time.Time) error { return nil }

func TestServerTunnelFiltersNonInnerInvalidClientIP(t *testing.T) {
	inner := &innerUnitTunnel{}
	counters := &serverCounters{}
	policy := &atomic.Pointer[clientPolicy]{}
	policy.Store(newClientPolicy([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil))
	tw := &serverTunnel{inner: inner, entry: &serverEntry{counters: counters}, policy: policy}

	for index, typ := range []C.Type{C.VLESS, C.SHADOWSOCKS} {
		conn := &closeTrackingConn{}
		meta := &C.Metadata{Type: typ}
		tw.HandleTCPConn(conn, meta)
		if inner.calls != 0 {
			t.Fatalf("type %v reached inner tunnel", typ)
		}
		if counters.connections.Load() != 0 || counters.dropped.Load() != uint64(index+1) {
			t.Fatalf("type %v counters = connections %d, dropped %d", typ, counters.connections.Load(), counters.dropped.Load())
		}
		if !conn.closed {
			t.Fatalf("type %v connection remained open", typ)
		}
	}
}
