package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// Observer receives node measurements for the statistics store. Methods must
// not block and must be safe for concurrent use.
type Observer interface {
	ObserveProbe(label string, alive bool, delay time.Duration)
	ObserveDial(label string, ok bool, took time.Duration)
	ObserveThroughput(label string, bytes int64, window time.Duration)
}

// AccountObserver is the optional per-account extension of Observer. An
// Observer may implement it; the core checks with a type assertion so
// existing implementations keep compiling.
type AccountObserver interface {
	ObserveAccountDial(login, label string, ok bool, took time.Duration)
	ObserveAccountThroughput(login, label string, up, down int64, window time.Duration)
}

// AffinityStats is a snapshot of the binding table.
type AffinityStats struct {
	Bindings  int
	Rebinds   uint64
	Hedges    uint64
	HedgeWins uint64
}

// member is the part of C.Proxy the sticky group uses; a small interface so
// tests can use fake members.
type member interface {
	Name() string
	DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error)
	ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error)
	SupportUDP() bool
	IsL3Protocol(metadata *C.Metadata) bool
}

var _ member = C.Proxy(nil)

// stickyGroup is a per-account-route Mihomo group adapter: every connection
// of one account to one site leaves through the same member while it is
// healthy. It reports type Selector so the warm-up keeps ignoring it; the
// select groups stay in the raw config for rule validation and are replaced
// in the proxy map after ApplyConfig.
type stickyGroup struct {
	*outboundgroup.GroupBase

	members  []member  // dial and listen paths
	proxies  []C.Proxy // the same members as C.Proxy, for Unwrap; may be nil in tests
	labels   []string  // statistics label per member, same order
	table    *affinityTable
	health   *healthTable
	healths  []*nodeHealth // per member, same order; Down() is one atomic load
	tiers    []int         // priority tier per member (lower first), nil = one tier
	isDownFn func(member int) bool
	observer Observer
	hedgeAll bool // test seam: fake members are hedgeable when set
}

// newStickyGroup builds the group over real Mihomo member proxies.
func newStickyGroup(name string, proxies []C.Proxy, labels []string, table *affinityTable, health *healthTable, observer Observer) *stickyGroup {
	members := make([]member, len(proxies))
	for i, proxy := range proxies {
		members[i] = proxy
	}
	g := newStickyGroupMembers(name, members, labels, table, health, observer)
	g.proxies = append([]C.Proxy(nil), proxies...)
	return g
}

// newStickyGroupMembers is the testable core: it accepts the member
// interface, so unit tests can use fake members (without Unwrap support).
func newStickyGroupMembers(name string, members []member, labels []string, table *affinityTable, health *healthTable, observer Observer) *stickyGroup {
	health.register(labels)
	g := &stickyGroup{
		GroupBase: outboundgroup.NewGroupBase(outboundgroup.GroupBaseOption{
			Name: name,
			Type: C.Selector,
		}),
		members:  members,
		labels:   labels,
		table:    table,
		health:   health,
		healths:  make([]*nodeHealth, len(labels)),
		observer: observer,
	}
	for i, label := range labels {
		g.healths[i] = health.health(label)
	}
	g.isDownFn = g.memberDown
	return g
}

// choose returns the member index for the connection key (login, site),
// creating or keeping the binding as needed. On a healthy binding hit it
// neither allocates nor builds health views.
func (g *stickyGroup) choose(login, site string) (int, bool) {
	return g.table.acquire(affinityKey{login: login, site: site}, g.isDownFn, g.pickFor(login, site))
}

func (g *stickyGroup) memberDown(member int) bool {
	return member >= 0 && member < len(g.healths) && g.healths[member].Down()
}

// pickFor builds the pick closure acquire calls on a miss; the views are
// built only then. It may read the health table but must not take shard
// locks.
func (g *stickyGroup) pickFor(login, site string) func() (int, bool) {
	return func() (int, bool) {
		views := g.health.views(g.labels)
		if len(g.tiers) == len(views) {
			for i := range views {
				views[i].Tier = g.tiers[i]
			}
		}
		index := selectMember(views, login, site)
		if index < 0 {
			index = 0
		}
		return index, provisionalBinding(views)
	}
}

