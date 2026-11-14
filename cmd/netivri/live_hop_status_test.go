package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

type liveStatusHopCore struct {
	*fakeCore
	routes    map[string]string
	snapshots []core.HopPoolSnapshot
	exported  []core.HopPoolState
	now       time.Time
}

func (f *liveStatusHopCore) AccountRoutes() map[string]string { return f.routes }
func (f *liveStatusHopCore) SnapshotHopPools() []core.HopPoolSnapshot {
	return append([]core.HopPoolSnapshot(nil), f.snapshots...)
}
func (f *liveStatusHopCore) ExportHopPools() []core.HopPoolState {
	out := append([]core.HopPoolState(nil), f.exported...)
	for i := range out {
		out[i].Entries = append([]hoppool.Entry(nil), out[i].Entries...)
	}
	return out
}
func (f *liveStatusHopCore) HopStatusNow() time.Time { return f.now }

// statusUnsafeHopKey stands for a malformed key that must never be printed.
const statusUnsafeHopKey = "socks5://user:do-not-print@hop-secret.example:1080"

func statusPoolID(char byte) string { return strings.Repeat(string(char), 64) }

func statusPoolSnapshot(role, route, identity, key, state string, trips int) core.HopPoolSnapshot {
	return core.HopPoolSnapshot{Role: role, RouteKey: route, PoolIdentity: identity, Key: key, State: state, Trips: trips}
}

func newLiveHopStatusFixture(t *testing.T) (*liveAngel, *liveStatusHopCore, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	routeA, routeB := "provider~DE~route-a", "provider~NL~route-b"
	identityA, identityB, identityC, identityD := statusPoolID('a'), statusPoolID('b'), statusPoolID('c'), statusPoolID('d')
	keyA, keyB, keyC, keyD, keyE := statusPoolID('1'), statusPoolID('2'), statusPoolID('3'), statusPoolID('4'), statusPoolID('5')
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Login: "alice", Password: config.InitPlaceholder},
		{Login: "eve", Password: config.InitPlaceholder},
		{Login: "bob", Password: config.InitPlaceholder},
		{Login: "single", Password: config.InitPlaceholder},
	}
	base := &fakeCore{addr: "127.0.0.1:12345"}
	inst := &liveStatusHopCore{
		fakeCore: base,
		routes:   map[string]string{"alice": routeA, "eve": routeA, "bob": routeB, "single": "provider~FR~single"},
		now:      now,
		snapshots: []core.HopPoolSnapshot{
			statusPoolSnapshot("pre", routeA, identityA, keyA, "healthy", 0),
			statusPoolSnapshot("pre", routeA, identityA, keyB, "quarantined", 2),
			statusPoolSnapshot("pre", routeA, identityA, keyE, "probing", 3),
			statusPoolSnapshot("pre", routeA, identityA, statusUnsafeHopKey, "probing", 3),
			statusPoolSnapshot("pre", routeA, identityB, keyC, "quarantined", 1),
			statusPoolSnapshot("pre", routeA, identityB, keyD, "quarantined", 2),
			statusPoolSnapshot("pre", routeA, identityA, keyA, "healthy", 0),
			statusPoolSnapshot("post", routeA, identityC, keyA, "quarantined", 1),
			statusPoolSnapshot("post", routeA, identityC, keyB, "healthy", 0),
			statusPoolSnapshot("pre", routeB, identityD, keyA, "healthy", 0),
			statusPoolSnapshot("pre", routeB, identityD, keyB, "healthy", 0),
			statusPoolSnapshot("pre", "provider~FR~single", statusPoolID('e'), keyB, "healthy", 0),
		},
		exported: []core.HopPoolState{
			{Role: "pre", RouteKey: routeA, PoolIdentity: identityA, Entries: []hoppool.Entry{
				{Key: keyB, Trips: 2, NextProbe: now.Add(1250 * time.Millisecond)},
				{Key: keyE, Trips: 3, NextProbe: now},
				{Key: statusUnsafeHopKey, Trips: 3, NextProbe: now.Add(-time.Second)},
				{Key: keyC, Trips: 99, NextProbe: now.Add(time.Hour)},
			}},
			{Role: "pre", RouteKey: routeA, PoolIdentity: identityB, Entries: []hoppool.Entry{
				{Key: keyC, Trips: 1, NextProbe: now.Add(30 * time.Second)},
				{Key: keyD, Trips: 2, NextProbe: now.Add(60 * time.Second)},
			}},
			{Role: "post", RouteKey: routeA, PoolIdentity: identityC, Entries: []hoppool.Entry{
				{Key: keyA, Trips: 1, NextProbe: now.Add(-time.Second)},
			}},
			// This export has the right hop key but belongs to a stale pool identity.
			{Role: "pre", RouteKey: routeA, PoolIdentity: statusPoolID('f'), Entries: []hoppool.Entry{
				{Key: keyB, Trips: 99, NextProbe: now.Add(time.Hour)},
			}},
		},
	}
	live := &liveAngel{
		opts: upOptions{},
		applied: desiredState{cfg: cfg, pool: routePool{accountRoutes: map[string]string{
			"alice": routeA, "eve": routeA, "bob": routeB, "single": "provider~FR~single",
		}}},
		inst: inst,
	}
	return live, inst, now
}

