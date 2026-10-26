//go:build integration

package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// healthEvents records OnNodeHealth calls.
type healthEvents struct {
	mu     sync.Mutex
	events []string
}

func (h *healthEvents) record(label string, down bool) {
	h.mu.Lock()
	h.events = append(h.events, fmt.Sprintf("%s %v", label, down))
	h.mu.Unlock()
}

func (h *healthEvents) list() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

func aliceAuthenticator() mihomoAuth.Authenticator {
	return mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{{User: "alice", Pass: alicePassword}})
}

// accountActivityLabels returns the node label per open connection of login.
func accountActivityLabels(instance *Instance, login string) map[string]int {
	out := map[string]int{}
	for _, a := range instance.Activity() {
		if a.Login != login {
			continue
		}
		for _, n := range a.Nodes {
			out[n.Label] += n.Connections
		}
	}
	return out
}

// An open connection survives two Reconfigures and keeps its node label; the
// site binding follows its node to the new index; once the node leaves the
// route, new connections go to the new node.
func TestReconfigureKeepsOpenConnections(t *testing.T) {
	first, firstAddress := startFakeUpstream(t)
	second, secondAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	domain := "reconfigure.invalid"
	first.mapDomain(domain, backend)
	second.mapDomain(domain, backend)
	target := net.JoinHostPort(domain, mustPort(t, backend))
	events := &healthEvents{}
	options := func(nodes []map[string]any, labels []string) Options {
		return Options{
			HomeDir:       testutil.TempHome(t),
			Authenticator: aliceAuthenticator(),
			RouteNodes:    map[string][]map[string]any{"de": nodes},
			NodeLabels:    map[string][]string{"de": labels},
			AccountRoute:  map[string]string{"alice": "de"},
			OnNodeHealth:  events.record,
		}
	}

	instance, err := Start(context.Background(), options([]map[string]any{nodeMap(firstAddress)}, []string{"p/first"}))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	address := instance.Addr()

	held, err := openAccountSOCKS5(t, address, domain, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open the held connection: %v", err)
	}
	defer held.Close()
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection before the change: %v", err)
	}

	// Step 1: a second node in front; the site stays on p/first, whose health
	// (and binding, once it is no longer provisional) is carried over.
	if err := instance.Reconfigure(options(
		[]map[string]any{nodeMap(secondAddress), nodeMap(firstAddress)},
		[]string{"p/second", "p/first"},
	)); err != nil {
		t.Fatalf("Reconfigure 1: %v", err)
	}
	if instance.Addr() != address {
		t.Fatalf("listener moved from %s to %s", address, instance.Addr())
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection after Reconfigure 1: %v", err)
	}
	if got := accountActivityLabels(instance, "alice"); got["p/first"] != 1 {
		t.Errorf("activity after Reconfigure 1 = %v, want the held connection on p/first", got)
	}
	// Closed connections leave the tracker list a moment later; the counts
	// below include at least the held one.
	if err := tryDialAccountSOCKS5(t, address, domain, backend, "alice", alicePassword); err != nil {
		t.Fatalf("new connection after Reconfigure 1: %v", err)
	}
	if got := len(first.recorded()); got != 2 {
		t.Errorf("p/first saw %d connections, want 2: its state was not carried over", got)
	}
	if got := second.recorded(); len(got) != 0 {
		t.Errorf("p/second saw %v, want nothing yet", got)
	}
	if got := instance.AccountRoutes(); got["alice"] != "de" {
		t.Errorf("AccountRoutes = %v", got)
	}

	// Step 2: p/first leaves the route; the held connection still runs on it.
	if err := instance.Reconfigure(options([]map[string]any{nodeMap(secondAddress)}, []string{"p/second"})); err != nil {
		t.Fatalf("Reconfigure 2: %v", err)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection after Reconfigure 2: %v", err)
	}
	if got := accountActivityLabels(instance, "alice"); got["p/first"] < 1 || len(got) > 2 {
		t.Errorf("activity after Reconfigure 2 = %v, want the held connection still on p/first", got)
	}
	if err := tryDialAccountSOCKS5(t, address, domain, backend, "alice", alicePassword); err != nil {
		t.Fatalf("new connection after Reconfigure 2: %v", err)
	}
	if got := second.recorded(); len(got) != 1 || got[0] != target {
		t.Errorf("p/second saw %v, want [%s]", got, target)
	}
	if ws, ok := instance.WarmStats(); ok {
		t.Errorf("an instance without warm options reports probes: %+v", ws)
	}
	if got := events.list(); len(got) != 0 {
		t.Errorf("health events %v, want none", got)
	}
}