// peekMember returns the member of the live binding for the key without
// creating or refreshing one, or the first member when there is none.
// Display and non-dial paths (Unwrap, IsL3Protocol) must never create,
// refresh or count bindings; only DialContext and ListenPacketContext may.
func (g *stickyGroup) peekMember(login, site string) int {
	if member, ok := g.table.peek(affinityKey{login: login, site: site}); ok {
		return member
	}
	return 0
}

func (g *stickyGroup) observeDial(label string, ok bool, took time.Duration) {
	if g.observer != nil {
		g.observer.ObserveDial(label, ok, took)
	}
}

func (g *stickyGroup) observeAccountDial(login, label string, ok bool, took time.Duration) {
	if g.observer == nil || login == "" {
		return
	}
	if ao, isAccount := g.observer.(AccountObserver); isAccount {
		ao.ObserveAccountDial(login, label, ok, took)
	}
}

// DialContext implements C.ProxyAdapter. It times the logical dial (hedged
// when the member is hedgeable, see hedge.go), feeds the health table and
// the observer once per logical dial, and retries once on the re-selected
// member when the failure makes the node down.
func (g *stickyGroup) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	login, site := metadata.InUser, siteKey(metadata.Host, metadata.DstIP)
	index, _ := g.choose(login, site)
	label := g.labels[index]

	conn, err, took, recorded := g.dialMember(ctx, index, metadata)
	if recorded {
		g.health.recordDial(label, err == nil, took)
		g.observeDial(label, err == nil, took)
		g.observeAccountDial(login, label, err == nil, took)
		if h := g.healths[index]; h != nil {
			h.hedgeRefillDial()
		}
	}
	if err == nil {
		conn.AppendToChains(g)
		return conn, nil
	}
	if !g.health.down(label) {
		return nil, err
	}

	// The node just went down: rebind and retry once.
	retry, _ := g.choose(login, site)
	if retry == index {
		return nil, err
	}
	retryLabel := g.labels[retry]
	retryConn, retryErr, retryTook, retryRecorded := g.dialMember(ctx, retry, metadata)
	if retryRecorded {
		g.health.recordDial(retryLabel, retryErr == nil, retryTook)
		g.observeDial(retryLabel, retryErr == nil, retryTook)
		g.observeAccountDial(login, retryLabel, retryErr == nil, retryTook)
	}
	if retryErr != nil {
		return nil, retryErr
	}
	retryConn.AppendToChains(g)
	return retryConn, nil
}

// ListenPacketContext implements C.ProxyAdapter. UDP associations use the
// same choice as TCP.
func (g *stickyGroup) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	index, _ := g.choose(metadata.InUser, siteKey(metadata.Host, metadata.DstIP))
	pc, err := g.members[index].ListenPacketContext(ctx, metadata)
	if err == nil {
		pc.AppendToChains(g)
	}
	return pc, err
}

// SupportUDP implements C.ProxyAdapter.
func (g *stickyGroup) SupportUDP() bool {
	if len(g.members) == 0 {
		return false
	}
	return g.members[0].SupportUDP()
}

// IsL3Protocol implements C.ProxyAdapter, without creating a binding.
func (g *stickyGroup) IsL3Protocol(metadata *C.Metadata) bool {
	if len(g.members) == 0 {
		return false
	}
	if metadata == nil {
		return g.members[0].IsL3Protocol(metadata)
	}
	return g.members[g.peekMember(metadata.InUser, siteKey(metadata.Host, metadata.DstIP))].IsL3Protocol(metadata)
}

// Unwrap implements C.ProxyAdapter: the currently bound member for the
// metadata, without creating a binding. Without real proxies (unit tests)
// it returns nil.
func (g *stickyGroup) Unwrap(metadata *C.Metadata, touch bool) C.Proxy {
	if len(g.proxies) == 0 {
		return nil
	}
	if metadata == nil {
		return g.proxies[0]
	}
	return g.proxies[g.peekMember(metadata.InUser, siteKey(metadata.Host, metadata.DstIP))]
}

