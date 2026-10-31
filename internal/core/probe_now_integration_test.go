//go:build integration

package core

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// ProbeNow on a running instance makes the probe loop run a round at once: the
// interval here is an hour, so only the first round and the asked one happen.
func TestInstanceProbeNowProbesAtOnce(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddress := reserved.Addr().String()
	_ = reserved.Close() // a node that refuses: the probes fail fast, which still counts as probes

	instance, err := Start(context.Background(), Options{
		HomeDir:       testutil.TempHome(t),
		Authenticator: aliceAuthenticator(),
		RouteNodes:    map[string][]map[string]any{"de": {nodeMap(deadAddress)}},
		NodeLabels:    map[string][]string{"de": {"dead"}},
		AccountRoute:  map[string]string{"alice": "de"},
		Warm:          &WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	waitProbes := func(atLeast uint64) {
		t.Helper()
		deadline := time.Now().Add(testTimeout)
		for time.Now().Before(deadline) {
			if ws, ok := instance.WarmStats(); ok && ws.Probes >= atLeast {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		ws, _ := instance.WarmStats()
		t.Fatalf("%d probes, want at least %d", ws.Probes, atLeast)
	}
	waitProbes(1) // the round at start
	ws, _ := instance.WarmStats()
	before := ws.Probes
	instance.ProbeNow()
	waitProbes(before + 1)
}

// Without probes configured ProbeNow does nothing, and does not fail.
func TestInstanceProbeNowWithoutWarmOptions(t *testing.T) {
	instance := startInstance(t, aliceAuthenticator())
	instance.ProbeNow()
	if _, ok := instance.WarmStats(); ok {
		t.Error("an instance without warm options reports probes")
	}
}
