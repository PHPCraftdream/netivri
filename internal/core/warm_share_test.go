package core

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

type probeLog struct {
	mu     sync.Mutex
	probed []string
	seen   map[string]time.Duration
	failed map[string]error
}

func newProbeLog() *probeLog {
	return &probeLog{seen: map[string]time.Duration{}, failed: map[string]error{}}
}

func (l *probeLog) probe(fail map[string]bool) warmProbe {
	return func(_ context.Context, p C.Proxy, _ string) (uint16, error) {
		l.mu.Lock()
		l.probed = append(l.probed, p.Name())
		l.mu.Unlock()
		if fail[p.Name()] {
			return 0, errors.New("boom")
		}
		return 7, nil
	}
}

func (l *probeLog) hook(o *WarmOptions) {
	o.OnProbe = func(name string, _ bool, d time.Duration) {
		l.mu.Lock()
		l.seen[name] = d
		l.mu.Unlock()
	}
	o.OnProbeError = func(name string, err error) {
		l.mu.Lock()
		l.failed[name] = err
		l.mu.Unlock()
	}
}

func (l *probeLog) probedNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]string(nil), l.probed...)
	sort.Strings(out)
	return out
}

func sharedWarmer(keys map[string]string, names ...string) (*warmer, *probeLog) {
	l := newProbeLog()
	w := &warmer{
		options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/", ProbeConcurrency: 4},
		clock:   testWarmClock{now: time.Unix(1000, 0)},
	}
	w.options.Same = func(name string) string { return keys[name] }
	l.hook(&w.options)
	for _, n := range names {
		w.proxies = append(w.proxies, testWarmProxy{name: n, typ: C.Socks5})
	}
	return w, l
}

// Names with one key are probed once and the result is recorded for each.
func TestProbeAllProbesNamesWithOneKeyOnce(t *testing.T) {
	w, l := sharedWarmer(map[string]string{"netivri-a": "k", "netivri-b": "k", "netivri-c": "other"}, "netivri-a", "netivri-b", "netivri-c")
	w.probe = l.probe(nil)
	w.probeAll(context.Background())
	if got := l.probedNames(); len(got) != 2 || got[0] != "netivri-a" || got[1] != "netivri-c" {
		t.Fatalf("probed %v, want the first of the shared pair and the other node", got)
	}
	for _, n := range []string{"netivri-a", "netivri-b", "netivri-c"} {
		if l.seen[n] != 7*time.Millisecond {
			t.Fatalf("%s recorded %v, want 7ms", n, l.seen[n])
		}
	}
	if st := w.Stats(); len(st.Nodes) != 3 {
		t.Fatalf("stats nodes = %d, want 3", len(st.Nodes))
	}
}

// A failed shared probe is a failure of every member, with the same error.
func TestProbeAllSharedFailureReachesEveryMember(t *testing.T) {
	w, l := sharedWarmer(map[string]string{"netivri-a": "k", "netivri-b": "k"}, "netivri-a", "netivri-b")
	w.probe = l.probe(map[string]bool{"netivri-a": true})
	w.probeAll(context.Background())
	if len(l.probedNames()) != 1 {
		t.Fatalf("probed %v, want one probe", l.probedNames())
	}
	for _, n := range []string{"netivri-a", "netivri-b"} {
		if l.failed[n] == nil {
			t.Fatalf("%s has no probe error", n)
		}
	}
	for _, n := range w.Stats().Nodes {
		if n.Alive {
			t.Fatalf("%s is alive after a failed shared probe", n.Name)
		}
	}
}

// An empty key never shares a probe.
func TestProbeAllEmptyKeyProbesAlone(t *testing.T) {
	w, l := sharedWarmer(map[string]string{}, "netivri-a", "netivri-b")
	w.probe = l.probe(nil)
	w.probeAll(context.Background())
	if got := l.probedNames(); len(got) != 2 {
		t.Fatalf("probed %v, want both", got)
	}
}

// Only the members that are due share a round: a member whose backoff is not
// up does not ride on another's probe.
func TestProbeAllSharedKeySkipsMembersNotDue(t *testing.T) {
	w, l := sharedWarmer(map[string]string{"netivri-a": "k", "netivri-b": "k"}, "netivri-a", "netivri-b")
	w.probe = l.probe(nil)
	w.backoff = []probeSchedule{{}, {nextDue: time.Unix(5000, 0)}}
	w.probeAll(context.Background())
	if _, ok := l.seen["netivri-b"]; ok {
		t.Fatal("a member that is not due was recorded")
	}
	if _, ok := l.seen["netivri-a"]; !ok {
		t.Fatal("the due member was not recorded")
	}
}