// MarshalJSON implements C.ProxyAdapter.
func (g *stickyGroup) MarshalJSON() ([]byte, error) {
	all := make([]string, 0, len(g.members))
	for _, m := range g.members {
		all = append(all, m.Name())
	}
	now := ""
	if len(g.members) > 0 {
		now = g.members[0].Name()
	}
	return json.Marshal(map[string]any{
		"type": g.Type().String(),
		"now":  now,
		"all":  all,
	})
}

var _ C.ProxyAdapter = (*stickyGroup)(nil)

// routePlan is one account route prepared for sticky-group installation.
type routePlan struct {
	index  int
	key    string   // route key the plan was built from
	names  []string // internal proxy names, member order
	labels []string // statistics labels, member order
	tiers  []int    // priority tier per member, lower first; nil = one tier
}

// checkedTiers validates the tiers of a route of n nodes: none, or one
// non-negative tier per node that never decreases along the route.
func checkedTiers(tiers []int, n int) ([]int, error) {
	if len(tiers) == 0 {
		return nil, nil
	}
	if len(tiers) != n {
		return nil, nodeInvalid("node tier count does not match the route")
	}
	prev := 0
	for _, tier := range tiers {
		if tier < prev {
			return nil, nodeInvalid("node tiers must not decrease along the route")
		}
		prev = tier
	}
	return append([]int(nil), tiers...), nil
}

// buildRoutePlans mirrors the route indexing of prepareRoutes (route keys are
// the sorted distinct values of AccountRoute) and resolves the statistics
// label of every member. Errors use nodeInvalid and never contain labels or
// keys.
func buildRoutePlans(o Options) ([]routePlan, error) {
	// Validate every NodeLabels entry of a known route, referenced or not and
	// whether or not any account has a route.
	for key, labels := range o.NodeLabels {
		nodes, ok := o.RouteNodes[key]
		if !ok {
			continue
		}
		if len(labels) != len(nodes) {
			return nil, nodeInvalid("node label count does not match the route")
		}
		for _, label := range labels {
			if !validNodeLabel(label) {
				return nil, nodeInvalid("invalid node label")
			}
		}
	}
	if len(o.AccountRoute) == 0 {
		return nil, nil
	}
	routeKeys := make(map[string]struct{}, len(o.AccountRoute))
	for _, key := range o.AccountRoute {
		routeKeys[key] = struct{}{}
	}
	keys := make([]string, 0, len(routeKeys))
	for key := range routeKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	plans := make([]routePlan, 0, len(keys))
	for index, key := range keys {
		nodes := o.RouteNodes[key]
		if len(nodes) == 0 {
			return nil, &cli.Error{
				Code:    "NO_NODES_FOR_COUNTRY",
				Message: "no nodes are available for an account route",
				Exit:    cli.ExitNotFound,
			}
		}
		plan := routePlan{index: index, key: key}
		for node := range nodes {
			plan.names = append(plan.names, fmt.Sprintf("netivri-r%d-%d", index, node))
			plan.labels = append(plan.labels, fmt.Sprintf("r%d-%d", index, node))
		}
		if labels, ok := o.NodeLabels[key]; ok {
			if len(labels) != len(nodes) {
				return nil, nodeInvalid("node label count does not match the route")
			}
			for _, label := range labels {
				if !validNodeLabel(label) {
					return nil, nodeInvalid("invalid node label")
				}
			}
			plan.labels = append([]string(nil), labels...)
		}
		tiers, err := checkedTiers(o.NodeTiers[key], len(nodes))
		if err != nil {
			return nil, err
		}
		plan.tiers = tiers
		plans = append(plans, plan)
	}
	return plans, nil
}

