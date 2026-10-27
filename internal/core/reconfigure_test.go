package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// instanceWith returns an unpublished Instance whose routing generation is st.
func instanceWith(st *routingState) *Instance {
	i := &Instance{}
	i.routing.Store(st)
	return i
}

func testNode(server string) map[string]any {
	return map[string]any{"type": "socks5", "server": server, "port": 1080}
}

func TestReconfigureRefusesWhatItCannotApply(t *testing.T) {
	routes := map[string]string{"alice": "k"}
	nodes := map[string][]map[string]any{"k": {testNode("192.0.2.1")}}
	inst := &Instance{}
	if err := inst.Reconfigure(Options{Node: testNode("192.0.2.9"), RouteNodes: nodes, AccountRoute: routes}); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("a default node: %v, want ErrReconfigureUnsupported", err)
	}
	if err := inst.Reconfigure(Options{}); !errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("no account routes: %v, want ErrReconfigureUnsupported", err)
	}
	var ce *cli.Error
	if err := inst.Reconfigure(Options{RouteNodes: nodes, AccountRoute: routes, AffinityTTL: -1}); !errors.As(err, &ce) || ce.Code != "NODE_INVALID" {
		t.Errorf("a negative TTL: %v, want NODE_INVALID", err)
	}
	if err := inst.Reconfigure(Options{RouteNodes: nodes, AccountRoute: routes, Warm: &WarmOptions{ProbeInterval: -1}}); err == nil || errors.Is(err, ErrReconfigureUnsupported) {
		t.Errorf("invalid warm options: %v, want a validation error", err)
	}
	closed := &Instance{closed: true}
	if err := closed.Reconfigure(Options{RouteNodes: nodes, AccountRoute: routes}); !errors.Is(err, errInstanceClosed) {
		t.Errorf("a closed instance: %v, want errInstanceClosed", err)
	}
	// Never published (Start did not return it): treated as closed too.
	if err := inst.Reconfigure(Options{RouteNodes: nodes, AccountRoute: routes}); !errors.Is(err, errInstanceClosed) {
		t.Errorf("an unpublished instance: %v, want errInstanceClosed", err)
	}
}

func TestGenerationProxyNames(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{memberProxyName(0, 1, 2), "netivri-r1-2"},
		{groupProxyName(0, 1), "netivri-group-1"},
		{memberProxyName(3, 1, 2), "netivri-g3-r1-2"},
		{groupProxyName(3, 1), "netivri-g3-group-1"},
	} {
		if c.got != c.want {
			t.Errorf("name = %q, want %q", c.got, c.want)
		}
	}

	o := Options{
		RouteNodes:   map[string][]map[string]any{"a": {testNode("192.0.2.1")}, "b": {testNode("192.0.2.2"), testNode("192.0.2.3")}},
		AccountRoute: map[string]string{"alice": "a", "bob": "b"},
	}
	proxies, groups, rules, err := prepareRoutesGen(o, 2)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := buildRoutePlansGen(o, 2)
	if err != nil {
		t.Fatal(err)
	}
	var proxyNames, planNames []string
	for _, p := range proxies {
		proxyNames = append(proxyNames, p["name"].(string))
	}
	for i, plan := range plans {
		planNames = append(planNames, plan.names...)
		if groups[i]["name"] != plan.group {
			t.Errorf("group %d is %v in the config and %q in the plan", i, groups[i]["name"], plan.group)
		}
	}
	if !reflect.DeepEqual(proxyNames, planNames) {
		t.Errorf("member names: config %v, plans %v", proxyNames, planNames)
	}
	want := []string{"IN-USER,alice,netivri-g2-group-0", "IN-USER,bob,netivri-g2-group-1", "MATCH,DIRECT"}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules = %v, want %v", rules, want)
	}
	all := planProxyNames(plans)
	sort.Strings(all)
	if want := []string{"netivri-g2-group-0", "netivri-g2-group-1", "netivri-g2-r0-0", "netivri-g2-r1-0", "netivri-g2-r1-1"}; !reflect.DeepEqual(all, want) {
		t.Errorf("planProxyNames = %v, want %v", all, want)
	}
}

func TestParseRouteRules(t *testing.T) {
	known := func(name string) bool { return name == "netivri-g1-group-0" || name == "DIRECT" }
	rules, err := parseRouteRules([]string{"IN-USER,alice,netivri-g1-group-0", "MATCH,DIRECT"}, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0].Adapter() != "netivri-g1-group-0" || rules[0].RuleType() != C.InUser ||
		rules[1].Adapter() != "DIRECT" || rules[1].RuleType() != C.MATCH {
		t.Fatalf("rules = %v", rules)
	}
	if _, err := parseRouteRules([]string{"IN-USER,alice,netivri-group-0"}, known); err == nil {
		t.Error("a rule to an unknown group was accepted")
	}
}