func idleWarmer(idle func(string) bool) (*warmer, *probeLog, *testWarmClockRef) {
	l := newProbeLog()
	clock := &testWarmClockRef{now: time.Unix(1000, 0)}
	w := &warmer{
		options: WarmOptions{ProbeInterval: 45 * time.Second, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/", ProbeConcurrency: 4, Idle: idle},
		clock:   clock,
		proxies: []C.Proxy{testWarmProxy{name: "netivri-a", typ: C.Socks5}},
	}
	l.hook(&w.options)
	return w, l, clock
}

// An idle node that answers is probed every fourth interval, not every one.
func TestProbeAllIdleNodeIsProbedLessOften(t *testing.T) {
	w, l, clock := idleWarmer(func(string) bool { return true })
	w.probe = l.probe(nil)
	var rounds []int
	for i := 0; i <= 8; i++ {
		before := len(l.probedNames())
		w.probeAll(context.Background())
		if len(l.probedNames()) > before {
			rounds = append(rounds, i)
		}
		clock.now = clock.now.Add(45 * time.Second)
	}
	want := []int{0, 4, 8}
	if len(rounds) != len(want) {
		t.Fatalf("probed in rounds %v, want %v", rounds, want)
	}
	for i := range want {
		if rounds[i] != want[i] {
			t.Fatalf("probed in rounds %v, want %v", rounds, want)
		}
	}
}

// A node that is not idle is probed every round.
func TestProbeAllBusyNodeIsProbedEveryRound(t *testing.T) {
	w, l, clock := idleWarmer(func(string) bool { return false })
	w.probe = l.probe(nil)
	for i := 0; i < 5; i++ {
		w.probeAll(context.Background())
		clock.now = clock.now.Add(45 * time.Second)
	}
	if got := len(l.probedNames()); got != 5 {
		t.Fatalf("probed %d times, want 5", got)
	}
}

// An idle node whose probe fails is probed again at the usual interval, so a
// real outage is confirmed as fast as for any other node.
func TestProbeAllIdleNodeThatFailsIsProbedAgainNextRound(t *testing.T) {
	w, l, clock := idleWarmer(func(string) bool { return true })
	w.probe = l.probe(map[string]bool{"netivri-a": true})
	for i := 0; i < 3; i++ {
		w.probeAll(context.Background())
		clock.now = clock.now.Add(45 * time.Second)
	}
	if got := len(l.probedNames()); got != 3 {
		t.Fatalf("probed %d times, want 3", got)
	}
}

// probeNow makes an idle node due at once, as after the computer slept.
func TestProbeNowOverridesIdleSpacing(t *testing.T) {
	w, l, _ := idleWarmer(func(string) bool { return true })
	w.probe = l.probe(nil)
	w.probeAll(context.Background())
	w.probeAll(context.Background())
	if got := len(l.probedNames()); got != 1 {
		t.Fatalf("probed %d times before probeNow, want 1", got)
	}
	w.kick = make(chan struct{}, 1)
	w.probeNow()
	w.probeAll(context.Background())
	if got := len(l.probedNames()); got != 2 {
		t.Fatalf("probed %d times after probeNow, want 2", got)
	}
}

func routeOf(key string, nodes ...map[string]any) (Options, routePlan) {
	names := make([]string, len(nodes))
	for i := range nodes {
		names[i] = memberProxyName(0, 0, i)
	}
	o := Options{RouteNodes: map[string][]map[string]any{key: nodes}, RoutePre: map[string]string{}, RoutePost: map[string]string{}}
	return o, routePlan{key: key, names: names}
}

func TestMemberProbeKeysSameNodeSameKey(t *testing.T) {
	a := map[string]any{"name": "from provider one", "type": "ss", "server": "s.example", "port": 443, "password": "p"}
	b := map[string]any{"name": "from provider two", "type": "ss", "server": "s.example", "port": 443, "password": "p"}
	o1, p1 := routeOf("a", a)
	o2, p2 := routeOf("b", b)
	p1.names[0], p2.names[0] = "netivri-x", "netivri-y"
	o1.RouteNodes["b"] = o2.RouteNodes["b"]
	keys := memberProbeKeys([]routePlan{p1, p2}, o1)
	if keys["netivri-x"] == "" || keys["netivri-x"] != keys["netivri-y"] {
		t.Fatalf("the same node under two names got keys %q and %q", keys["netivri-x"], keys["netivri-y"])
	}
}

func TestMemberProbeKeysDifferWhenTheProbeWouldDiffer(t *testing.T) {
	base := map[string]any{"type": "ss", "server": "s.example", "port": 443, "password": "p"}
	other := map[string]any{"type": "ss", "server": "s.example", "port": 444, "password": "p"}
	otherPassword := map[string]any{"type": "ss", "server": "s.example", "port": 443, "password": "q"}

	o, p1 := routeOf("a", base)
	_, p2 := routeOf("b", other)
	_, p3 := routeOf("c", otherPassword)
	_, p4 := routeOf("d", base)
	_, p5 := routeOf("e", base)
	p1.names[0], p2.names[0], p3.names[0], p4.names[0], p5.names[0] = "n1", "n2", "n3", "n4", "n5"
	o.RouteNodes["b"] = []map[string]any{other}
	o.RouteNodes["c"] = []map[string]any{otherPassword}
	o.RouteNodes["d"] = []map[string]any{base}
	o.RouteNodes["e"] = []map[string]any{base}
	o.RoutePre["d"] = "socks5://pre.example:1080"
	o.RoutePost["e"] = "http://post.example:3128"

	keys := memberProbeKeys([]routePlan{p1, p2, p3, p4, p5}, o)
	seen := map[string]string{}
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
		if keys[name] == "" {
			t.Fatalf("%s has no key", name)
		}
		if prev, dup := seen[keys[name]]; dup {
			t.Fatalf("%s and %s share a key but are probed differently", prev, name)
		}
		seen[keys[name]] = name
	}
}