// installStickyGroups replaces every route select group in Mihomo's proxy
// map with a sticky adapter of the same name, the way the pool replaces the
// default node. Must run after executor.ApplyConfig.
func installStickyGroups(plans []routePlan, table *affinityTable, health *healthTable, observer Observer) error {
	proxies := tunnel.Proxies()
	updated := make(map[string]C.Proxy, len(proxies))
	for name, existing := range proxies {
		updated[name] = existing
	}
	for _, plan := range plans {
		name := fmt.Sprintf("%s%d", accountGroupPrefix, plan.index)
		members := make([]C.Proxy, 0, len(plan.names))
		for _, memberName := range plan.names {
			proxy, ok := proxies[memberName]
			if !ok || proxy == nil {
				return errors.New("core: missing route member proxy " + memberName)
			}
			members = append(members, proxy)
		}
		group := newStickyGroup(name, members, plan.labels, table, health, observer)
		group.tiers = plan.tiers
		updated[name] = adapter.NewProxy(group)
	}
	tunnel.UpdateProxies(updated, tunnel.Providers())
	return nil
}

// probeHook maps internal proxy names to node labels and feeds the health
// table and the observer; the caller's own OnProbe, if any, still runs.
func probeHook(nameToLabel map[string]string, health *healthTable, observer Observer, next func(string, bool, time.Duration)) func(string, bool, time.Duration) {
	return func(name string, alive bool, delay time.Duration) {
		if label, ok := nameToLabel[name]; ok {
			health.recordProbe(label, alive, delay)
			if observer != nil {
				observer.ObserveProbe(label, alive, delay)
			}
		}
		if next != nil {
			next(name, alive, delay)
		}
	}
}

// memberNameLabels maps every member proxy name of every route to its
// statistics label.
func memberNameLabels(plans []routePlan) map[string]string {
	labels := make(map[string]string)
	for _, plan := range plans {
		for i, name := range plan.names {
			labels[name] = plan.labels[i]
		}
	}
	return labels
}

const (
	throughputPollInterval = 5 * time.Second
	// throughputSampleMin is the smallest window that is recorded as a
	// throughput sample (16 KiB).
	throughputSampleMin = 16 << 10
)

// stickyClock is injectable for tests (poller only).
type stickyClock interface {
	Now() time.Time
	NewTicker(time.Duration) stickyTicker
}

type stickyTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realStickyClock struct{}

func (realStickyClock) Now() time.Time { return time.Now() }
func (realStickyClock) NewTicker(d time.Duration) stickyTicker {
	return realStickyTicker{time.NewTicker(d)}
}

type realStickyTicker struct{ ticker *time.Ticker }

func (t realStickyTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t realStickyTicker) Stop()                  { t.ticker.Stop() }

// throughputTracker is the part of statistic.Tracker the poller reads.
type throughputTracker interface {
	ID() string
	Info() *statistic.TrackerInfo
}

// trackerSource enumerates the active connection trackers.
type trackerSource interface {
	Range(f func(tracker throughputTracker) bool)
}

// managerSource adapts statistic.DefaultManager.
type managerSource struct{ manager *statistic.Manager }

func (s managerSource) Range(f func(throughputTracker) bool) {
	s.manager.Range(func(t statistic.Tracker) bool { return f(t) })
}

type trackerTotals struct {
	label string
	login string
	up    int64
	down  int64
}

// accountTraffic accumulates the up/down deltas of one (login, label) pair
// during a window.
type accountTraffic struct {
	login string
	label string
	up    int64
	down  int64
}

