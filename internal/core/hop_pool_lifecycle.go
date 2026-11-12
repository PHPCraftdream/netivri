package core

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/check"
	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/proxyclient"
	hoprevive "github.com/PHPCraftdream/netivri/pkg/hopkit/revive"
	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

// HopEventKind identifies a pool health transition.
type HopEventKind string

const (
	HopEventQuarantined HopEventKind = "quarantined"
	HopEventRevived     HopEventKind = "revived"
)

// HopEvent contains opaque route and hop identities.
type HopEvent struct {
	Role     string
	RouteKey string
	Key      string
	Kind     HopEventKind
	Trips    int
}

// HopPoolSnapshot reports one secret-free pool entry.
type HopPoolSnapshot struct {
	Role         string
	RouteKey     string
	PoolIdentity string
	Key          string
	State        string
	Sand         float64
	Weight       float64
	Trips        int
}

// HopPoolState is a quarantine-only persistence record.
type HopPoolState struct {
	Role         string
	RouteKey     string
	PoolIdentity string
	Entries      []pool.Entry
}

type hopPoolLogicalKey struct {
	role  hopPoolRole
	route string
	node  int
}
type hopPoolUse struct {
	logical       hopPoolLogicalKey
	route         string
	role          hopPoolRole
	node          int
	spec          *hopPoolSpec
	pool          *pool.Pool
	adapterName   string
	members       map[string]member
	carrier       *nodeHealth
	carrierMember member
	direct        bool
	nodeAddress   string
	nodeProto     string
}
type hopProbeTarget struct {
	role          hopPoolRole
	route         string
	key           string
	carrierMember member
	carrier       *nodeHealth
	direct        bool
	nodeAddress   string
	nodeProtocol  string
	hopAddress    string
	protocol      string
	uri           string
}
type hopPoolLifecycle struct {
	mu            sync.Mutex
	mutationMu    sync.Mutex
	pools         map[string]*pool.Pool
	uses          map[hopPoolLogicalKey]*hopPoolUse
	targets       map[*pool.Pool]map[string][]hopProbeTarget
	now           func() time.Time
	onEventMu     sync.RWMutex
	onEvent       func(HopEvent)
	atCapacity    func() bool
	binder        siteBinder
	postKeys      map[string]bool
	pendingForget []string
}

type hopReviveClock struct{ now func() time.Time }

func (c hopReviveClock) Now() time.Time { return c.now() }
func (hopReviveClock) NewTicker(d time.Duration) hoprevive.Ticker {
	return hopReviveTicker{ticker: time.NewTicker(d)}
}

type hopReviveTicker struct{ ticker *time.Ticker }

func (t hopReviveTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t hopReviveTicker) Stop()                  { t.ticker.Stop() }

func newHopPoolLifecycle(onEvent func(HopEvent)) *hopPoolLifecycle {
	return newHopPoolLifecycleWithClock(onEvent, time.Now)
}
func newHopPoolLifecycleWithClock(onEvent func(HopEvent), now func() time.Time) *hopPoolLifecycle {
	if now == nil {
		now = time.Now
	}
	return &hopPoolLifecycle{
		pools: make(map[string]*pool.Pool), uses: make(map[hopPoolLogicalKey]*hopPoolUse),
		targets: make(map[*pool.Pool]map[string][]hopProbeTarget), now: now, onEvent: onEvent,
	}
}
func (m *hopPoolLifecycle) setOnEvent(fn func(HopEvent)) {
	m.onEventMu.Lock()
	m.onEvent = fn
	m.onEventMu.Unlock()
}
func (m *hopPoolLifecycle) emit(event HopEvent) {
	m.onEventMu.RLock()
	fn := m.onEvent
	m.onEventMu.RUnlock()
	if fn != nil {
		fn(event)
	}
}
func hopRoleName(role hopPoolRole) string {
	if role == preHopPool {
		return "pre"
	}
	if role == postHopPool {
		return "post"
	}
	return ""
}
func (m *hopPoolLifecycle) poolLock(*pool.Pool) *sync.Mutex { return &m.mutationMu }

type HopSnapshot = pool.HopSnapshot

