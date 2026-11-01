package core

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	C "github.com/metacubex/mihomo/constant"
)

type unitServerProtocol struct {
	name   string
	fail   error
	starts atomic.Int32
	closed atomic.Int32
}

func (p *unitServerProtocol) Name() string                                           { return p.name }
func (*unitServerProtocol) SingleUser() bool                                         { return false }
func (*unitServerProtocol) Validate(map[string]string) error                         { return nil }
func (*unitServerProtocol) NewMaterial(map[string]string) (map[string]string, error) { return nil, nil }
func (*unitServerProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	return nil, nil
}
func (p *unitServerProtocol) Start(_ servers.Spec, _ servers.Env) (io.Closer, error) {
	p.starts.Add(1)
	if p.fail != nil {
		return nil, p.fail
	}
	return unitServerCloser{closed: &p.closed}, nil
}
func (*unitServerProtocol) Link(servers.Spec, string, string) (string, error) { return "", nil }

type unitServerCloser struct{ closed *atomic.Int32 }

func (c unitServerCloser) Close() error { c.closed.Add(1); return nil }

var unitServerProtocolID atomic.Uint64

func registerUnitServerProtocol(t *testing.T) *unitServerProtocol {
	t.Helper()
	p := &unitServerProtocol{name: "unit-test-protocol-" + strconv.FormatUint(unitServerProtocolID.Add(1), 10)}
	servers.Register(p)
	return p
}
func unitServerSpec(name, protocol string, port int) servers.Spec {
	return servers.Spec{Name: name, Protocol: protocol, Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}}
}

func TestServerSetCommitFailureRestoresPolicy(t *testing.T) {
	p := registerUnitServerProtocol(t)
	s := &serverSet{broker: newSocketBroker()}
	t.Cleanup(s.close)
	initial := unitServerSpec("edge", p.name, 10001)
	tx, err := s.prepare([]servers.Spec{initial}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare initial:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit initial:", err)
	}
	previousPolicy := s.policy.Load()
	failedPolicy := newClientPolicy([]netip.Prefix{}, nil)
	stageTx, err := s.prepare([]servers.Spec{unitServerSpec("staged", p.name, 10002)}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare staged:", err)
	}
	stagedClosed := p.closed.Load()
	s.broker.close()
	stageTx.policy = failedPolicy
	if err := stageTx.commit(); err != net.ErrClosed {
		t.Fatalf("commit on closed broker error = %v", err)
	}
	if s.policy.Load() != previousPolicy {
		t.Fatal("failed commit did not restore previous policy")
	}
	stageTx.rollback()
	if p.closed.Load() != stagedClosed+1 {
		t.Fatal("rollback did not close staged closer")
	}
}

func TestServerSetTransactions(t *testing.T) {
	p := registerUnitServerProtocol(t)
	s := &serverSet{broker: newSocketBroker()}
	t.Cleanup(s.close)
	first := unitServerSpec("edge", p.name, 10001)
	tx, err := s.prepare([]servers.Spec{first}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare initial:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit initial:", err)
	}
	original := s.items["edge"]
	tx, err = s.prepare([]servers.Spec{first.Clone()}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare equal:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit equal:", err)
	}
	if s.items["edge"] != original {
		t.Fatal("equal spec replaced running entry")
	}
	if got := p.starts.Load(); got != 1 {
		t.Fatalf("starts after equal prepare = %d, want 1", got)
	}
	changed := first.Clone()
	changed.Port++
	tx, err = s.prepare([]servers.Spec{changed}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare changed:", err)
	}
	if got := p.starts.Load(); got != 2 {
		t.Fatalf("starts after changed prepare = %d, want 2", got)
	}
	if s.items["edge"] != original || p.closed.Load() != 0 {
		t.Fatal("changed listener replaced entry before commit or closed old listener early")
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit changed:", err)
	}
	if got := p.starts.Load(); got != 2 {
		t.Fatalf("starts after changed commit = %d, want 2", got)
	}
	if s.items["edge"] == original || p.closed.Load() != 1 {
		t.Fatal("changed listener did not replace and close old listener")
	}
	if s.items["edge"].counters != original.counters {
		t.Fatal("counter continuity lost on replacement")
	}
	tx, err = s.prepare(nil, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal("prepare removal:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit removal:", err)
	}
	if len(s.items) != 0 || p.closed.Load() != 2 {
		t.Fatal("removal did not close listener")
	}
}

