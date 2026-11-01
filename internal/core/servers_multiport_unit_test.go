package core

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
)

type multiportTestAddr string

func (a multiportTestAddr) Network() string { return "test" }
func (a multiportTestAddr) String() string  { return string(a) }

type multiportFakeConn struct {
	input   *bytes.Reader
	written bytes.Buffer
	closed  bool
}

func (c *multiportFakeConn) Read(p []byte) (int, error) {
	if c.input == nil || c.input.Len() == 0 {
		return 0, io.EOF
	}
	if len(p) > 7 {
		p = p[:7]
	}
	return c.input.Read(p)
}
func (c *multiportFakeConn) Write(p []byte) (int, error)    { return c.written.Write(p) }
func (c *multiportFakeConn) Close() error                   { c.closed = true; return nil }
func (*multiportFakeConn) LocalAddr() net.Addr              { return multiportTestAddr("local") }
func (*multiportFakeConn) RemoteAddr() net.Addr             { return multiportTestAddr("remote") }
func (*multiportFakeConn) SetDeadline(time.Time) error      { return nil }
func (*multiportFakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*multiportFakeConn) SetWriteDeadline(time.Time) error { return nil }

type multiportTimeoutErr struct{}

func (multiportTimeoutErr) Error() string   { return "timed out" }
func (multiportTimeoutErr) Timeout() bool   { return true }
func (multiportTimeoutErr) Temporary() bool { return true }

type multiportDeadlineConn struct {
	*multiportFakeConn
	deadlines int
}

func (c *multiportDeadlineConn) SetReadDeadline(time.Time) error { c.deadlines++; return nil }

type multiportErrorReadConn struct {
	*multiportDeadlineConn
	data []byte
	err  error
}

func (c *multiportErrorReadConn) Read(p []byte) (int, error) {
	if len(c.data) > 0 {
		n := copy(p, c.data)
		c.data = c.data[n:]
		return n, nil
	}
	return 0, c.err
}

func multiportClientHello(t *testing.T, serverName string, protocols []string) []byte {
	t.Helper()
	conn := &multiportFakeConn{}
	client := tls.Client(conn, &tls.Config{ServerName: serverName, NextProtos: protocols})
	_ = client.Handshake()
	return append([]byte(nil), conn.written.Bytes()...)
}

func TestMultiportBrokerClassifiesClientHello(t *testing.T) {
	helloBytes := multiportClientHello(t, "one.example", []string{"h2"})
	otherHello := multiportClientHello(t, "other.example", []string{"h2"})
	for _, tc := range []struct {
		name       string
		specs      []servers.Spec
		input      []byte
		wantLease  string
		wantClosed bool
		wantCount  uint64
	}{
		{name: "match", specs: []servers.Spec{{Name: "one", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}, {Name: "two", Protocol: "trojan", Params: map[string]string{"sni": "two.example"}}}, input: helloBytes, wantLease: "one"},
		{name: "unknown TLS", specs: []servers.Spec{{Name: "one", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}}, input: otherHello, wantClosed: true, wantCount: 1},
		{name: "nonTLS", specs: []servers.Spec{{Name: "one", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}}, input: []byte("GET /"), wantClosed: true, wantCount: 1},
		{name: "default", specs: []servers.Spec{{Name: "one", Protocol: "trojan", Params: map[string]string{"sni": "other.example"}}, {Name: "fallback", Protocol: "trojan", Default: true, Params: map[string]string{"sni": "fallback.example"}}}, input: helloBytes, wantLease: "fallback"},
		{name: "nonTLS default", specs: []servers.Spec{{Name: "fallback", Protocol: "trojan", Default: true, Params: map[string]string{"sni": "fallback.example"}}}, input: []byte("GET /"), wantClosed: true, wantCount: 1},
		{name: "ALPN overlap", specs: []servers.Spec{{Name: "one", Protocol: "trojan", Params: map[string]string{"sni": "one.example", "alpn": "h2"}}, {Name: "two", Protocol: "trojan", Params: map[string]string{"sni": "one.example", "alpn": "h2,http/1.1"}}}, input: helloBytes, wantClosed: true, wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSocketBroker()
			key := endpointKey{"tcp", "multiport-test"}
			ep := seedUnitEndpoint(b, key, newUnitListener())
			ep.members = make(map[string]*socketLease)
			for i := range tc.specs {
				sp := tc.specs[i].Clone()
				lease := &socketLease{endpoint: ep, done: make(chan struct{}), tcp: make(chan net.Conn, 2), spec: &sp, name: sp.Name}
				ep.leases[lease] = struct{}{}
				ep.members[lease.name] = lease
			}
			fake := &multiportFakeConn{input: bytes.NewReader(append([]byte(nil), tc.input...))}
			conn := &brokerConn{Conn: fake, broker: b}
			b.conns[conn] = struct{}{}
			b.classify(ep, conn, activeMembers(ep))
			if tc.wantLease != "" {
				lease := ep.members[tc.wantLease]
				if lease == nil {
					t.Fatalf("lease %q missing", tc.wantLease)
				}
				select {
				case delivered := <-lease.tcp:
					got, err := io.ReadAll(delivered)
					if err != nil {
						t.Fatal("read replayed connection:", err)
					}
					if !bytes.Equal(got, tc.input) {
						t.Fatal("replayed bytes differ from ClientHello")
					}
					if fake.closed {
						t.Fatal("connection closed on delivery")
					}
					_ = delivered.Close()
				default:
					t.Fatal("classified connection was not delivered")
				}
			}
			if tc.wantClosed && !fake.closed {
				t.Fatal("unmatched connection was not closed")
			}
			if got := ep.unclassified.Load(); got != tc.wantCount {
				t.Fatalf("unclassified count = %d, want %d", got, tc.wantCount)
			}
			b.mu.Lock()
			delete(b.conns, conn)
			b.mu.Unlock()
			for lease := range ep.leases {
				lease.close()
			}
		})
	}
}