func poolState(p *pool.Pool, key string) HopSnapshot {
	for _, state := range p.Snapshot() {
		if state.Key == key {
			return state
		}
	}
	return HopSnapshot{Key: key, State: pool.Healthy}
}
func (m *hopPoolLifecycle) mutate(p *pool.Pool, role hopPoolRole, route, key string, change func()) {
	if p == nil || change == nil {
		return
	}
	lock := m.poolLock(p)
	lock.Lock()
	before := poolState(p, key)
	change()
	after := poolState(p, key)
	lock.Unlock()
	if (string(before.State) == "healthy") == (string(after.State) == "healthy") {
		return
	}
	kind := HopEventQuarantined
	if string(after.State) == "healthy" {
		kind = HopEventRevived
	}
	m.mu.Lock()
	routes := make(map[string]struct{})
	for _, use := range m.uses {
		if use.pool == p && use.role == role {
			if _, ok := use.members[key]; ok {
				routes[use.route] = struct{}{}
			}
		}
	}
	m.mu.Unlock()
	if len(routes) == 0 && route != "" {
		routes[route] = struct{}{}
	}
	ordered := make([]string, 0, len(routes))
	for routeKey := range routes {
		ordered = append(ordered, routeKey)
	}
	sort.Strings(ordered)
	for _, routeKey := range ordered {
		m.emit(HopEvent{Role: hopRoleName(role), RouteKey: routeKey, Key: key, Kind: kind, Trips: after.Trips})
	}
}

func planPool(o Options, plan routePlan, node int, role hopPoolRole) *hopPoolSpec {
	if role == preHopPool {
		if node < len(plan.prePools) {
			return plan.prePools[node]
		}
		return newHopPoolSpec(role, o.nodePrePool(plan.key, node), plan.gen)
	}
	if node < len(plan.postPools) {
		return plan.postPools[node]
	}
	return newHopPoolSpec(role, o.nodePostPool(plan.key, node), plan.gen)
}

func (m *hopPoolLifecycle) prepareUses(o Options, plans []routePlan, proxies map[string]C.Proxy, health *healthTable) ([]*hopPoolUse, error) {
	var uses []*hopPoolUse
	for _, plan := range plans {
		for node := range o.RouteNodes[plan.key] {
			for _, role := range []hopPoolRole{preHopPool, postHopPool} {
				spec := planPool(o, plan, node, role)
				if spec == nil || len(spec.keys) < 2 {
					continue
				}
				members := make(map[string]member, len(spec.keys))
				for _, key := range spec.keys {
					members[key] = nil
				}
				adapterName := spec.name
				if role == postHopPool && node < len(plan.names) {
					adapterName = plan.names[node]
				}
				var carrier *nodeHealth
				if health != nil && (node < len(plan.healthKeys) || node < len(plan.carrierHealthKeys)) {
					carrierKey := ""
					if node < len(plan.healthKeys) {
						carrierKey = plan.healthKeys[node]
					}
					if node < len(plan.carrierHealthKeys) && plan.carrierHealthKeys[node] != "" {
						carrierKey = plan.carrierHealthKeys[node]
					}
					carrier = health.health(carrierKey)
				}
				nodeAddress, nodeProto := "", ""
				if node < len(plan.hops) {
					nodeAddress, nodeProto = plan.hops[node].node.addr, plan.hops[node].node.proto
				}
				baseName := memberProxyName(plan.gen, plan.index, node)
				var carrierMember member
				if role == postHopPool {
					carrierMember = proxies[baseName]
				}
				logical := hopPoolLogicalKey{role: role, route: plan.key, node: node}
				uses = append(uses, &hopPoolUse{
					logical: logical, route: plan.key, role: role, node: node, spec: spec,
					adapterName: adapterName, members: members, carrier: carrier,
					carrierMember: carrierMember, nodeAddress: nodeAddress, nodeProto: nodeProto,
				})
			}
		}
	}
	return uses, nil
}

func (u *hopPoolUse) specProtocol(index int) string {
	if index < 0 || index >= len(u.spec.uris) {
		return ""
	}
	parsed, err := url.Parse(u.spec.uris[index])
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme)
}

func (m *hopPoolLifecycle) install(o Options, plans []routePlan, proxies map[string]C.Proxy, health *healthTable) (map[string]C.Proxy, []string, error) {
	gen := 0
	if len(plans) > 0 {
		gen = plans[0].gen
	}
	return m.installGen(o, plans, proxies, health, gen)
}