// throughputPoller walks Mihomo's active connection trackers every window,
// attributes each tracker to the member in its chain and reports the bytes
// moved since the previous read per node.
type throughputPoller struct {
	source   trackerSource
	health   *healthTable
	observer Observer
	labels   map[string]string // member proxy name -> node label
	window   time.Duration
	clock    stickyClock

	// started and lastTick come from the injected clock; last holds the
	// per-tracker totals of the previous read. All three are touched only by
	// the poller goroutine (and by tests between ticks), so they need no lock.
	started  time.Time
	lastTick time.Time
	last     map[string]trackerTotals

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newThroughputPoller(source trackerSource, health *healthTable, observer Observer, memberLabels map[string]string, clock stickyClock) *throughputPoller {
	if clock == nil {
		clock = realStickyClock{}
	}
	labels := make([]string, 0, len(memberLabels))
	for _, label := range memberLabels {
		labels = append(labels, label)
	}
	health.register(labels)
	ctx, cancel := context.WithCancel(context.Background())
	return &throughputPoller{
		source:   source,
		health:   health,
		observer: observer,
		labels:   memberLabels,
		window:   throughputPollInterval,
		last:     make(map[string]trackerTotals),
		clock:    clock,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

func (p *throughputPoller) start() {
	p.started = p.clock.Now()
	p.lastTick = p.started
	go p.run()
}

// run walks the trackers once per window until the poller is stopped. The
// window is the real elapsed time measured with the injected clock.
func (p *throughputPoller) run() {
	defer close(p.done)
	ticker := p.clock.NewTicker(p.window)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.Chan():
			now := p.clock.Now()
			prev := p.lastTick
			p.lastTick = now
			p.collectAt(prev, now, now.Sub(prev))
		}
	}
}

// collectAt accounts the bytes moved during the window ending at now into
// per-node samples. A tracker already present is counted by its delta; a
// tracker first seen now whose connection started after prevTick counts its
// whole current totals (its first window was otherwise missed); a tracker
// that already existed before the previous tick is only baselined. Trackers
// that disappeared are pruned.
func (p *throughputPoller) collectAt(prevTick, now time.Time, window time.Duration) {
	type accountKey struct{ login, label string }
	nodeMoved := make(map[string]*accountTraffic)
	acctMoved := make(map[accountKey]*accountTraffic)
	seen := make(map[string]struct{}, len(p.last))
	p.source.Range(func(t throughputTracker) bool {
		info := t.Info()
		if info == nil {
			return true
		}
		label := p.memberLabel(info.Chain)
		if label == "" {
			return true
		}
		login := ""
		if info.Metadata != nil {
			login = info.Metadata.InUser
		}
		id := t.ID()
		seen[id] = struct{}{}
		up := info.UploadTotal.Load()
		down := info.DownloadTotal.Load()
		key := accountKey{login: login, label: label}
		node := nodeMoved[label]
		if node == nil {
			node = &accountTraffic{label: label}
			nodeMoved[label] = node
		}
		entry := acctMoved[key]
		if entry == nil {
			entry = &accountTraffic{login: login, label: label}
			acctMoved[key] = entry
		}
		if prev, ok := p.last[id]; ok && prev.label == label && prev.login == login {
			if delta := (up - prev.up) + (down - prev.down); delta > 0 {
				node.up += up - prev.up
				node.down += down - prev.down
				entry.up += up - prev.up
				entry.down += down - prev.down
			}
		} else if _, ok := p.last[id]; !ok && info.Start.After(prevTick) {
			if total := up + down; total > 0 {
				node.up += up
				node.down += down
				entry.up += up
				entry.down += down
			}
		}
		p.last[id] = trackerTotals{label: label, login: login, up: up, down: down}
		return true
	})
	for id := range p.last {
		if _, ok := seen[id]; !ok {
			delete(p.last, id)
		}
	}
	if window <= 0 {
		window = p.window
	}
	// Two independent gates apply: the node gate is throughputSampleMin on
	// the combined per-node total of all logins, while the account gate is
	// the same threshold applied to each individual per-(login, label) entry.
	for label, node := range nodeMoved {
		bytes := node.up + node.down
		if bytes < throughputSampleMin {
			continue
		}
		if p.observer != nil {
			p.observer.ObserveThroughput(label, bytes, window)
		}
		p.health.recordThroughput(label, bytes, window)
	}
	for key, acc := range acctMoved {
		if acc.up+acc.down < throughputSampleMin {
			continue
		}
		if key.login == "" || p.observer == nil {
			continue
		}
		if ao, ok := p.observer.(AccountObserver); ok {
			ao.ObserveAccountThroughput(key.login, key.label, acc.up, acc.down, window)
		}
	}
}

// stop cancels the poller goroutine and waits briefly for it. Nil-safe.
func (p *throughputPoller) stop() {
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

// memberLabel maps a connection chain to the node label of the member it
// used, or "" when the chain contains no member of this instance.
func (p *throughputPoller) memberLabel(chain C.Chain) string {
	for _, name := range chain {
		if label, ok := p.labels[name]; ok {
			return label
		}
	}
	return ""
}