func TestMultiportBrokerEmptyTruncatedAndJoinedTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    []byte
		readErr error
		slow    uint64
	}{
		{name: "empty", readErr: io.EOF},
		{name: "truncated", data: []byte{22, 3, 1}, readErr: io.EOF},
		{name: "joined timeout", readErr: errors.Join(io.EOF, multiportTimeoutErr{}), slow: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSocketBroker()
			ep := seedUnitEndpoint(b, endpointKey{"tcp", "multiport-timeout"}, newUnitListener())
			fc := &multiportDeadlineConn{multiportFakeConn: &multiportFakeConn{input: bytes.NewReader(nil)}}
			readConn := &multiportErrorReadConn{multiportDeadlineConn: fc, data: tc.data, err: tc.readErr}
			conn := &brokerConn{Conn: readConn, broker: b}
			b.conns[conn] = struct{}{}
			b.classify(ep, conn, nil)
			if got := ep.slowFirstByte.Load(); got != tc.slow {
				t.Fatalf("slowFirstByte = %d, want %d", got, tc.slow)
			}
			if got := ep.unclassified.Load(); got != 1 {
				t.Fatalf("unclassified = %d, want 1", got)
			}
			if !fc.closed || fc.deadlines != 1 {
				t.Fatalf("closed=%t read deadlines=%d", fc.closed, fc.deadlines)
			}
		})
	}
}

func multiportSpec(name string, port int) servers.Spec {
	return servers.Spec{Name: name, Protocol: "trojan", Bind: "127.0.0.1", Port: port, Params: map[string]string{"sni": name + ".example"}}
}

func multiportSeedLease(ep *socketEndpoint, sp servers.Spec) *socketLease {
	if ep.members == nil {
		ep.members = make(map[string]*socketLease)
	}
	copy := sp.Clone()
	l := &socketLease{endpoint: ep, done: make(chan struct{}), tcp: make(chan net.Conn, 4), spec: &copy, name: copy.Name}
	ep.leases[l] = struct{}{}
	ep.members[l.name] = l
	return l
}

func TestMultiportStageFullMembershipAndRollback(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	a, bb := multiportSpec("A", 41001), multiportSpec("B", 41001)
	key := endpointKey{"tcp", a.ListenAddress()}
	ep := seedUnitEndpoint(b, key, newUnitListener())
	oldA, oldB := multiportSeedLease(ep, a), multiportSeedLease(ep, bb)
	st := b.stage()
	st.setServers([]servers.Spec{a, bb})
	if err := st.commit(); err != nil {
		t.Fatal(err)
	}
	if ep.members["A"] != oldA {
		t.Fatal("unchanged A lease pointer changed")
	}
	queued := &multiportFakeConn{input: bytes.NewReader(nil)}
	oldB.tcp <- queued
	replacement := b.stage()
	replacement.setServers([]servers.Spec{a})
	if err := replacement.commit(); err != nil {
		t.Fatal(err)
	}
	if ep.members["B"] != nil {
		t.Fatal("removed B remains active")
	}
	replacement.rollbackActivation()
	if ep.members["A"] != oldA || ep.members["B"] != oldB {
		t.Fatal("rollback did not restore A/B membership")
	}
	if queued.closed {
		t.Fatal("rollback prematurely closed restored queued connection")
	}
	oldA.close()
	oldB.close()
}

func TestMultiportStageMoveMemberAndRollback(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	a, bb := multiportSpec("A", 42001), multiportSpec("B", 42001)
	left := seedUnitEndpoint(b, endpointKey{"tcp", a.ListenAddress()}, newUnitListener())
	movedB := multiportSpec("B", 42002)
	right := seedUnitEndpoint(b, endpointKey{"tcp", movedB.ListenAddress()}, newUnitListener())
	oldA := multiportSeedLease(left, a)
	oldB := multiportSeedLease(left, bb)
	st := b.stage()
	st.setServers([]servers.Spec{a, bb})
	if err := st.commit(); err != nil {
		t.Fatal(err)
	}
	move := b.stage()
	move.setServers([]servers.Spec{a, movedB})
	newB, err := move.forServer(movedB).Listen(t.Context(), right.key.network, right.key.address)
	if err != nil {
		t.Fatal(err)
	}
	if err := move.commit(); err != nil {
		t.Fatal(err)
	}
	if left.members["B"] != nil || right.members["B"] != newB {
		t.Fatal("B was not moved to second endpoint")
	}
	move.rollbackActivation()
	if left.members["A"] != oldA || left.members["B"] != oldB || len(right.members) != 0 {
		t.Fatal("rollback did not restore original membership maps")
	}
	st.rollback()
}
