//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/metacubex/mihomo/adapter"
	mihomoAuth "github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// silentServer accepts connections and reads what they send, but never
// answers: a destination that is black-holed.
func silentServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("silent listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(testTimeout))
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	return l.Addr().String()
}

// A destination that the first node never answers moves, over a real relay, to
// the second node, while another site stays on the first one.
func TestSilentSiteMovesToTheNextNodeOverARealRelay(t *testing.T) {
	old := silentMinLife
	silentMinLife = 100 * time.Millisecond
	t.Cleanup(func() { silentMinLife = old })

	first, firstAddress := startFakeUpstream(t)
	second, secondAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	const blackhole, fine = "blackhole.invalid", "fine.invalid"
	first.mapDomain(blackhole, silentServer(t))
	second.mapDomain(blackhole, backend)
	first.mapDomain(fine, backend)
	second.mapDomain(fine, backend)

	sink := &eventSink{}
	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {nodeMap(firstAddress), nodeMap(secondAddress)}},
		NodeLabels:   map[string][]string{"de": {"first", "second"}},
		NodeTiers:    map[string][]int{"de": {0, 1}},
		AccountRoute: map[string]string{"alice": "de"},
		OnConnEvent:  sink.add,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	// Several connections wait for an answer together and give up together.
	// That alone is no block: the node must be seen answering afterwards.
	var waiting []net.Conn
	for i := 0; i < silentNeeded; i++ {
		c, err := openAccountSOCKS5(t, instance.Addr(), blackhole, backend, "alice", alicePassword)
		if err != nil {
			t.Fatalf("silent connection %d: %v", i, err)
		}
		if _, err := c.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		waiting = append(waiting, c)
	}
	time.Sleep(400 * time.Millisecond)
	for _, c := range waiting {
		_ = c.Close()
	}
	// One more silent connection, while another site is dialed through the
	// first node: the node answered after the first silent end.
	last, err := openAccountSOCKS5(t, instance.Addr(), blackhole, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("last silent connection: %v", err)
	}
	if _, err := last.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tryDialAccountSOCKS5(t, instance.Addr(), fine, backend, "alice", alicePassword); err != nil {
		t.Fatalf("another site: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	_ = last.Close()

	blocked := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnSiteBlocked })
	if blocked.Login != "alice" || blocked.Label != "first" || blocked.Why != MoveNoAnswer || blocked.Block != siteBlockFirst || !strings.HasPrefix(blocked.Target, blackhole+":") {
		t.Fatalf("block = %+v, want alice, first, no answer, %s, a target of %s", blocked, siteBlockFirst, blackhole)
	}

	if err := tryDialAccountSOCKS5(t, instance.Addr(), blackhole, backend, "alice", alicePassword); err != nil {
		t.Fatalf("the blocked site did not get through the second node: %v", err)
	}
	if !recordedHost(second, blackhole) {
		t.Errorf("the second node recorded %v, want a connection to %s", second.recorded(), blackhole)
	}
	moved := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnSiteMoved })
	if moved.From != "first" || moved.Label != "second" || moved.Why != MoveNoAnswer {
		t.Errorf("move = %+v, want first -> second (no answer)", moved)
	}

	if !recordedHost(first, fine) || recordedHost(second, fine) {
		t.Errorf("first recorded %v, second %v: the other site should stay on the first node", first.recorded(), second.recorded())
	}
}