func TestCarryHealthKeepsStateWithoutAnnouncing(t *testing.T) {
	var oldEvents, newEvents []string
	old := newHealthTable()
	old.notify = func(ev NodeEvent) { oldEvents = append(oldEvents, fmt.Sprintf("%s %v", ev.Label, ev.Down)) }
	old.register([]string{"p/a", "p/gone"})
	old.recordProbe("p/a", false, 0)
	old.recordProbe("p/a", false, 0)
	old.recordProbe("p/gone", true, 30*time.Millisecond)
	old.health("p/a").hedgeUnits.Store(3)
	if !old.down("p/a") || len(oldEvents) != 1 {
		t.Fatalf("setup: down=%v events=%v", old.down("p/a"), oldEvents)
	}
	oldEvents = nil

	carried := carryHealth(old, []string{"p/a", "p/new", "p/a"}, func(ev NodeEvent) {
		newEvents = append(newEvents, fmt.Sprintf("%s %v", ev.Label, ev.Down))
	})
	if !carried.down("p/a") {
		t.Error("a node that was down came back by the change")
	}
	if h := carried.health("p/a"); h == old.health("p/a") || h.consProbeFail != 2 || h.ringLen != 2 || h.hedgeUnits.Load() != 3 {
		t.Errorf("carried record: same=%v fails=%d ring=%d hedge=%d", h == old.health("p/a"), h.consProbeFail, h.ringLen, h.hedgeUnits.Load())
	}
	if carried.health("p/gone") != nil {
		t.Error("a label that left kept a record")
	}
	if h := carried.health("p/new"); h == nil || h.Down() || h.hedgeUnits.Load() != hedgeBudgetCap {
		t.Errorf("a new label: %+v", h)
	}
	if len(oldEvents)+len(newEvents) != 0 {
		t.Fatalf("carrying announced: old %v, new %v", oldEvents, newEvents)
	}

	carried.recordProbe("p/a", true, 20*time.Millisecond)
	if len(newEvents) != 1 || newEvents[0] != "p/a false" || len(oldEvents) != 0 {
		t.Errorf("the recovery went to old %v, new %v; want only the new listener", oldEvents, newEvents)
	}
	if !old.down("p/a") {
		t.Error("the old record changed with the carried one")
	}
	if carryHealth(nil, []string{"p/x"}, nil).health("p/x") == nil {
		t.Error("without an old table the labels were not registered")
	}
}

