//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

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

	// Several connections wait for an answer together; a later dial of the
	// first node shows it was reachable while they waited.
	var waiting []net.Conn
	for i := 0; i < silentNeeded+1; i++ {
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

	if err := tryDialAccountSOCKS5(t, instance.Addr(), fine, backend, "alice", alicePassword); err != nil {
		t.Fatalf("another site: %v", err)
	}
	if !recordedHost(first, fine) || recordedHost(second, fine) {
		t.Errorf("first recorded %v, second %v: the other site should stay on the first node", first.recorded(), second.recorded())
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