func TestMemberProbeKeysUnhashableNodeProbesAlone(t *testing.T) {
	bad := map[string]any{"type": "ss", "x": make(chan int)}
	o, p := routeOf("a", bad)
	p.names[0] = "n1"
	if keys := memberProbeKeys([]routePlan{p}, o); keys["n1"] != "" {
		t.Fatalf("an unhashable node got key %q", keys["n1"])
	}
}

func fallbackPlan() routePlan {
	return routePlan{
		key:    "a",
		names:  []string{"netivri-g0-r0-n0", "netivri-g0-r0-n1", "netivri-g0-r0-n2"},
		labels: []string{"p/best", "p/second", "p/third"},
		tiers:  []int{0, 1, 2},
	}
}

func TestFallbackIdleOnlyForWorseTiersWithoutTraffic(t *testing.T) {
	health := newHealthTable()
	health.register([]string{"p/best", "p/second", "p/third"})
	idle := fallbackIdle([]routePlan{fallbackPlan()}, health)
	if idle == nil {
		t.Fatal("no Idle for a plan with fallbacks")
	}
	if idle("netivri-g0-r0-n0") {
		t.Fatal("the best tier is never idle")
	}
	if !idle("netivri-g0-r0-n1") || !idle("netivri-g0-r0-n2") {
		t.Fatal("fallbacks without traffic should be idle")
	}
	if idle("netivri-unknown") {
		t.Fatal("a name outside the plans is not idle")
	}
	health.noteTraffic("p/second")
	if idle("netivri-g0-r0-n1") {
		t.Fatal("a fallback that carries traffic is not idle")
	}
	if !idle("netivri-g0-r0-n2") {
		t.Fatal("traffic on one fallback must not wake the other")
	}
}

func TestFallbackIdleEndsWhenTheBetterNodesAreDown(t *testing.T) {
	health := newHealthTable()
	health.register([]string{"p/best", "p/second", "p/third"})
	idle := fallbackIdle([]routePlan{fallbackPlan()}, health)
	health.recordProbe("p/best", false, 0)
	health.recordProbe("p/best", false, 0)
	if !health.down("p/best") {
		t.Fatal("setup: the best node should be down")
	}
	if idle("netivri-g0-r0-n1") {
		t.Fatal("the second tier is the one in use while the best is down: not idle")
	}
	if !idle("netivri-g0-r0-n2") {
		t.Fatal("the third tier still has an up node above it: idle")
	}
	health.recordProbe("p/second", false, 0)
	health.recordProbe("p/second", false, 0)
	if idle("netivri-g0-r0-n2") {
		t.Fatal("with every better node down the third tier is in use: not idle")
	}
}

func TestFallbackIdleNoneForSingleTierOrDirect(t *testing.T) {
	health := newHealthTable()
	health.register([]string{"p/a", "p/b"})
	one := routePlan{key: "a", names: []string{"netivri-x", "netivri-y"}, labels: []string{"p/a", "p/b"}, tiers: []int{0, 0}}
	if fallbackIdle([]routePlan{one}, health) != nil {
		t.Fatal("one tier has no fallbacks")
	}
	noTiers := routePlan{key: "a", names: []string{"netivri-x"}, labels: []string{"p/a"}}
	if fallbackIdle([]routePlan{noTiers}, health) != nil {
		t.Fatal("a plan without tiers has no fallbacks")
	}
}