// TestDialTimeoutBlocksOnlyTheSilentSite verifies that real member SOCKS5
// dial timeouts count as unanswered site dials while healthy probes keep the
// node available for other destinations.
func TestDialTimeoutBlocksOnlyTheSilentSite(t *testing.T) {
	blackholeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackhole listen: %v", err)
	}
	defer blackholeListener.Close()
	health := newHealthTable()
	probeDuringDial := func() { health.recordProbe("first", true, time.Millisecond) }
	blackholeDone := make(chan struct{})
	var connsMu sync.Mutex
	var capturedConns []net.Conn
	var handlers sync.WaitGroup
	go func() {
		defer close(blackholeDone)
		for {
			c, acceptErr := blackholeListener.Accept()
			if acceptErr != nil {
				return
			}
			connsMu.Lock()
			capturedConns = append(capturedConns, c)
			connsMu.Unlock()
			handlers.Add(1)
			go func(c net.Conn) {
				defer handlers.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(testTimeout))
				greeting := make([]byte, 3)
				if _, e := io.ReadFull(c, greeting); e != nil {
					return
				}
				_, _ = c.Write([]byte{5, 0})
				request := make([]byte, 5)
				if _, e := io.ReadFull(c, request[:4]); e != nil {
					return
				}
				if request[3] == 3 {
					if _, e := io.ReadFull(c, request[4:5]); e != nil {
						return
					}
					domain := make([]byte, int(request[4])+2)
					if _, e := io.ReadFull(c, domain); e != nil {
						return
					}
				} else if request[3] == 1 {
					if _, e := io.CopyN(io.Discard, c, 6); e != nil {
						return
					}
				} else if request[3] == 4 {
					if _, e := io.CopyN(io.Discard, c, 18); e != nil {
						return
					}
				}
				// Deliberately withhold CONNECT's reply until the dial context expires.
				probeDuringDial()
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = blackholeListener.Close()
		<-blackholeDone
		connsMu.Lock()
		for _, c := range capturedConns {
			_ = c.Close()
		}
		connsMu.Unlock()
		handlers.Wait()
	})

	second, secondAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	const blackhole = "blackhole.invalid"
	second.mapDomain(blackhole, backend)
	firstAddress := blackholeListener.Addr().String()
	first, err := adapter.ParseProxy(map[string]any{"type": "socks5", "name": "first", "server": "127.0.0.1", "port": mustPort(t, firstAddress)})
	if err != nil {
		t.Fatalf("first SOCKS proxy: %v", err)
	}
	secondProxy, err := adapter.ParseProxy(map[string]any{"type": "socks5", "name": "second", "server": "127.0.0.1", "port": mustPort(t, secondAddress)})
	if err != nil {
		t.Fatalf("second SOCKS proxy: %v", err)
	}

	g := newStickyGroup("timeout-group", []C.Proxy{first, secondProxy}, []string{"first", "second"}, newAffinityTable(nil, 0), health, nil)
	health.recordProbe("first", true, time.Millisecond)
	health.recordProbe("second", true, time.Millisecond)
	g.tiers = []int{0, 1}
	g.blocks.minLife = time.Millisecond
	sink := &eventSink{}
	g.events = sink.add

	for i := 0; i < silentNeeded; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, dialErr := g.DialContext(ctx, dialMetadata("alice", blackhole))
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		if dialErr == nil || dialErrKind(dialErr) != "timeout" {
			t.Fatalf("timeout dial %d error = %v, want timeout", i+1, dialErr)
		}
		// The loopback SOCKS server records the probe while this dial waits.
		if health.down("first") {
			t.Fatal("first node marked down after a destination timeout")
		}
	}
	blocked := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnSiteBlocked })
	if blocked.Target != blackhole+":443" || blocked.Port != 443 || blocked.Label != "first" || blocked.Why != MoveNoAnswer {
		t.Fatalf("block = %+v, want first/no-answer for %s:443", blocked, blackhole)
	}
	if got, _ := g.table.peek(affinityKey{login: "alice", site: siteKey(blackhole, netip.Addr{})}); got != 0 {
		t.Fatalf("block unexpectedly rebound affinity to %d before the next dial", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := g.DialContext(ctx, dialMetadata("alice", blackhole))
	if err != nil {
		t.Fatalf("fourth dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(testTimeout))
	payload := []byte("timeout-failover")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != string(payload) {
		t.Fatalf("echo = %q, %v; want %q", echoed, err, payload)
	}
	if !recordedHost(second, blackhole) {
		t.Fatal("fourth dial did not reach the second fake upstream")
	}
	moved := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnSiteMoved })
	if moved.From != "first" || moved.Label != "second" || moved.Why != MoveNoAnswer {
		t.Errorf("fourth dial move = %+v, want first -> second (no answer)", moved)
	}
}

func recordedHost(f *fakeUpstream, host string) bool {
	for _, target := range f.recorded() {
		if strings.HasPrefix(target, host+":") {
			return true
		}
	}
	return false
}
