//go:build integration

package core

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// Start installs the initial bindings for real: a record for a member of the
// login's route keeps that member even though a fresh pick (the first tier)
// would choose the other node, records with an unknown label are not
// installed, and RestoredBindings counts only what was installed.
func TestStartInstallsInitialBindings(t *testing.T) {
	first, firstAddress := startFakeUpstream(t)
	second, secondAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	now := time.Now()
	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
		}),
		RouteNodes:   map[string][]map[string]any{"de": {nodeMap(firstAddress), nodeMap(secondAddress)}},
		NodeLabels:   map[string][]string{"de": {"p/first", "p/second"}},
		NodeTiers:    map[string][]int{"de": {0, 1}},
		AccountRoute: map[string]string{"alice": "de"},
		InitialBindings: []BindingRecord{
			{Login: "alice", Site: "bound.invalid", Label: "p/second",
				BoundAt: now.Add(-time.Hour), LastUsed: now.Add(-time.Minute)},
			// Unknown label: ignored.
			{Login: "alice", Site: "unknown.invalid", Label: "p/missing", BoundAt: now, LastUsed: now},
		},
	})
	if err != nil {
		t.Fatalf("Start with initial bindings: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if got := instance.RestoredBindings(); got != 1 {
		t.Fatalf("RestoredBindings = %d, want 1 (the unknown label is not counted)", got)
	}

	// The bound site leaves through the second node although the fresh pick
	// of a new site goes to the first-tier node.
	// The upstream fixture records every CONNECT destination exactly as the
	// client sent it, as host:port (node_integration_test.go), so the two
	// sites are compared in that shape.
	_, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("parse backend address: %v", err)
	}
	if _, err := strconv.Atoi(backendPortText); err != nil {
		t.Fatalf("parse backend port: %v", err)
	}
	wantBound := net.JoinHostPort("bound.invalid", backendPortText)
	wantFresh := net.JoinHostPort("fresh.invalid", backendPortText)

	first.mapDomain("bound.invalid", backend)
	second.mapDomain("bound.invalid", backend)
	if err := tryDialAccountSOCKS5(t, instance.Addr(), "bound.invalid", backend, "alice", alicePassword); err != nil {
		t.Fatalf("bound site: %v", err)
	}
	first.mapDomain("fresh.invalid", backend)
	second.mapDomain("fresh.invalid", backend)
	if err := tryDialAccountSOCKS5(t, instance.Addr(), "fresh.invalid", backend, "alice", alicePassword); err != nil {
		t.Fatalf("fresh site: %v", err)
	}
	// Per upstream: the stored binding names p/second, so only the second
	// upstream must have seen the bound site; the fresh site goes to the
	// first-tier node.
	saw := func(f *fakeUpstream, dest string) int {
		n := 0
		for _, got := range f.recorded() {
			if got == dest {
				n++
			}
		}
		return n
	}
	if n := saw(first, wantBound); n != 0 {
		t.Errorf("the first upstream saw the bound site %d times, want 0 (the binding names p/second)", n)
	}
	if n := saw(second, wantBound); n != 1 {
		t.Errorf("the second upstream saw the bound site %d times, want exactly 1", n)
	}
	if n := saw(first, wantFresh); n != 1 {
		t.Errorf("the first upstream saw the fresh site %d times, want exactly 1 (first-tier fresh pick)", n)
	}
	if n := saw(second, wantFresh); n != 0 {
		t.Errorf("the second upstream saw the fresh site %d times, want 0", n)
	}
}
