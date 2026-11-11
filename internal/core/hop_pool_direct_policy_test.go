package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/metacubex/mihomo/component/loopback"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/miekg/dns"
)

type directPolicyTestResolver struct {
	calls int
	host  string
}

func (r *directPolicyTestResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.LookupIPv4(ctx, host)
}
func (r *directPolicyTestResolver) LookupIPv4(_ context.Context, host string) ([]netip.Addr, error) {
	r.calls++
	r.host = host
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}
func (*directPolicyTestResolver) LookupIPv6(context.Context, string) ([]netip.Addr, error) {
	panic("unexpected LookupIPv6")
}
func (*directPolicyTestResolver) ResolveECH(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (*directPolicyTestResolver) ExchangeContext(context.Context, *dns.Msg) (*dns.Msg, error) {
	return nil, nil
}
func (*directPolicyTestResolver) Invalid() bool    { return true }
func (*directPolicyTestResolver) ClearCache()      {}
func (*directPolicyTestResolver) ResetConnection() {}

func TestDirectPostUsesStableDirectPolicyForLoopbackCarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxy := newNativeLoopbackProxy(t, "http")
	resolverStub := &directPolicyTestResolver{}
	previousResolver := resolver.DirectHostResolver
	resolver.DirectHostResolver = resolverStub
	t.Cleanup(func() { resolver.DirectHostResolver = previousResolver })

	_, portText, err := net.SplitHostPort(proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	spec := newHopPoolSpec(postHopPool, []string{"http://proxy-policy.test:" + portText}, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	adapter := newHopPoolAdapter("direct-post-policy", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	for _, hop := range adapter.hops {
		hop.directCarrier = true
	}
	conn, err := adapter.DialContext(ctx, &C.Metadata{
		NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443,
	})
	if err != nil || conn == nil {
		t.Fatalf("direct post carrier failed to reach loopback proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if resolverStub.calls != 1 || resolverStub.host != "proxy-policy.test" {
		t.Fatalf("DIRECT host resolver calls = %d for %q, want one for proxy-policy.test", resolverStub.calls, resolverStub.host)
	}
	if proxy.requests.Load() != 1 {
		t.Fatalf("direct post loopback proxy requests = %d, want one", proxy.requests.Load())
	}
	localHost, localPortText, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	localIP, err := netip.ParseAddr(localHost)
	if err != nil {
		t.Fatal(err)
	}
	localPort, err := strconv.Atoi(localPortText)
	if err != nil {
		t.Fatal(err)
	}
	loopbackMetadata := &C.Metadata{Type: C.INNER, NetWork: C.TCP}
	if err := loopbackMetadata.SetRemoteAddress(proxy.listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	loopbackMetadata.SrcIP, loopbackMetadata.SrcPort = localIP, uint16(localPort)
	detectorConn, detectorErr := directPostCarrier.DialContext(ctx, loopbackMetadata)
	if !errors.Is(detectorErr, loopback.ErrReject) {
		if detectorConn != nil {
			_ = detectorConn.Close()
		}
		t.Fatalf("DIRECT detector error = %v, want loopback rejection", detectorErr)
	}
	if got := poolState(p, spec.keys[0]); got.HardFailures != 0 || got.Sand != 0 {
		t.Fatalf("successful direct carrier changed post-hop health: hard=%d soft=%v", got.HardFailures, got.Sand)
	}
}

var _ resolver.Resolver = (*directPolicyTestResolver)(nil)
