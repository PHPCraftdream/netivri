package core

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestReconfigureCommitFailureClosesUnpublishedWarmer(t *testing.T) {
	inst, oldState, protocol, serverSet, server := newReconfigureWarmFixture(t)
	started, stopped, warmRef := installBlockingReconfigureWarmer(t)

	failedServer := server.Clone()
	failedServer.Params["revision"] = "fail"
	protocol.mu.Lock()
	protocol.failRevision = "fail"
	sentinel := protocol.sentinel
	protocol.mu.Unlock()

	err := inst.Reconfigure(reconfigureWarmOptions(failedServer))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Reconfigure error = %v, want injected server commit failure", err)
	}
	warm := warmRef()
	waitReconfigureWarmSignal(t, started, "replacement probe start")
	waitReconfigureWarmSignal(t, stopped, "replacement probe stop after commit failure")
	waitReconfigureWarmSignal(t, warm.done, "replacement warmer exit after commit failure")
	if inst.state() != oldState || inst.state().gen != 0 || inst.AccountRoutes()["alice"] != "k" {
		t.Fatal("failed Reconfigure published replacement routing state")
	}
	serverSet.mu.RLock()
	gotRevision := serverSet.items[server.Name].spec.Params["revision"]
	serverSet.mu.RUnlock()
	if gotRevision != "old" {
		t.Errorf("server revision after failed commit = %q, want old", gotRevision)
	}
}

func TestReconfigureCommitPublishesLiveWarmer(t *testing.T) {
	inst, oldState, _, _, server := newReconfigureWarmFixture(t)
	started, stopped, warmRef := installBlockingReconfigureWarmer(t)

	if err := inst.Reconfigure(reconfigureWarmOptions(server)); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	warm := warmRef()
	waitReconfigureWarmSignal(t, started, "replacement probe start")
	if state := inst.state(); state == oldState || state.warm != warm || state.gen != 1 {
		t.Fatalf("published routing state = %#v, want generation 1 with replacement warmer", state)
	}
	select {
	case <-warm.done:
		t.Fatal("published replacement warmer stopped before Close")
	default:
	}
	select {
	case <-stopped:
		t.Fatal("published replacement probe stopped before Close")
	default:
	}

	warm.Close()
	waitReconfigureWarmSignal(t, stopped, "replacement probe stop on Close")
	waitReconfigureWarmSignal(t, warm.done, "replacement warmer exit on Close")
}

func newReconfigureWarmFixture(t *testing.T) (*Instance, *routingState, *serverTestProtocol, *serverSet, servers.Spec) {
	t.Helper()

	protocol := newServerTestProtocol(true)
	serverSet := testServerSet()
	t.Cleanup(serverSet.close)
	port := reconfigureWarmTestPort(t)
	server := testServerSpec("entry", protocol.Name(), port)
	server.Params["revision"] = "old"
	tx, err := serverSet.prepare([]servers.Spec{server}, nil, nil, nil)
	if err != nil {
		t.Fatal("prepare initial UDP server:", err)
	}
	if err := tx.commit(); err != nil {
		t.Fatal("commit initial UDP server:", err)
	}

	previousProxies := tunnel.Proxies()
	previousProviders := tunnel.Providers()
	previousRules := tunnel.Rules()
	previousRuleProviders := tunnel.RuleProviders()
	t.Cleanup(func() {
		tunnel.UpdateProxies(previousProxies, previousProviders)
		tunnel.UpdateRules(previousRules, nil, previousRuleProviders)
	})
	lifecycleMu.Lock()
	if current != nil {
		lifecycleMu.Unlock()
		t.Fatal("cannot install Reconfigure test instance while another instance is current")
	}
	lifecycleMu.Unlock()
	tunnel.UpdateProxies(map[string]C.Proxy{
		"DIRECT": testWarmProxy{name: "DIRECT", typ: C.Direct},
	}, previousProviders)

	oldOptions := Options{
		RouteNodes:   map[string][]map[string]any{"k": {testNode("192.0.2.1")}},
		AccountRoute: map[string]string{"alice": "k"},
	}
	oldPlans, err := buildRoutePlansGen(oldOptions, 0)
	if err != nil {
		t.Fatal("build initial route plans:", err)
	}
	oldHealth := carryHealth(nil, planLabels(oldPlans), nil)
	oldLabels := memberNameLabels(oldPlans)
	oldState := &routingState{
		accountRoutes: copyAccountRoutes(oldOptions.AccountRoute),
		table:         newAffinityTable(nil, 0),
		health:        oldHealth,
		memberLabels:  oldLabels,
		connLabels:    oldLabels,
		routeLabels:   routeLabels(oldPlans),
		names:         planProxyNames(oldPlans),
	}
	inst := &Instance{
		route:   routeDirect,
		servers: serverSet,
		poller:  newThroughputPoller(nil, oldHealth, nil, oldLabels, nil),
	}
	inst.routing.Store(oldState)

	lifecycleMu.Lock()
	previousCurrent := current
	current = inst
	lifecycleMu.Unlock()

	t.Cleanup(func() {
		inst.poller.cancel()
		lifecycleMu.Lock()
		current = previousCurrent
		lifecycleMu.Unlock()
	})
	return inst, oldState, protocol, serverSet, server
}

func installBlockingReconfigureWarmer(t *testing.T) (started, stopped <-chan struct{}, result func() *warmer) {
	t.Helper()
	entered := make(chan struct{})
	stoppedChannel := make(chan struct{})
	previous := startWarmForReconfigure
	var warm *warmer
	startWarmForReconfigure = func(options WarmOptions, node map[string]any) (*warmer, error) {
		clock := runWarmClock{
			testWarmClock: testWarmClock{now: time.Unix(100, 0)},
			ticker:        &runWarmTicker{ticks: make(chan time.Time)},
		}
		created, err := startWarmWith(options, node, clock, func(ctx context.Context, _ C.Proxy, _ string) (uint16, error) {
			close(entered)
			<-ctx.Done()
			close(stoppedChannel)
			return 0, ctx.Err()
		})
		if err != nil {
			return nil, err
		}
		warm = created
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		select {
		case <-entered:
		case <-timeout.C:
			created.Close()
			return nil, errors.New("replacement probe did not start")
		}
		return created, nil
	}
	t.Cleanup(func() {
		startWarmForReconfigure = previous
		if warm != nil {
			warm.Close()
		}
	})
	return entered, stoppedChannel, func() *warmer { return warm }
}

func waitReconfigureWarmSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func reconfigureWarmOptions(server servers.Spec) Options {
	return Options{
		Servers:      []servers.Spec{server},
		RouteNodes:   map[string][]map[string]any{"k": {testNode("192.0.2.2")}},
		AccountRoute: map[string]string{"alice": "k"},
		Warm: &WarmOptions{
			ProbeInterval: time.Hour,
			ProbeTimeout:  24 * time.Hour,
			ProbeURL:      "http://probe.invalid/",
		},
	}
}

func reconfigureWarmTestPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve dynamic fake UDP port:", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if err := conn.Close(); err != nil {
		t.Fatal("release dynamic fake UDP port:", err)
	}
	return port
}
