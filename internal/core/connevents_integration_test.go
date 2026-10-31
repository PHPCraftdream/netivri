//go:build integration

package core

import (
	"context"
	"net"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func waitEvent(t *testing.T, sink *eventSink, match func(ConnEvent) bool) ConnEvent {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		for _, ev := range sink.all() {
			if match(ev) {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no matching event within %v: %+v", testTimeout, sink.all())
	return ConnEvent{}
}

// With the sink set, the real core wraps the connections of the node: the
// data still makes the round trip through Mihomo's relay (no panic, no lost
// bytes) and the end of the connection is reported with the bytes that moved.
func TestConnEventsOverARealRelay(t *testing.T) {
	live, liveAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	sink := &eventSink{}
	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {nodeMap(liveAddress)}},
		NodeLabels:   map[string][]string{"de": {"live"}},
		AccountRoute: map[string]string{"alice": "de"},
		OnConnEvent:  sink.add,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	domain := "events.invalid"
	live.mapDomain(domain, backend)
	for i := 0; i < 3; i++ {
		connection, err := openAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		if err := accountEcho(connection); err != nil {
			t.Fatalf("echo %d through the wrapped connection: %v", i, err)
		}
		_ = connection.Close()
	}

	ev := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnStreamEnded && ev.Label == "live" })
	if ev.Login != "alice" {
		t.Errorf("login = %q, want alice", ev.Login)
	}
	if ev.Up == 0 || ev.Down == 0 {
		t.Errorf("up %d down %d, want the echoed bytes counted both ways", ev.Up, ev.Down)
	}
	if ev.Why != "" {
		t.Errorf("a clean echo ended with %q", ev.Why)
	}
	if ev.Port == 0 || ev.Site == 0 {
		t.Errorf("port %d site %d, want the destination of the connection", ev.Port, ev.Site)
	}
	if ev.Answer <= 0 || ev.Answer > testTimeout {
		t.Errorf("answer %v, want the time the echo took to start coming back", ev.Answer)
	}
	for _, ev := range sink.all() {
		if ev.Kind == ConnDialFailed {
			t.Errorf("a dial failure was reported for a working node: %+v", ev)
		}
	}
}

// A dead node: its failed dials are reported with a fixed kind, and when it is
// marked down the site moves to the live node, which is reported too.
func TestConnEventsForADeadNodeAndTheFailover(t *testing.T) {
	live, liveAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddress := deadListener.Addr().String()
	_ = deadListener.Close()

	sink := &eventSink{}
	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {deadNodeMap(deadAddress), nodeMap(liveAddress)}},
		NodeLabels:   map[string][]string{"de": {"dead", "live"}},
		AccountRoute: map[string]string{"alice": "de"},
		OnConnEvent:  sink.add,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	domain := "failover-events.invalid"
	live.mapDomain(domain, backend)
	succeeded := false
	for attempt := 0; attempt < 12 && !succeeded; attempt++ {
		succeeded = tryDialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword) == nil
	}
	if !succeeded {
		t.Fatal("no connection succeeded through the live node")
	}

	failed := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnDialFailed && ev.Label == "dead" })
	if failed.Login != "alice" || failed.Why == "" {
		t.Errorf("dial failure = %+v, want alice via dead with a kind", failed)
	}
	moved := waitEvent(t, sink, func(ev ConnEvent) bool { return ev.Kind == ConnSiteMoved })
	if moved.From != "dead" || moved.Label != "live" || moved.Why != MoveNodeDown || moved.Login != "alice" {
		t.Errorf("move = %+v, want alice from dead to live (node down)", moved)
	}
}