func TestLiveStatusOmitsHopPoolsForCoreFakesWithoutOptionalSource(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	live := &liveAngel{
		applied: desiredState{cfg: cfg, pool: routePool{accountRoutes: map[string]string{"alice": "route"}}},
		inst:    &fakeCore{addr: "127.0.0.1:12345"},
	}
	for _, account := range live.status("").Accounts {
		if len(account.HopPools) != 0 {
			t.Fatalf("ordinary core fake acquired pool details: %#v", account.HopPools)
		}
	}
}

func TestLiveStatusHopPoolsAreRoleScopedDeduplicatedAndSafe(t *testing.T) {
	live, _, _ := newLiveHopStatusFixture(t)
	st := live.status("")
	byLogin := make(map[string]angel.AccountInfo, len(st.Accounts))
	for _, account := range st.Accounts {
		byLogin[account.Login] = account
	}
	if len(byLogin["alice"].HopPools) != 3 {
		t.Fatalf("alice pools = %#v; want separate pre provider pools and post pool", byLogin["alice"].HopPools)
	}
	var roles []string
	for _, pool := range byLogin["alice"].HopPools {
		roles = append(roles, pool.Role)
	}
	if !reflect.DeepEqual(roles, []string{"pre", "pre", "post"}) {
		t.Fatalf("pool order = %v; want pre pools before the post pool", roles)
	}
	prePoolCounts := make([]int, 0, 2)
	for _, pool := range byLogin["alice"].HopPools {
		if pool.Role == "pre" {
			prePoolCounts = append(prePoolCounts, pool.HopCount)
		}
	}
	if len(prePoolCounts) != 2 || prePoolCounts[0] != 4 || prePoolCounts[1] != 2 {
		t.Fatalf("provider-differing pre pools were combined: %#v", prePoolCounts)
	}
	if !reflect.DeepEqual(byLogin["alice"].HopPools, byLogin["eve"].HopPools) {
		t.Fatalf("shared-route account pool data differs: alice=%#v eve=%#v", byLogin["alice"].HopPools, byLogin["eve"].HopPools)
	}
	if len(byLogin["bob"].HopPools) != 1 || byLogin["bob"].HopPools[0].HopCount != 2 {
		t.Fatalf("bob pools = %#v", byLogin["bob"].HopPools)
	}
	if len(byLogin["single"].HopPools) != 0 {
		t.Fatalf("singleton account pools = %#v", byLogin["single"].HopPools)
	}

	var preLarge, preDown, post *angel.HopPoolInfo
	for i := range byLogin["alice"].HopPools {
		pool := &byLogin["alice"].HopPools[i]
		switch {
		case pool.Role == "pre" && pool.HopCount == 4:
			preLarge = pool
		case pool.Role == "pre" && pool.HopCount == 2:
			preDown = pool
		case pool.Role == "post":
			post = pool
		}
	}
	if preLarge == nil || preLarge.ActiveCount != 1 || preLarge.QuarantinedCount != 3 || preLarge.UnpersistableCount != 1 {
		t.Fatalf("pre pool counts = %#v", preLarge)
	}
	if len(preLarge.Hops) != 4 || preLarge.Hops[0].ID != "11111111" || preLarge.Hops[1].ID != "22222222" || preLarge.Hops[2].ID != "55555555" || preLarge.Hops[3].ID != "" {
		t.Fatalf("pre hops are not safely identified and sorted: %#v", preLarge.Hops)
	}
	if preLarge.Hops[1].Trips != 2 || preLarge.Hops[1].NextRevivalSeconds == nil || *preLarge.Hops[1].NextRevivalSeconds != 2 {
		t.Fatalf("fractional countdown or snapshot trips = %#v", preLarge.Hops[1])
	}
	if preLarge.Hops[2].State != "probing" || preLarge.Hops[2].Trips != 3 || preLarge.Hops[2].NextRevivalSeconds != nil {
		t.Fatalf("probing entry = %#v", preLarge.Hops[2])
	}
	if preLarge.Hops[3].State != "probing" || preLarge.Hops[3].ID != "" || preLarge.Hops[3].NextRevivalSeconds != nil {
		t.Fatalf("unsafe probing entry = %#v", preLarge.Hops[3])
	}
	if preDown == nil || preDown.ActiveCount != 0 || preDown.QuarantinedCount != 2 {
		t.Fatalf("second provider pool was combined into misleading health counts: %#v", preDown)
	}
	if preDown.Hops[0].NextRevivalSeconds == nil || *preDown.Hops[0].NextRevivalSeconds != 30 || preDown.Hops[1].NextRevivalSeconds == nil || *preDown.Hops[1].NextRevivalSeconds != 60 {
		t.Fatalf("stale exports were joined across pool identities: %#v", preDown.Hops)
	}
	if post == nil || post.ActiveCount != 1 || post.QuarantinedCount != 1 || post.Hops[0].NextRevivalSeconds == nil || *post.Hops[0].NextRevivalSeconds != 0 {
		t.Fatalf("post role pool = %#v", post)
	}

	raw := mustJSON(t, st)
	for _, secret := range []string{"hop-secret.example", "do-not-print", "route-a", statusPoolID('f')} {
		if strings.Contains(raw, secret) {
			t.Errorf("status exposed private/internal value %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, `"unpersistable_count":1`) || strings.Contains(raw, statusUnsafeHopKey) {
		t.Fatalf("unpersistable entry was not counted safely: %s", raw)
	}
}

func TestStatusHopPoolsReachShortAndFullJSONAndKtav(t *testing.T) {
	live, _, _ := newLiveHopStatusFixture(t)
	status := live.status("2026-11-12T12:00:00Z")
	for _, args := range [][]string{{"--json"}, {"--json", "--full"}} {
		stubStatus(t, status)
		data := runStatusData(t, args...)
		accounts := data["accounts"].([]any)
		alice := accounts[0].(map[string]any)
		if alice["login"] != "alice" || len(alice["hop_pools"].([]any)) != 3 {
			t.Fatalf("JSON args %v omitted live pool status: %#v", args, alice)
		}
		single := accounts[3].(map[string]any)
		if _, ok := single["hop_pools"]; ok {
			t.Fatalf("JSON args %v included singleton pool status: %#v", args, single)
		}
	}

	useKtavOutput(t)
	for _, args := range [][]string{nil, {"--full"}} {
		stubStatus(t, status)
		doc := runStatusKtavDoc(t, args...)
		data := doc["data"].(map[string]any)
		accounts := data["accounts"].([]any)
		alice := accounts[0].(map[string]any)
		pools := alice["hop_pools"].([]any)
		if len(pools) != 3 {
			t.Fatalf("Ktav args %v pools = %#v", args, pools)
		}
		single := accounts[3].(map[string]any)
		if _, ok := single["hop_pools"]; ok {
			t.Fatalf("Ktav args %v included singleton pool status: %#v", args, single)
		}
		first := pools[0].(map[string]any)
		if first["role"] == "" || first["hop_count"] == nil {
			t.Fatalf("Ktav pool shape = %#v", first)
		}
	}
}

func TestLiveStatusShowsTheDefaultRoutePoolAtTopLevel(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	identity, keyA, keyB := statusPoolID('a'), statusPoolID('1'), statusPoolID('2')
	inst := &liveStatusHopCore{
		fakeCore: &fakeCore{addr: "127.0.0.1:12345"},
		routes:   map[string]string{"alice": "provider~DE~route"},
		now:      now,
		snapshots: []core.HopPoolSnapshot{
			statusPoolSnapshot("post", core.GlobalPoolRoute, identity, keyA, "healthy", 0),
			statusPoolSnapshot("post", core.GlobalPoolRoute, identity, keyB, "quarantined", 1),
		},
		exported: []core.HopPoolState{{Role: "post", RouteKey: core.GlobalPoolRoute, PoolIdentity: identity, Entries: []hoppool.Entry{
			{Key: keyB, Trips: 1, NextProbe: now.Add(30 * time.Second)},
		}}},
	}
	live := &liveAngel{
		applied: desiredState{cfg: cfg, pool: routePool{accountRoutes: map[string]string{"alice": "provider~DE~route"}}},
		inst:    inst,
	}
	st := live.status("")
	if len(st.HopPools) != 1 {
		t.Fatalf("default route pools = %#v, want one", st.HopPools)
	}
	got := st.HopPools[0]
	if got.Role != "post" || got.HopCount != 2 || got.ActiveCount != 1 || got.QuarantinedCount != 1 ||
		got.Hops[1].ID != "22222222" || got.Hops[1].NextRevivalSeconds == nil || *got.Hops[1].NextRevivalSeconds != 30 {
		t.Fatalf("default route pool = %#v", got)
	}
	for _, account := range st.Accounts {
		if len(account.HopPools) != 0 {
			t.Fatalf("account %s got the default route pool: %#v", account.Login, account.HopPools)
		}
	}
	raw := mustJSON(t, st)
	if !strings.Contains(raw, `"hop_pools":[{"role":"post"`) {
		t.Fatalf("status JSON lacks the top-level pool: %s", raw)
	}
}

func TestLiveStatusWithoutDefaultRoutePoolOmitsTheField(t *testing.T) {
	live, _, _ := newLiveHopStatusFixture(t)
	st := live.status("")
	if st.HopPools != nil {
		t.Fatalf("default route pools = %#v, want none", st.HopPools)
	}
	if raw := mustJSON(t, angel.Status{}); strings.Contains(raw, "hop_pools") {
		t.Fatalf("empty status JSON has hop_pools: %s", raw)
	}
}