func TestCarryBackoffByLabel(t *testing.T) {
	due := time.Unix(1000, 0)
	old := map[string]probeSchedule{
		"netivri-r0-0": {downRounds: 2, nextDue: due},
		"netivri-r1-0": {downRounds: 4, nextDue: due.Add(time.Minute)},
		"netivri-r0-1": {downRounds: 3, nextDue: due},
		"unknown":      {downRounds: 9},
	}
	oldLabels := map[string]string{"netivri-r0-0": "p/a", "netivri-r1-0": "p/a", "netivri-r0-1": "p/gone"}
	newLabels := map[string]string{"netivri-g1-r0-0": "p/a", "netivri-g1-r0-1": "p/new"}
	got := carryBackoff(old, oldLabels, newLabels)
	want := map[string]probeSchedule{"netivri-g1-r0-0": {downRounds: 4, nextDue: due.Add(time.Minute)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("carryBackoff = %v, want %v", got, want)
	}
}

func TestWarmLeavesAndSeededBackoff(t *testing.T) {
	proxies := map[string]C.Proxy{
		"netivri-r0-0":    testWarmProxy{name: "netivri-r0-0", typ: C.Socks5},
		"netivri-g1-r0-1": testWarmProxy{name: "netivri-g1-r0-1", typ: C.Socks5},
		"netivri-g1-r0-0": testWarmProxy{name: "netivri-g1-r0-0", typ: C.Socks5},
		"DIRECT":          testWarmProxy{name: "DIRECT", typ: C.Direct},
	}
	if got := warmLeaves(proxies, nil); len(got) != 3 || got[0].Name() != "netivri-g1-r0-0" {
		t.Errorf("all leaves = %v", got)
	}
	only := warmLeaves(proxies, func(name string) bool { return strings.HasPrefix(name, "netivri-g1-") })
	if len(only) != 2 || only[0].Name() != "netivri-g1-r0-0" || only[1].Name() != "netivri-g1-r0-1" {
		t.Fatalf("filtered leaves = %v", only)
	}
	if seededBackoff(only, nil) != nil {
		t.Error("no seed must leave the backoff to probeAll")
	}
	got := seededBackoff(only, map[string]probeSchedule{"netivri-g1-r0-1": {downRounds: 3}})
	if len(got) != 2 || got[0] != (probeSchedule{}) || got[1].downRounds != 3 {
		t.Errorf("seeded backoff = %v", got)
	}

	var none *warmer
	if none.backoffByName() != nil {
		t.Error("a nil warmer has a backoff")
	}
	checked := time.Unix(500, 0)
	w := &warmer{proxies: only, backoff: got, options: WarmOptions{ProbeInterval: 45 * time.Second}}
	w.stats.Nodes = []NodeHealth{
		{Name: "netivri-g1-r0-0", Alive: true, CheckedAt: checked},
		{Name: "netivri-g1-r0-1", CheckedAt: checked},
	}
	by := w.backoffByName()
	if len(by) != 2 || by["netivri-g1-r0-1"].downRounds != 3 ||
		by["netivri-g1-r0-0"] != (probeSchedule{nextDue: checked.Add(45 * time.Second)}) {
		t.Errorf("backoffByName = %v", by)
	}

	// A seed the warmer has not reached yet is passed on to the next
	// generation, unless a later probe moved the schedule.
	seeded := time.Unix(600, 0)
	w = &warmer{proxies: only, backoff: []probeSchedule{{nextDue: seeded}, {nextDue: seeded}}, options: WarmOptions{ProbeInterval: 45 * time.Second}}
	w.stats.Nodes = []NodeHealth{{Name: "netivri-g1-r0-1", Alive: true, CheckedAt: seeded.Add(time.Minute)}}
	by = w.backoffByName()
	if by["netivri-g1-r0-0"] != (probeSchedule{nextDue: seeded}) ||
		by["netivri-g1-r0-1"] != (probeSchedule{nextDue: seeded.Add(time.Minute + 45*time.Second)}) {
		t.Errorf("backoffByName with seeds = %v", by)
	}
}

// A node that is not down but was seeded by Reconfigure waits for its
// interval; afterwards it is probed every round again.
func TestProbeAllHonoursSeededSchedule(t *testing.T) {
	clock := &testWarmClockRef{now: time.Unix(1000, 0)}
	var mu sync.Mutex
	probed := map[string]int{}
	count := func(name string) int { mu.Lock(); defer mu.Unlock(); return probed[name] }
	w := &warmer{options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: clock}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-g1-r0-0", typ: C.Socks5}, testWarmProxy{name: "netivri-g1-r0-1", typ: C.Socks5}}
	w.backoff = []probeSchedule{{nextDue: clock.now.Add(30 * time.Second)}, {}}
	w.probe = func(_ context.Context, p C.Proxy, _ string) (uint16, error) {
		mu.Lock()
		probed[p.Name()]++
		mu.Unlock()
		return 10, nil
	}
	w.probeAll(context.Background())
	if count("netivri-g1-r0-0") != 0 || count("netivri-g1-r0-1") != 1 {
		t.Fatalf("first round probed %d/%d, want only the unseeded node", count("netivri-g1-r0-0"), count("netivri-g1-r0-1"))
	}
	clock.now = clock.now.Add(31 * time.Second)
	w.probeAll(context.Background())
	w.probeAll(context.Background())
	if count("netivri-g1-r0-0") != 2 || count("netivri-g1-r0-1") != 3 {
		t.Fatalf("later rounds probed %d/%d, want every node each round", count("netivri-g1-r0-0"), count("netivri-g1-r0-1"))
	}
}

// testWarmClockRef is a settable clock for probeAll.
type testWarmClockRef struct{ now time.Time }

func (c *testWarmClockRef) Now() time.Time                     { return c.now }
func (c *testWarmClockRef) NewTicker(time.Duration) warmTicker { return nil }

var _ warmClock = (*testWarmClockRef)(nil)

// The bindings of the old generation are carried by label: a site keeps
// its node when the node is still in the route, under its new index. A login
// whose route key changed (new country or provider lists) picks anew.
func TestCarriedBindingsFollowTheLabel(t *testing.T) {
	notDown := func(int) bool { return false }
	pick := func(member int) func() (int, bool) { return func() (int, bool) { return member, false } }
	oldTable := newAffinityTable(nil, 0)
	oldTable.acquire(affinityKey{login: "alice", site: "example.com"}, notDown, pick(1))
	oldTable.acquire(affinityKey{login: "alice", site: "gone.example"}, notDown, pick(0))
	oldTable.acquire(affinityKey{login: "bob", site: "example.com"}, notDown, pick(1))
	oldTable.rebinds.Add(2)
	oldTable.hedges.Add(3)
	oldTable.hedgeWins.Add(1)
	old := &routingState{
		table:         oldTable,
		accountRoutes: map[string]string{"alice": "k", "bob": "k"},
		routeLabels:   map[string][]string{"k": {"p/a", "p/b"}},
	}

	o := Options{
		RouteNodes: map[string][]map[string]any{
			"k":  {testNode("192.0.2.2"), testNode("192.0.2.3")},
			"k2": {testNode("192.0.2.2"), testNode("192.0.2.3")},
		},
		NodeLabels:   map[string][]string{"k": {"p/b", "p/c"}, "k2": {"p/b", "p/c"}},
		AccountRoute: map[string]string{"alice": "k", "bob": "k2"},
	}
	plans, err := buildRoutePlansGen(o, 1)
	if err != nil {
		t.Fatal(err)
	}
	table := newAffinityTable(nil, 0)
	table.carryCounters(oldTable)
	records := sameRouteBindings(old.snapshotBindings(), old.accountRoutes, o.AccountRoute)
	if n := loadInitialBindings(table, plans, o.AccountRoute, records); n != 1 {
		t.Fatalf("carried %d bindings, want 1", n)
	}
	if member, ok := table.peek(affinityKey{login: "alice", site: "example.com"}); !ok || member != 0 {
		t.Errorf("example.com is on member %d (ok=%v), want 0 (p/b)", member, ok)
	}
	if _, ok := table.peek(affinityKey{login: "alice", site: "gone.example"}); ok {
		t.Error("a binding to a node that left the route was carried")
	}
	if _, ok := table.peek(affinityKey{login: "bob", site: "example.com"}); ok {
		t.Error("a binding of a login with a new route key was carried")
	}
	if table.rebindCount() != 2 || table.hedgeCount() != 3 || table.hedgeWinCount() != 1 {
		t.Errorf("counters = %d/%d/%d, want 2/3/1", table.rebindCount(), table.hedgeCount(), table.hedgeWinCount())
	}
}

func TestLiveConnLabelsKeepsOpenChains(t *testing.T) {
	source := &fakeTrackerSource{trackers: []throughputTracker{
		&fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0", "netivri-group-0"}}},
		&fakeTracker{id: "t2", info: &statistic.TrackerInfo{Chain: C.Chain{"DIRECT"}}},
		&fakeTracker{id: "t3"},
	}}
	got := liveConnLabels(source, map[string]string{"netivri-r0-0": "p/a", "netivri-r0-1": "p/b"})
	if !reflect.DeepEqual(got, map[string]string{"netivri-r0-0": "p/a"}) {
		t.Fatalf("liveConnLabels = %v", got)
	}
}

// After setTargets the poller feeds the new health table and still counts
// the connections of the previous generation by their old names.
func TestThroughputPollerFollowsSetTargets(t *testing.T) {
	oldHealth := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, oldHealth, obs, map[string]string{"netivri-r0-0": "p/a"}, nil)
	base := time.Unix(0, 0)
	held := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0"}, Start: base.Add(-time.Minute)}}
	source := &fakeTrackerSource{trackers: []throughputTracker{held}}
	poller.source = source
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)

	newHealth := newHealthTable()
	poller.setTargets(newHealth, map[string]string{"netivri-r0-0": "p/a", "netivri-g1-r0-0": "p/b"})
	fresh := &fakeTracker{id: "t2", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-g1-r0-0"}, Start: base.Add(6 * time.Second)}}
	source.trackers = append(source.trackers, fresh)
	held.info.UploadTotal.Add(1 << 20)
	fresh.info.DownloadTotal.Add(1 << 20)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)

	got := map[string]int64{}
	for _, s := range obs.throughputSamples() {
		got[s.label] += s.bytes
	}
	if got["p/a"] != 1<<20 || got["p/b"] != 1<<20 {
		t.Fatalf("throughput = %v, want 1 MiB on each node", got)
	}
	if newHealth.entries["p/a"].throughput <= 0 || newHealth.entries["p/b"].throughput <= 0 {
		t.Error("the new health table was not fed")
	}
	if oldHealth.entries["p/a"].throughput != 0 {
		t.Error("the old health table was fed after the switch")
	}
	var none *throughputPoller
	none.setTargets(newHealth, nil)
}