// A node that went down stays down across a Reconfigure, without a second
// report, and new sites never try it: with fresh health it would get about
// a third of the new sites (no node has delay data, so the pick is a hash).
func TestReconfigureKeepsNodeHealth(t *testing.T) {
	live, liveAddress := startFakeUpstream(t)
	spare, spareAddress := startFakeUpstream(t)
	backend, _ := countEchoServer(t)
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead upstream address: %v", err)
	}
	deadAddress := deadListener.Addr().String()
	if err := deadListener.Close(); err != nil {
		t.Fatalf("close reserved upstream: %v", err)
	}
	events := &healthEvents{}
	options := func(nodes []map[string]any, labels []string) Options {
		return Options{
			HomeDir:       testutil.TempHome(t),
			Authenticator: aliceAuthenticator(),
			RouteNodes:    map[string][]map[string]any{"de": nodes},
			NodeLabels:    map[string][]string{"de": labels},
			AccountRoute:  map[string]string{"alice": "de"},
			OnNodeHealth:  events.record,
		}
	}
	// Only the dead node at first, so every dial fails on it until it is down.
	instance, err := Start(context.Background(), options([]map[string]any{deadNodeMap(deadAddress)}, []string{"p/dead"}))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	down := func() bool {
		for _, e := range events.list() {
			if e == "p/dead true" {
				return true
			}
		}
		return false
	}
	for attempt := 0; attempt < 10 && !down(); attempt++ {
		_ = tryDialAccountSOCKS5(t, instance.Addr(), fmt.Sprintf("site%d.invalid", attempt), backend, "alice", alicePassword)
	}
	if !down() {
		t.Fatalf("p/dead never went down: %v", events.list())
	}
	before := events.list()

	if err := instance.Reconfigure(options(
		[]map[string]any{deadNodeMap(deadAddress), nodeMap(liveAddress), nodeMap(spareAddress)},
		[]string{"p/dead", "p/live", "p/spare"},
	)); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		domain := fmt.Sprintf("after%d.invalid", attempt)
		live.mapDomain(domain, backend)
		spare.mapDomain(domain, backend)
		if err := tryDialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword); err != nil {
			t.Fatalf("new site %d after Reconfigure: %v (the down node was tried)", attempt, err)
		}
	}
	if after := events.list(); len(after) != len(before) {
		t.Errorf("health events after Reconfigure = %v, want still %v", after, before)
	}
}

// An instance started without account routes, or one that routes through a
// default node, cannot take routes in place; a closed one refuses too.
func TestReconfigureNeedsAccountRoutes(t *testing.T) {
	_, upstreamAddress := startFakeUpstream(t)
	routes := Options{
		RouteNodes:   map[string][]map[string]any{"de": {nodeMap(upstreamAddress)}},
		AccountRoute: map[string]string{"alice": "de"},
	}

	direct, err := Start(context.Background(), Options{HomeDir: testutil.TempHome(t), Authenticator: aliceAuthenticator()})
	if err != nil {
		t.Fatalf("Start direct: %v", err)
	}
	if err := direct.Reconfigure(routes); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("direct instance: %v, want ErrReconfigureUnsupported", err)
	}
	_ = direct.Close()

	node := routes
	node.HomeDir, node.Authenticator, node.Node = testutil.TempHome(t), aliceAuthenticator(), nodeMap(upstreamAddress)
	withNode, err := Start(context.Background(), node)
	if err != nil {
		t.Fatalf("Start with a node: %v", err)
	}
	if err := withNode.Reconfigure(routes); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("instance with a default node: %v, want ErrReconfigureUnsupported", err)
	}
	_ = withNode.Close()
	if err := withNode.Reconfigure(routes); !errors.Is(err, errInstanceClosed) {
		t.Errorf("closed instance: %v, want errInstanceClosed", err)
	}
}
