//go:build integration

package core

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

// recorderTotals returns how many destinations each recorder saw.
func recorderTotals(recorders ...*chainRecorder) []int {
	out := make([]int, len(recorders))
	for i, r := range recorders {
		out[i] = len(r.snapshot())
	}
	return out
}

// The default route (no account) leaves through the global post pool; the
// site stays on the hop it was bound to, under the shared anonymous key.
func TestGlobalPostPoolServesConnectionsWithoutAnAccount(t *testing.T) {
	first, firstAddr := startChainRecorder(t)
	second, secondAddr := startChainRecorder(t)
	backend, _ := countEchoServer(t)
	binder := pool.NewBinder(pool.BinderOptions{})
	instance, err := Start(context.Background(), Options{
		HomeDir:        testutil.TempHome(t),
		GlobalPostPool: []string{"socks5://" + firstAddr, "socks5://" + secondAddr},
		SiteBinder:     binder,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	roundTrip := func() {
		t.Helper()
		c := socks5ConnectNoAuth(t, instance.Addr(), backend)
		defer c.Close()
		echoRoundTrip(t, c, c)
	}
	roundTrip()
	totals := recorderTotals(first, second)
	if totals[0]+totals[1] != 1 {
		t.Fatalf("recorder totals after one connection = %v, want exactly one hop used", totals)
	}
	bound := 0
	if totals[1] == 1 {
		bound = 1
	}
	if _, ok := binder.Get(anonymousBindLogin, "127.0.0.1"); !ok {
		t.Fatal("the site was not bound under the anonymous key")
	}
	roundTrip()
	roundTrip()
	totals = recorderTotals(first, second)
	if totals[bound] != 3 || totals[1-bound] != 0 {
		t.Fatalf("recorder totals after three connections = %v, want all on hop %d", totals, bound)
	}
	snapshot := instance.SnapshotHopPools()
	if len(snapshot) != 2 {
		t.Fatalf("hop pool snapshot has %d entries, want 2", len(snapshot))
	}
	for _, entry := range snapshot {
		if entry.RouteKey != GlobalPoolRoute || entry.Role != "post" {
			t.Fatalf("snapshot entry %+v is not the global post pool", entry)
		}
	}

	// The pool of the default route is edited in place: the instance has no
	// node-backed route, so only the rules and the pool change.
	if err := instance.Reconfigure(Options{GlobalPostPool: []string{"socks5://" + secondAddr, "socks5://" + firstAddr}, SiteBinder: binder}); err != nil {
		t.Fatalf("reorder the global pool: %v", err)
	}
	roundTrip()
	totals = recorderTotals(first, second)
	if totals[bound] != 4 || totals[1-bound] != 0 {
		t.Fatalf("recorder totals after the reorder = %v, want the binding kept on hop %d", totals, bound)
	}
	if err := instance.Reconfigure(Options{}); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Fatalf("removing the global post: %v, want ErrReconfigureUnsupported", err)
	}
}

// A generation whose routes are direct-post-only pools has no node table and
// no poller; it is reconfigured in place through reorder, add and remove of a
// hop, health and bindings follow the hops by identity, and an open
// connection is not interrupted.
func TestReconfigureDirectPostPoolRouteInPlace(t *testing.T) {
	r1, addr1 := startChainRecorder(t)
	r2, addr2 := startChainRecorder(t)
	r3, addr3 := startChainRecorder(t)
	recorders := []*chainRecorder{r1, r2, r3}
	hop1, hop2, hop3 := "socks5://"+addr1, "socks5://"+addr2, "socks5://"+addr3
	backend, _ := countEchoServer(t)
	host, _, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("split backend address: %v", err)
	}
	home := testutil.TempHome(t)
	binder := pool.NewBinder(pool.BinderOptions{})
	options := func(hops ...string) Options {
		return Options{
			HomeDir:       home,
			Authenticator: aliceAuthenticator(),
			AccountRoute:  map[string]string{"alice": "direct"},
			RoutePostPool: map[string][]string{"direct": hops},
			SiteBinder:    binder,
		}
	}
	instance, err := Start(context.Background(), options(hop1, hop2))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if instance.poller != nil || instance.state().table != nil {
		t.Fatal("a direct-post-only generation has a node table or poller")
	}

	held, err := openAccountSOCKS5(t, instance.Addr(), host, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open the held connection: %v", err)
	}
	defer held.Close()
	if err := accountEcho(held); err != nil {
		t.Fatalf("held echo: %v", err)
	}
	totals := recorderTotals(recorders...)
	bound := -1
	for i, n := range totals {
		if n == 1 {
			bound = i
		}
	}
	if bound < 0 || bound > 1 || totals[0]+totals[1]+totals[2] != 1 {
		t.Fatalf("recorder totals = %v, want one request on hop 1 or 2", totals)
	}
	if key, ok := binder.Get("alice", "127.0.0.1"); !ok || key != hopPoolKey([]string{hop1, hop2}[bound]) {
		t.Fatalf("binding = %q, %v, want the hop that served the connection", key, ok)
	}

	roundTrip := func(want []int) {
		t.Helper()
		c, err := openAccountSOCKS5(t, instance.Addr(), host, backend, "alice", alicePassword)
		if err != nil {
			t.Fatalf("open connection: %v", err)
		}
		defer c.Close()
		if err := accountEcho(c); err != nil {
			t.Fatalf("echo: %v", err)
		}
		if got := recorderTotals(recorders...); got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("recorder totals = %v, want %v", got, want)
		}
	}
	// With the tests above: one used hop, the others idle.
	next := func(extra int) []int {
		out := make([]int, 3)
		out[bound] = 1 + extra
		return out
	}

	// Reorder: the binding keeps the site on its hop.
	if err := instance.Reconfigure(options(hop2, hop1)); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held echo after the reorder: %v", err)
	}
	roundTrip(next(1))

	// Add a hop: same.
	if err := instance.Reconfigure(options(hop2, hop1, hop3)); err != nil {
		t.Fatalf("add: %v", err)
	}
	roundTrip(next(2))
	if got := len(instance.SnapshotHopPools()); got != 3 {
		t.Fatalf("snapshot after the add has %d entries, want 3", got)
	}

	// Remove the bound hop: its bindings are forgotten, the site moves on.
	remaining := []string{hop1, hop2}
	remaining = append(remaining[:bound], remaining[bound+1:]...)
	remaining = append(remaining, hop3)
	if err := instance.Reconfigure(options(remaining...)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := binder.Get("alice", "127.0.0.1"); ok {
		t.Fatal("the binding of the removed hop survived")
	}
	c, err := openAccountSOCKS5(t, instance.Addr(), host, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open connection after the removal: %v", err)
	}
	if err := accountEcho(c); err != nil {
		_ = c.Close()
		t.Fatalf("echo after the removal: %v", err)
	}
	_ = c.Close()
	after := recorderTotals(recorders...)
	if after[bound] != 3 {
		t.Fatalf("the removed hop saw more traffic: totals = %v", after)
	}
	if sum := after[0] + after[1] + after[2]; sum != 4 {
		t.Fatalf("recorder totals after the removal = %v, want 4 requests in all", after)
	}
	if got := len(instance.SnapshotHopPools()); got != 2 {
		t.Fatalf("snapshot after the removal has %d entries, want 2", got)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held echo after the removal: %v", err)
	}

	// A node-backed route needs a node table: refused, nothing changes.
	routed := options(remaining...)
	routed.AccountRoute = map[string]string{"alice": "routed"}
	routed.RouteNodes = map[string][]map[string]any{"routed": {nodeMap(addr1)}}
	if err := instance.Reconfigure(routed); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Fatalf("a node-backed route: %v, want ErrReconfigureUnsupported", err)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held echo after the refused change: %v", err)
	}
}
