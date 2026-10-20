//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// stallFirstUpstream wraps a fake upstream: the first accepted connection is
// answered up to the SOCKS5 request and then stalled forever (never
// answered), like a hung transport handshake. Later connections are served
// normally by the wrapped upstream.
type stallFirstUpstream struct {
	*fakeUpstream
	accepted atomic.Int32
}

// startStallFirstUpstream runs the combined upstream on 127.0.0.1:0.
func startStallFirstUpstream(t *testing.T) (*stallFirstUpstream, string) {
	t.Helper()
	f, _ := startFakeUpstream(t)
	stall := &stallFirstUpstream{fakeUpstream: f}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stall upstream listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			if stall.accepted.Add(1) == 1 {
				go stallConn(c)
				continue
			}
			go f.serve(c)
		}
	}()
	return stall, listener.Addr().String()
}

// stallConn completes the no-auth SOCKS5 greeting and reads the CONNECT
// request, then never answers; it unblocks when the client disappears.
func stallConn(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * testTimeout))
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	if head[0] != 5 {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, int(head[1]))); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	switch req[3] {
	case 1:
		_, _ = io.ReadFull(c, make([]byte, 6))
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(c, length); err != nil {
			return
		}
		_, _ = io.ReadFull(c, make([]byte, int(length[0])+2))
	case 4:
		_, _ = io.ReadFull(c, make([]byte, 18))
	}
	// Stalled: wait until the cancelled attempt is closed underneath us.
	buf := make([]byte, 512)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}

// TestHedgedDialRecoversStalledUpstream: the single route node stalls the
// first connection like a hung handshake; the hedge dial to the same node
// completes the proxied request well before the 5 s tunnel timeout, and the
// hedge counters record it. Socks5 is not hedgeable by default, so the test
// enables it through the eligibility seam (the unit tests cover the
// default-only-VLESS policy).
func TestHedgedDialRecoversStalledUpstream(t *testing.T) {
	previous := hedgeEligible
	hedgeEligible = func(t C.AdapterType) bool { return t == C.Socks5 }
	t.Cleanup(func() { hedgeEligible = previous })

	upstream, upstreamAddress := startStallFirstUpstream(t)
	backend, _ := countEchoServer(t)
	domain := "hedge-stall.invalid"
	upstream.mapDomain(domain, backend)

	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes: map[string][]map[string]any{
			"de": {nodeMap(upstreamAddress)},
		},
		AccountRoute: map[string]string{"alice": "de"},
	})
	if err != nil {
		t.Fatalf("Start hedge instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	// The first (stalled) attempt is hedged after the 1 s default threshold
	// (no dial EWMA yet); the second connection is served normally, so the
	// full SOCKS5 round trip succeeds far below the 10 s client deadline.
	dialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)

	stats := instance.AffinityStats()
	if stats.Hedges < 1 {
		t.Fatalf("AffinityStats().Hedges = %d, want at least 1", stats.Hedges)
	}
	if stats.HedgeWins < 1 {
		t.Fatalf("AffinityStats().HedgeWins = %d, want at least 1", stats.HedgeWins)
	}
}