func TestClonePrefixesUnit(t *testing.T) {
	if got := clonePrefixes(nil); got != nil {
		t.Fatal("clonePrefixes(nil) is not nil")
	}
	empty := []netip.Prefix{}
	gotEmpty := clonePrefixes(empty)
	if gotEmpty == nil || len(gotEmpty) != 0 {
		t.Fatal("clonePrefixes(empty) did not preserve non-nil empty slice")
	}
	original := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	cloned := clonePrefixes(original)
	if len(cloned) != len(original) {
		t.Fatalf("clone length = %d, want %d", len(cloned), len(original))
	}
	cloned[0] = netip.MustParsePrefix("198.51.100.0/24")
	if original[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatal("mutating clone changed original")
	}
}

func TestServerSetValidationAndRollback(t *testing.T) {
	p := registerUnitServerProtocol(t)
	s := &serverSet{broker: newSocketBroker()}
	t.Cleanup(s.close)
	base := unitServerSpec("kept", p.name, 12001)
	tx, err := s.prepare([]servers.Spec{base}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal(err)
	}
	old := s.items["kept"]
	cases := []struct {
		name  string
		specs []servers.Spec
	}{
		{"duplicate name", []servers.Spec{base, base}},
		{"duplicate endpoint", []servers.Spec{base, unitServerSpec("other", p.name, base.Port)}},
		{"bad spec", []servers.Spec{{Name: "bad", Protocol: p.name, Port: 0, Accounts: []string{"alice"}}}},
		{"unknown protocol", []servers.Spec{unitServerSpec("unknown", "missing-unit-protocol", 12003)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.prepare(tc.specs, &udpCountingTunnel{}, nil, nil); err == nil {
				t.Fatal("prepare unexpectedly succeeded")
			}
		})
	}
	p.fail = errors.New("synthetic start failure")
	candidate := unitServerSpec("new", p.name, 12002)
	if _, err := s.prepare([]servers.Spec{base, candidate}, &udpCountingTunnel{}, nil, nil); err == nil {
		t.Fatal("start failure was ignored")
	}
	if s.items["kept"] != old || old.running.Closer == nil {
		t.Fatal("failed prepare changed committed entries")
	}
	p.fail = nil
	starts, closed := p.starts.Load(), p.closed.Load()
	tx, err = s.prepare([]servers.Spec{candidate}, &udpCountingTunnel{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.rollback()
	tx.rollback()
	if s.items["kept"] != old || p.starts.Load() != starts+1 || p.closed.Load() != closed+1 {
		t.Fatal("rollback did not preserve old state and close exactly the staged listener")
	}
}

func TestServerClientPolicyUnit(t *testing.T) {
	pfx := func(s string) netip.Prefix { return netip.MustParsePrefix(s) }
	addr := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	cases := []struct {
		name   string
		policy *clientPolicy
		meta   *C.Metadata
		want   bool
	}{
		{"nil metadata", nil, nil, false},
		{"nil policy", nil, &C.Metadata{SrcIP: addr("192.0.2.1")}, true},
		{"invalid ip nil global", &clientPolicy{global: nil}, &C.Metadata{}, true},
		{"invalid ip nonnil global", &clientPolicy{global: []netip.Prefix{}}, &C.Metadata{}, false},
		{"global deny", newClientPolicy([]netip.Prefix{}, nil), &C.Metadata{SrcIP: addr("192.0.2.1")}, false},
		{"account allowed", newClientPolicy(nil, map[string][]netip.Prefix{"alice": {pfx("192.0.2.0/24")}}), &C.Metadata{SrcIP: addr("192.0.2.1"), InUser: "alice"}, true},
		{"account empty deny", newClientPolicy(nil, map[string][]netip.Prefix{"alice": {}}), &C.Metadata{SrcIP: addr("192.0.2.1"), InUser: "alice"}, false},
		{"account nil allows", newClientPolicy(nil, map[string][]netip.Prefix{"alice": nil}), &C.Metadata{SrcIP: addr("192.0.2.1"), InUser: "alice"}, true},
		{"account absent allows", newClientPolicy(nil, nil), &C.Metadata{SrcIP: addr("192.0.2.1"), InUser: "alice"}, true},
		{"IPv6", newClientPolicy([]netip.Prefix{pfx("2001:db8::/32")}, nil), &C.Metadata{SrcIP: addr("2001:db8::1")}, true},
		{"mapped IPv4", newClientPolicy([]netip.Prefix{pfx("192.0.2.0/24")}, nil), &C.Metadata{SrcIP: addr("::ffff:192.0.2.1")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := permitsServerClient(tc.policy, tc.meta); got != tc.want {
				t.Fatalf("permitsServerClient() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServerSetInfoCloseAndPolicySnapshot(t *testing.T) {
	p := registerUnitServerProtocol(t)
	s := &serverSet{broker: newSocketBroker()}
	t.Cleanup(s.close)
	cloneEmpty := clonePrefixes([]netip.Prefix{})
	if cloneEmpty == nil || len(cloneEmpty) != 0 || clonePrefixes(nil) != nil {
		t.Fatal("clonePrefixes did not preserve nil versus empty")
	}
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	accounts := map[string][]netip.Prefix{"alice": prefixes}
	spec := unitServerSpec("z-info", p.name, 13001)
	spec2 := unitServerSpec("a-info", p.name, 13002)
	tx, err := s.prepare([]servers.Spec{spec, spec2}, &udpCountingTunnel{}, prefixes, accounts)
	if err != nil {
		t.Fatal(err)
	}
	prefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
	accounts["alice"][0] = prefixes[0]
	if err := tx.commit(); err != nil {
		t.Fatal(err)
	}
	if !permitsServerClient(s.policy.Load(), &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.9"), InUser: "alice"}) {
		t.Fatal("policy snapshot followed mutated source slices")
	}
	got := s.info()
	if len(got) != 2 || got[0].Name != "a-info" || got[1].Name != "z-info" || got[1].Listen != "127.0.0.1:13001" {
		t.Fatalf("unexpected info: %#v", got)
	}
	e := s.items["z-info"]
	e.counters.connections.Store(3)
	e.counters.dropped.Store(4)
	got = s.info()
	if got[1].Connections != 3 || got[1].DroppedClients != 4 {
		t.Fatalf("unexpected counters: %#v", got)
	}
	got[0].Accounts[0] = "changed"
	if s.info()[0].Accounts[0] != "alice" {
		t.Fatal("info exposed mutable account slice")
	}
	s.close()
	if len(s.info()) != 0 || p.closed.Load() != 2 {
		t.Fatal("close did not clear and close running servers")
	}
	s.close()
	if p.closed.Load() != 2 {
		t.Fatal("repeated close closed a listener more than once")
	}
}

type fakeNatTable struct{ C.NatTable }
type natCheckingTunnel struct {
	tcp   atomic.Int32
	nat   C.NatTable
	onTCP func()
}

func (n *natCheckingTunnel) HandleTCPConn(c net.Conn, _ *C.Metadata) {
	n.tcp.Add(1)
	if n.onTCP != nil {
		n.onTCP()
	}
	_ = c.Close()
}
func (*natCheckingTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (n *natCheckingTunnel) NatTable() C.NatTable                   { return n.nat }

func TestServerTunnelTCPFilteringAndNat(t *testing.T) {
	nat := &fakeNatTable{}
	inner := &natCheckingTunnel{nat: nat}
	counters := &serverCounters{}
	policy := &atomic.Pointer[clientPolicy]{}
	tw := &serverTunnel{inner: inner, entry: &serverEntry{counters: counters}, policy: policy}
	inner.onTCP = func() {
		if got := counters.connections.Load(); got != 1 {
			t.Errorf("active connections inside handler = %d, want 1", got)
		}
	}
	pair := func() (net.Conn, net.Conn) {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		deadline := time.Now().Add(5 * time.Second)
		if err := a.SetDeadline(deadline); err != nil {
			t.Fatal("set first pipe deadline:", err)
		}
		if err := b.SetDeadline(deadline); err != nil {
			t.Fatal("set second pipe deadline:", err)
		}
		return a, b
	}
	server, _ := pair()
	tw.HandleTCPConn(server, &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.8"), InUser: "alice"})
	if inner.tcp.Load() != 1 || counters.connections.Load() != 0 {
		t.Fatal("nil policy did not forward TCP with balanced connection count")
	}
	policy.Store(newClientPolicy(nil, map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}}))
	server, _ = pair()
	tw.HandleTCPConn(server, &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.8"), InUser: "alice"})
	if inner.tcp.Load() != 2 || counters.connections.Load() != 0 {
		t.Fatal("allowed TCP was not forwarded with balanced counter")
	}
	server, client := pair()
	tw.HandleTCPConn(server, &C.Metadata{SrcIP: netip.MustParseAddr("203.0.113.8"), InUser: "alice"})
	if counters.dropped.Load() != 1 || inner.tcp.Load() != 2 {
		t.Fatal("disallowed TCP was not dropped and counted")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("disallowed TCP connection remained open")
	}
	if tw.NatTable() != nat {
		t.Fatal("NatTable was not delegated")
	}
}
