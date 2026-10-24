//go:build integration

package core

import (
	"context"
	"fmt"
	"net"
	"testing"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// With every node alive the first tier gets all the traffic of every site: a
// route without tiers would spread these sites over both nodes.
func TestTierFirstTierTakesAllSites(t *testing.T) {
	first, firstAddress := startFakeUpstream(t)
	second, secondAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)

	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {nodeMap(firstAddress), nodeMap(secondAddress)}},
		NodeTiers:    map[string][]int{"de": {0, 1}},
		AccountRoute: map[string]string{"alice": "de"},
	})
	if err != nil {
		t.Fatalf("Start tiered instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	const sites = 12
	for i := 0; i < sites; i++ {
		domain := fmt.Sprintf("tier-%d.invalid", i)
		first.mapDomain(domain, backend)
		second.mapDomain(domain, backend)
		if err := tryDialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword); err != nil {
			t.Fatalf("site %d: %v", i, err)
		}
	}
	if got := len(first.recorded()); got != sites {
		t.Fatalf("first tier served %d of %d sites", got, sites)
	}
	if got := len(second.recorded()); got != 0 {
		t.Fatalf("second tier served %d sites while the first tier was alive", got)
	}
}

// The first tier is dead: after the dead node is marked down every site
// leaves through the second tier, and nothing falls back to a direct dial.
func TestTierDeadFirstTierFallsBackToSecond(t *testing.T) {
	second, secondAddress := startFakeUpstream(t)
	backend, backendConnections := countEchoServer(t)
	// The dead address is reserved after every live listener exists.
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead upstream address: %v", err)
	}
	deadAddress := deadListener.Addr().String()
	if err := deadListener.Close(); err != nil {
		t.Fatalf("close reserved upstream: %v", err)
	}

	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {deadNodeMap(deadAddress), nodeMap(secondAddress)}},
		NodeTiers:    map[string][]int{"de": {0, 1}},
		AccountRoute: map[string]string{"alice": "de"},
	})
	if err != nil {
		t.Fatalf("Start tiered instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	// Dials of different sites to the dead node fail until it is marked down
	// (3 failures), then the failing dial is retried once on the second tier.
	succeeded := 0
	for i := 0; i < 8; i++ {
		domain := fmt.Sprintf("tier-down-%d.invalid", i)
		second.mapDomain(domain, backend)
		if tryDialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword) == nil {
			succeeded++
		}
	}
	if succeeded < 5 {
		t.Fatalf("only %d of 8 sites got through the second tier", succeeded)
	}
	// Once the first tier is known to be down, new sites go straight to the
	// second tier and succeed.
	for i := 0; i < 4; i++ {
		domain := fmt.Sprintf("tier-after-%d.invalid", i)
		second.mapDomain(domain, backend)
		if err := tryDialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword); err != nil {
			t.Fatalf("site after the first tier went down: %v", err)
		}
	}
	if got := len(second.recorded()); got != succeeded+4 {
		t.Fatalf("second tier served %d connections, want %d", got, succeeded+4)
	}
	if got := backendConnections.Load(); got != int32(succeeded+4) {
		t.Fatalf("the backend saw %d connections, want %d through the second tier only", got, succeeded+4)
	}
}
