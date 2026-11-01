package core

import (
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestServerClientPrefixDecision(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.8")
	cases := []struct {
		name   string
		policy *clientPolicy
		user   string
		ip     netip.Addr
		want   bool
	}{
		{"nil global", &clientPolicy{}, "a", ip, true},
		{"empty global denies", &clientPolicy{global: []netip.Prefix{}}, "a", ip, false},
		{"mapped address", &clientPolicy{global: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}, "a", netip.MustParseAddr("::ffff:192.0.2.8"), true},
		{"account absent allows", &clientPolicy{accounts: map[string][]netip.Prefix{}}, "a", ip, true},
		{"account empty denies", &clientPolicy{accounts: map[string][]netip.Prefix{"a": {}}}, "a", ip, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := permitsServerClient(tc.policy, &C.Metadata{SrcIP: tc.ip, InUser: tc.user}); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestServerTunnelDropsUDP(t *testing.T) {
	var forwarded atomic.Int32
	inner := &udpCountingTunnel{forwarded: &forwarded}
	counters := &serverCounters{}
	tunnel := &serverTunnel{inner: inner, entry: &serverEntry{counters: counters}, policy: &atomic.Pointer[clientPolicy]{}}
	packet := &dropCountingPacket{}
	tunnel.HandleUDPPacket(packet, &C.Metadata{SrcIP: netip.MustParseAddr("192.0.2.1")})
	if forwarded.Load() != 0 {
		t.Fatalf("forwarded = %d, want 0", forwarded.Load())
	}
	if packet.drops != 1 {
		t.Fatalf("drops = %d, want 1", packet.drops)
	}
	if got := counters.dropped.Load(); got != 1 {
		t.Fatalf("dropped counter = %d, want 1", got)
	}
}

type udpCountingTunnel struct{ forwarded *atomic.Int32 }

func (*udpCountingTunnel) HandleTCPConn(net.Conn, *C.Metadata)        {}
func (t *udpCountingTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) { t.forwarded.Add(1) }
func (*udpCountingTunnel) NatTable() C.NatTable                       { return nil }

type dropCountingPacket struct{ drops int }

func (*dropCountingPacket) Data() []byte                            { return nil }
func (*dropCountingPacket) WriteBack([]byte, net.Addr) (int, error) { return 0, nil }
func (p *dropCountingPacket) Drop()                                 { p.drops++ }
func (*dropCountingPacket) LocalAddr() net.Addr                     { return nil }

func TestServerSetInfoNil(t *testing.T) {
	var s *serverSet
	if got := s.info(); got != nil {
		t.Fatalf("got %#v", got)
	}
}