func (m *hopPoolLifecycle) installGen(o Options, plans []routePlan, proxies map[string]C.Proxy, health *healthTable, gen int) (map[string]C.Proxy, []string, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	uses, err := m.prepareUses(o, plans, proxies, health)
	if err != nil {
		return nil, nil, err
	}
	directUses, err := directPostPoolUses(o, gen, proxies)
	if err != nil {
		return nil, nil, err
	}
	uses = append(uses, directUses...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.SiteBinder != nil {
		m.binder = o.SiteBinder
	}
	binder := m.binder
	assigned := m.assignPoolsLocked(uses)
	updated := make(map[string]C.Proxy, len(proxies)+len(uses))
	for name, proxy := range proxies {
		updated[name] = proxy
	}
	targets := make(map[*pool.Pool]map[string][]hopProbeTarget)
	nextUses := make(map[hopPoolLogicalKey]*hopPoolUse, len(uses))
	preAdapters := make(map[string]*hopPoolUse)
	var names []string
	for _, use := range uses {
		use.pool = assigned[poolIdentity(use.role, use.spec.keys)]
		nextUses[use.logical] = use
		if targets[use.pool] == nil {
			targets[use.pool] = make(map[string][]hopProbeTarget)
		}
		for index, key := range use.spec.keys {
			hopAddr := ""
			if index < len(use.spec.uris) {
				hopAddr = hopAddress(use.spec.uris[index])
			}
			targets[use.pool][key] = append(targets[use.pool][key], hopProbeTarget{
				role: use.role, route: use.route, key: key, carrierMember: use.carrierMember, carrier: use.carrier,
				direct: use.direct, nodeAddress: use.nodeAddress, nodeProtocol: use.nodeProto,
				hopAddress: hopAddr, protocol: use.specProtocol(index), uri: use.spec.uris[index],
			})
		}
		if use.role == preHopPool && preAdapters[use.spec.name] == nil {
			preAdapters[use.spec.name] = use
		}
	}
	for _, use := range preAdapters {
		p := assigned[poolIdentity(use.role, use.spec.keys)]
		use := use
		pa := newHopPoolAdapter(use.adapterName, preHopPool, use.spec, p, binder, nil, m.now)
		pa.mutate = func(key string, change func()) { m.mutate(p, preHopPool, use.route, key, change) }
		updated[use.adapterName] = adapter.NewProxy(pa)
		names = append(names, use.adapterName)
	}
	for _, use := range uses {
		if use.role != postHopPool {
			continue
		}
		p := use.pool
		use := use
		pa := newHopPoolAdapter(use.adapterName, postHopPool, use.spec, p, binder, use.carrier, m.now)
		for _, hop := range pa.hops {
			hop.nodeAddress, hop.directCarrier, hop.carrierMember = use.nodeAddress, use.direct, use.carrierMember
		}
		pa.mutate = func(key string, change func()) { m.mutate(p, postHopPool, use.route, key, change) }
		updated[use.adapterName] = adapter.NewProxy(pa)
		names = append(names, use.adapterName)
	}
	oldPost, newPost := m.postKeys, postHopKeys(o)
	for key := range oldPost {
		if !newPost[key] {
			m.pendingForget = append(m.pendingForget, key)
		}
	}
	m.postKeys = newPost
	m.uses, m.targets = nextUses, targets
	m.setOnEvent(o.OnHopEvent)
	sort.Strings(m.pendingForget)
	return updated, uniqueSorted(names), nil
}

func postHopKeys(o Options) map[string]bool {
	keys := make(map[string]bool)
	for _, route := range o.AccountRoute {
		nodes := o.RouteNodes[route]
		if len(nodes) == 0 {
			for _, uri := range o.routePostPool(route) {
				if key := hopPoolKey(uri); key != "" {
					keys[key] = true
				}
			}
			continue
		}
		for node := range nodes {
			for _, uri := range o.nodePostPool(route, node) {
				if key := hopPoolKey(uri); key != "" {
					keys[key] = true
				}
			}
		}
	}
	return keys
}

func (m *hopPoolLifecycle) completeInstall(binder siteBinder) {
	m.mu.Lock()
	if binder == nil {
		binder = m.binder
	}
	keys := append([]string(nil), m.pendingForget...)
	m.pendingForget = nil
	m.mu.Unlock()
	if binder != nil {
		for _, key := range keys {
			binder.Forget(key)
		}
	}
}

func (m *hopPoolLifecycle) assignPoolsLocked(uses []*hopPoolUse) map[string]*pool.Pool {
	keysByID, predecessor := make(map[string][]string), make(map[string][]string)
	for _, use := range uses {
		id := poolIdentity(use.role, use.spec.keys)
		keysByID[id] = append([]string(nil), use.spec.keys...)
		if old := m.uses[use.logical]; old != nil {
			predecessor[id] = append(predecessor[id], poolIdentity(old.role, old.spec.keys))
		}
	}
	ids := make([]string, 0, len(keysByID))
	for id := range keysByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	assigned := make(map[string]*pool.Pool, len(ids))
	used := make(map[*pool.Pool]bool)
	for _, id := range ids {
		if p := m.pools[id]; p != nil && !used[p] {
			assigned[id], used[p] = p, true
		}
	}
	for _, id := range ids {
		if assigned[id] != nil {
			continue
		}
		var sources []*pool.Pool
		for _, oldID := range uniqueSorted(predecessor[id]) {
			if candidate := m.pools[oldID]; candidate != nil {
				sources = append(sources, candidate)
			}
		}
		if len(sources) != 0 {
			assigned[id] = pool.ForkFrom(keysByID[id], sources...)
		} else {
			assigned[id] = pool.New(keysByID[id], pool.Options{Clock: m.now})
		}
	}
	for id, p := range assigned {
		p.Reconcile(keysByID[id])
	}
	clear(m.pools)
	for id, p := range assigned {
		m.pools[id] = p
	}
	return assigned
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (i *Instance) SnapshotHopPools() []HopPoolSnapshot {
	if i == nil || i.state().hopPools == nil {
		return nil
	}
	return i.state().hopPools.snapshot()
}
func (m *hopPoolLifecycle) snapshot() []HopPoolSnapshot {
	m.mu.Lock()
	uses := make([]*hopPoolUse, 0, len(m.uses))
	for _, use := range m.uses {
		uses = append(uses, use)
	}
	m.mu.Unlock()
	sort.Slice(uses, func(i, j int) bool {
		if uses[i].route != uses[j].route {
			return uses[i].route < uses[j].route
		}
		if uses[i].role != uses[j].role {
			return uses[i].role < uses[j].role
		}
		return uses[i].node < uses[j].node
	})
	seen := make(map[string]bool)
	var out []HopPoolSnapshot
	for _, use := range uses {
		id := poolIdentity(use.role, use.spec.keys)
		composite := use.route + "\x00" + hopRoleName(use.role) + "\x00" + id
		if seen[composite] {
			continue
		}
		seen[composite] = true
		for _, state := range use.pool.Snapshot() {
			out = append(out, HopPoolSnapshot{Role: hopRoleName(use.role), RouteKey: use.route, PoolIdentity: id, Key: state.Key, State: string(state.State), Sand: state.Sand, Weight: state.Weight, Trips: state.Trips})
		}
	}
	return out
}
func (i *Instance) ExportHopPools() []HopPoolState {
	if i == nil || i.state().hopPools == nil {
		return nil
	}
	return i.state().hopPools.export()
}
func (m *hopPoolLifecycle) export() []HopPoolState {
	m.mu.Lock()
	uses := make([]*hopPoolUse, 0, len(m.uses))
	for _, use := range m.uses {
		uses = append(uses, use)
	}
	m.mu.Unlock()
	sort.Slice(uses, func(i, j int) bool {
		if uses[i].route != uses[j].route {
			return uses[i].route < uses[j].route
		}
		if uses[i].role != uses[j].role {
			return uses[i].role < uses[j].role
		}
		return uses[i].node < uses[j].node
	})
	seen := make(map[string]bool)
	var out []HopPoolState
	for _, use := range uses {
		id := poolIdentity(use.role, use.spec.keys)
		composite := use.route + "\x00" + hopRoleName(use.role) + "\x00" + id
		if seen[composite] {
			continue
		}
		seen[composite] = true
		out = append(out, HopPoolState{Role: hopRoleName(use.role), RouteKey: use.route, PoolIdentity: id, Entries: use.pool.Export()})
	}
	return out
}
func (i *Instance) RestoreHopPools(states []HopPoolState) error {
	if i == nil || i.state().hopPools == nil {
		if len(states) == 0 {
			return nil
		}
		return errors.New("core: unknown hop pool state")
	}
	return i.state().hopPools.restore(states)
}
func (m *hopPoolLifecycle) restore(states []HopPoolState) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	type destination struct {
		pool    *pool.Pool
		allowed map[string]bool
		entries map[string]pool.Entry
	}
	byPool := make(map[*pool.Pool]*destination)
	for _, state := range states {
		role := preHopPool
		if state.Role == "post" {
			role = postHopPool
		} else if state.Role != "pre" {
			return errors.New("core: unknown hop pool state")
		}
		var match *hopPoolUse
		for _, use := range m.uses {
			if use.role == role && use.route == state.RouteKey && poolIdentity(role, use.spec.keys) == state.PoolIdentity {
				match = use
				break
			}
		}
		if match == nil {
			return errors.New("core: unknown hop pool state")
		}
		d := byPool[match.pool]
		if d == nil {
			d = &destination{pool: match.pool, allowed: make(map[string]bool), entries: make(map[string]pool.Entry)}
			for _, key := range match.spec.keys {
				d.allowed[key] = true
			}
			byPool[match.pool] = d
		}
		for _, entry := range state.Entries {
			if !d.allowed[entry.Key] {
				return errors.New("core: unknown hop pool key")
			}
			if old, ok := d.entries[entry.Key]; ok && old.Trips != entry.Trips {
				return errors.New("core: conflicting hop pool state")
			}
			d.entries[entry.Key] = entry
		}
	}
	for _, d := range byPool {
		entries := make([]pool.Entry, 0, len(d.entries))
		for _, entry := range d.entries {
			entries = append(entries, entry)
		}
		d.pool.Restore(entries, m.now())
	}
	return nil
}

func (m *hopPoolLifecycle) baseProbeNames(plans []routePlan) []string {
	var names []string
	for _, plan := range plans {
		for i := range plan.names {
			pre := i < len(plan.prePools) && plan.prePools[i] != nil && len(plan.prePools[i].keys) > 1
			post := i < len(plan.postPools) && plan.postPools[i] != nil && len(plan.postPools[i].keys) > 1
			if pre || post {
				names = append(names, strings.TrimSuffix(plan.names[i], "-post"))
			}
		}
	}
	m.mu.Lock()
	for _, use := range m.uses {
		if use.direct && use.role == postHopPool {
			names = append(names, use.adapterName)
		}
	}
	m.mu.Unlock()
	return uniqueSorted(names)
}

func (m *hopPoolLifecycle) newReviver(options WarmOptions) (*hoprevive.Reviver, error) {
	var err error
	options, err = normalizeWarmOptions(options)
	if err != nil {
		return nil, err
	}
	if options.ProbeTimeout > 20*time.Second {
		options.ProbeTimeout = 20 * time.Second
	}
	m.mu.Lock()
	uses := make([]*hopPoolUse, 0, len(m.uses))
	for _, use := range m.uses {
		uses = append(uses, use)
	}
	targets := make(map[*pool.Pool]map[string][]hopProbeTarget, len(m.targets))
	for p, keyed := range m.targets {
		copyKeyed := make(map[string][]hopProbeTarget, len(keyed))
		for key, list := range keyed {
			copyKeyed[key] = append([]hopProbeTarget(nil), list...)
		}
		targets[p] = copyKeyed
	}
	m.mu.Unlock()
	registrations := make([]hoprevive.Registration, 0, len(m.pools))
	seen := make(map[*pool.Pool]bool)
	for _, use := range uses {
		p := use.pool
		if p == nil || seen[p] {
			continue
		}
		seen[p] = true
		var hops []hopdial.Hop
		for _, key := range use.spec.keys {
			candidates := targets[p][key]
			var selected *mihomoPoolHop
			if target, ok := availableProbeTarget(candidates, m.now()); ok {
				selected, _ = nativeProbeHop(target, m.now)
			}
			if selected == nil {
				for _, candidate := range candidates {
					if native, ok := nativeProbeHop(candidate, m.now); ok {
						selected = native
						break
					}
				}
			}
			if selected == nil {
				return nil, errors.New("core: missing native hop probe target")
			}
			hops = append(hops, selected)
		}
		role, route := use.role, use.route
		registrations = append(registrations, hoprevive.Registration{
			Pool: p, Hops: hops, Prober: lifecyclePoolProber{lifecycle: m, pool: p, options: options},
			Eligible: func(key string) bool {
				_, ok := availableProbeTarget(targets[p][key], m.now())
				return ok && !m.isAtCapacity()
			},
			Mutate: func(key string, change func()) { m.mutate(p, role, route, key, change) },
		})
	}
	return hoprevive.New(registrations, hoprevive.Options{Interval: options.ProbeInterval, Timeout: options.ProbeTimeout, Concurrency: options.ProbeConcurrency, Clock: hopReviveClock{now: m.now}})
}

func (m *hopPoolLifecycle) isAtCapacity() bool {
	m.mu.Lock()
	check := m.atCapacity
	m.mu.Unlock()
	return check != nil && check()
}

type lifecyclePoolProber struct {
	lifecycle *hopPoolLifecycle
	pool      *pool.Pool
	options   WarmOptions
}

func (p lifecyclePoolProber) Probe(ctx context.Context, hop hopdial.Hop) pool.ProbeOutcome {
	if ctx == nil || ctx.Err() != nil || hop == nil || p.lifecycle == nil {
		return pool.ProbeInconclusive
	}
	p.lifecycle.mu.Lock()
	var candidates []hopProbeTarget
	for current, keyed := range p.lifecycle.targets {
		if current == p.pool {
			candidates = append(candidates, keyed[hop.Key()]...)
		}
	}
	p.lifecycle.mu.Unlock()
	target, ok := availableProbeTarget(candidates, p.lifecycle.now())
	if !ok || p.lifecycle.isAtCapacity() || ctx.Err() != nil {
		return pool.ProbeInconclusive
	}
	probeHop, ok := nativeProbeHop(target, p.lifecycle.now)
	if !ok {
		return pool.ProbeInconclusive
	}
	if target.role == postHopPool && !target.direct && !recentNodeEvidence(target.carrier, p.lifecycle.now()) {
		return pool.ProbeInconclusive
	}
	address := target.nodeAddress
	if target.role == postHopPool {
		address, ok = probeURLAddress(p.options.ProbeURL)
		if !ok {
			return pool.ProbeInconclusive
		}
	}
	if validateNativeAddress(address) != nil {
		return pool.ProbeInconclusive
	}
	timeout := p.options.ProbeTimeout
	if timeout <= 0 || timeout > 20*time.Second {
		timeout = 20 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if probeCtx.Err() != nil {
		return pool.ProbeInconclusive
	}
	started := p.lifecycle.now()
	outcome := check.New("tcp", address).Probe(probeCtx, probeHop)
	if ctx.Err() != nil || probeCtx.Err() != nil || p.lifecycle.now().Before(started) || p.lifecycle.isAtCapacity() {
		return pool.ProbeInconclusive
	}
	if target.role == postHopPool && !target.direct && !recentNodeEvidence(target.carrier, p.lifecycle.now()) {
		return pool.ProbeInconclusive
	}
	return outcome
}

func nativeProbeHop(target hopProbeTarget, now func() time.Time) (*mihomoPoolHop, bool) {
	config, err := proxyclient.Parse(target.uri)
	if err != nil {
		return nil, false
	}
	return &mihomoPoolHop{key: target.key, role: target.role, address: target.hopAddress, protocol: target.protocol, proxyConfig: config, carrier: target.carrier, carrierMember: target.carrierMember, directCarrier: target.direct, nodeAddress: target.nodeAddress, now: now}, true
}

func probeURLAddress(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return "", false
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port), true
}

func validateNativeAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("invalid probe address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid probe port")
	}
	return nil
}

func availableProbeTarget(targets []hopProbeTarget, now time.Time) (hopProbeTarget, bool) {
	for _, target := range targets {
		if !validProbeURI(target.uri) {
			continue
		}
		if target.direct {
			return target, true
		}
		if target.nodeAddress == "" || !recentNodeEvidence(target.carrier, now) {
			continue
		}
		if target.role == preHopPool || target.role == postHopPool && target.carrierMember != nil {
			return target, true
		}
	}
	for _, target := range targets {
		if target.role != preHopPool || !validProbeURI(target.uri) || target.nodeAddress == "" {
			continue
		}
		if target.carrier != nil && target.carrier.Down() {
			continue
		}
		return target, true
	}
	return hopProbeTarget{}, false
}

func validProbeURI(raw string) bool {
	if raw == "" {
		return false
	}
	_, err := proxyclient.Parse(raw)
	return err == nil
}

func hopAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "socks5":
			port = "1080"
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
