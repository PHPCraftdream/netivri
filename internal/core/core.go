// Package core embeds Mihomo as a library and exposes it as a local
// dual-protocol (HTTP + SOCKS4/5) listener. See docs/design/mixed-port.md.
//
// With Options.Node set, Start builds a rule-mode config whose MATCH rule
// routes the default route through the selected upstream node (fixed internal
// name "netivri-node"); domain destinations are passed to the node unresolved.
// Account mappings add per-user route groups while keeping the default route
// intact. There is no fallback when a routed node is unreachable. Route
// groups are sticky groups: connections of one account to one site leave
// through the same member while it is healthy (see docs/design/node-stats.md).
// Reconfigure replaces the account routes of a running instance without
// closing its listener or its open connections.
package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/adapter/inbound"
	auth "github.com/metacubex/mihomo/component/auth"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	authStore "github.com/metacubex/mihomo/listener/auth"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/listener/mixed"
	mihomolog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
	logrus "github.com/sirupsen/logrus"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// Options configures Start.
type Options struct {
	HomeDir       string             // required; created if missing, passed to constant.SetHomeDir
	Bind          string             // default "127.0.0.1"
	Port          int                // 0 = random free port
	Authenticator auth.Authenticator // nil = no authentication
	// AllowedClients optionally restricts which client addresses may connect:
	// parsed prefixes of listen.allowed_clients (nil = allow all). It is
	// enforced by the listener's accept loop, before any handshake.
	AllowedClients []netip.Prefix
	// Node optionally routes every connection accepted by the mixed listener
	// through this Mihomo proxy mapping (a Clash `proxies:` entry as produced
	// by internal/subscription). nil keeps Direct mode. The node is fixed for
	// the lifetime of the instance; hot-switching is not supported yet.
	Node map[string]any
	// RouteNodes holds, per opaque route key, the Mihomo proxy mappings of the
	// nodes of that route, in preference order. A route key is 1..128 printable
	// ASCII characters chosen by the caller; it never reaches a rule.
	RouteNodes map[string][]map[string]any
	// RoutePre and RoutePost map opaque route keys to one effective URI string.
	RoutePre  map[string]string
	RoutePost map[string]string
	// GlobalPost chains the MATCH route through one effective URI string.
	GlobalPost string
	// AccountRoute maps an authenticated login to the route key it uses.
	AccountRoute map[string]string
	// Warm optionally enables the background probes of the node proxies and,
	// with PoolSize > 0, a pre-dial pool for the default node (see warm.go).
	// nil disables both.
	Warm *WarmOptions
	// NodeLabels optionally overrides the statistics label of every route
	// node: route key -> one label per node, same order and length as
	// RouteNodes[key]. nil labels a node "r<R>-<N>".
	NodeLabels map[string][]string
	// NodeTiers optionally ranks the nodes of a route: route key -> one tier per
	// node, same order and length as RouteNodes[key], never decreasing. A new
	// binding goes to a node of the first tier (smallest number) that has a node
	// not down, so the later tiers are fallbacks. nil puts all nodes in one tier.
	NodeTiers map[string][]int
	// OnNodeHealth optionally hears every change of a route node's down state
	// (true: it just went down, false: it is back) with the node label. It runs
	// on the goroutine that recorded the measurement, so it must return quickly.
	// nil disables it.
	OnNodeHealth func(label string, down bool)
	// Observer optionally receives probe, dial and throughput measurements
	// of the route nodes. nil disables reporting.
	Observer Observer
	// AffinityTTL is how long an idle site binding is kept before it is
	// dropped. 0 means 24 hours; a negative value is invalid.
	AffinityTTL time.Duration
	// InitialBindings optionally loads persisted bindings into the affinity
	// table after the sticky groups are installed: a record is bound to the
	// member whose label matches within the login's route, keeping bound_at
	// and last_used so the idle TTL continues. Records with an unknown
	// login, an unknown label or an already expired binding are ignored.
	InitialBindings []BindingRecord
}

// Instance is one running proxy listener.
type Instance struct {
	mu     sync.Mutex
	closed bool
	ln     *mixed.Listener
	done   chan struct{} // closed by Close; stops the ctx watcher
	tt     *trackedTunnel
	route  string // "direct" or "node"; immutable after Start publishes the instance
	// poller is nil without account routes; one poller serves every
	// generation, so the byte counts of open connections carry on.
	// Immutable after Start publishes the instance.
	poller *throughputPoller
	// restoredBindings is how many of Options.InitialBindings Start actually
	// installed; immutable after Start publishes the instance.
	restoredBindings int
	// routing is the part Reconfigure replaces; never nil once published.
	routing atomic.Pointer[routingState]
}

// routingState is one generation of the account routes: Start builds
// generation 0 and every Reconfigure the next one.
type routingState struct {
	gen           int
	accountRoutes map[string]string
	warm          *warmer // nil without Options.Warm
	// Sticky-group state, nil without account routes.
	table  *affinityTable
	health *healthTable
	// memberLabels maps this generation's member proxy names to node labels;
	// connLabels also keeps the names of earlier generations that open
	// connections still go through, for Activity and the poller.
	memberLabels map[string]string
	connLabels   map[string]string
	// routeLabels maps a route key to the statistics label per member, for
	// SnapshotBindings.
	routeLabels map[string][]string
	// names are the proxies this generation added to Mihomo's proxy map;
	// prevNames are those of the generation before, still in the map so a
	// connection matched by the previous rules finds its group.
	names, prevNames []string
}

var (
	// lifecycleMu guards Mihomo's process-global state: it is held for the
	// whole of Start (check, config, listen, claim) and the shutdown part of
	// Close. Never taken while an instance lock is held.
	lifecycleMu sync.Mutex
	current     *Instance
	logOnce     sync.Once
	// everApplied is set after the first configuration was applied in this
	// process; guarded by lifecycleMu.
	everApplied bool
)

// silenceMihomoLog keeps Mihomo's logger off stdout: stdout carries only the
// JSON envelope. Even at SILENT level logrus could still be invoked directly,
// so its output is discarded as well.
func silenceMihomoLog() {
	logOnce.Do(func() {
		logrus.SetOutput(io.Discard)
		logrus.SetLevel(logrus.PanicLevel)
		mihomolog.SetLevel(mihomolog.SILENT)
	})
}

// Start applies a minimal Mihomo config with executor listeners disabled
// (all ports set to 0) and opens netivri's own mixed listener. Mihomo holds
// process-global state, so only one Instance may be open at a time; Close it
// before starting another.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.HomeDir == "" {
		return nil, errors.New("core: HomeDir is required")
	}
	// constant.SetHomeDir expects an existing directory.
	if err := os.MkdirAll(o.HomeDir, 0o700); err != nil {
		return nil, err
	}

	if o.AffinityTTL < 0 {
		return nil, nodeInvalid("affinity TTL is negative")
	}

	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if current != nil {
		return nil, errors.New("core: an instance is already running in this process")
	}

	silenceMihomoLog()
	C.SetHomeDir(o.HomeDir)

	raw := config.DefaultRawConfig()
	raw.Mode = tunnel.Direct
	raw.LogLevel = mihomolog.SILENT
	raw.Port, raw.SocksPort, raw.RedirPort, raw.TProxyPort, raw.MixedPort = 0, 0, 0, 0, 0
	raw.ExternalController = ""
	raw.AllowLan = false
	// Nothing is selected or cached in v1: no state database, no geodata
	// updates, nothing to persist across runs.
	raw.Profile.StoreSelected = false
	raw.Profile.StoreFakeIP = false
	raw.GeoAutoUpdate = false
	route := routeDirect
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		return nil, err
	}
	if o.Node != nil || len(o.AccountRoute) != 0 || (o.GlobalPost != "" && !isNoChain(o.GlobalPost)) {
		raw.Mode = tunnel.Rule
		raw.Proxy = proxies
		raw.ProxyGroup = groups
		raw.Rule = rules
		// Rule mode uses MATCH for the default route; per-user route rules
		// take precedence and domain destinations remain unresolved.
		// No rule needs process lookup, so keep it disabled.
		raw.FindProcessMode = process.FindProcessOff
		if o.Node != nil {
			route = routeNode
		}
	}

	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		return nil, err
	}
	// The per-route sticky groups: built after prepareRoutes (the select
	// groups stay in the raw config so the rules validate) and installed
	// into the proxy map after ApplyConfig below.
	plans, err := buildRoutePlans(o)
	if err != nil {
		return nil, err
	}
	var table *affinityTable
	var health *healthTable
	var poller *throughputPoller
	if len(plans) > 0 {
		table = newAffinityTable(nil, o.AffinityTTL)
		health = newHealthTable()
		health.notify = o.OnNodeHealth
	}
	if everApplied {
		// A previous configuration left adapters whose finalizers read the
		// logger level that ApplyConfig is about to rewrite.
		drainFinalizers()
	}
	executor.ApplyConfig(cfg, true)
	everApplied = true

	// ApplyConfig resets the process-global address filter; set ours after
	// it and before the listener opens. A nil rule list means "no rule"
	// (allow all); an empty list refuses everyone and must never be
	// silently widened to allow-all.
	allowed := o.AllowedClients
	if allowed == nil {
		allowed = allowAllIPs
	}
	inbound.SetAllowedIPs(allowed)

	restored := 0
	if len(plans) > 0 {
		// The replaced select groups become garbage; their finalizers are
		// drained by the next Start through drainFinalizers.
		if err := installStickyGroups(plans, table, health, o.Observer); err != nil {
			// The filter was already set; restore allow-all on shutdown.
			executor.Shutdown()
			resetAllowedClients()
			return nil, err
		}
		restored = loadInitialBindings(table, plans, o.AccountRoute, o.InitialBindings)
	}

	bind := o.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(o.Port))
	tt := newTrackedTunnel(tunnel.Tunnel)
	ln, err := mixed.NewWithConfig(
		LC.AuthServer{Enable: true, Listen: addr, AuthStore: authStore.NewAuthStore(o.Authenticator)},
		inbound.NewListenConfig(),
		tt,
		inbound.WithInName("NETIVRI-MIXED"), inbound.WithSpecialRules(""),
	)
	if err != nil {
		executor.Shutdown()
		resetAllowedClients()
		if isAddrInUse(err) {
			return nil, &cli.Error{Code: "PORT_IN_USE", Message: "listen " + addr + ": port is already in use", Exit: cli.ExitConflict}
		}
		return nil, err
	}

	// Started after ApplyConfig (it reads the proxies Mihomo built) and
	// stopped before executor.Shutdown, see Close.
	var warm *warmer
	if o.Warm != nil {
		// Copy: never mutate the caller's WarmOptions.
		wo := *o.Warm
		if len(plans) > 0 {
			// Feed the probe results to the sticky health table and the
			// observer; the caller's own OnProbe, if any, still runs.
			// Down feeds the down state back so probed nodes that stay
			// down are probed with a lengthened interval.
			nameToLabel := memberNameLabels(plans)
			wo.OnProbe = probeHook(nameToLabel, health, o.Observer, o.Warm.OnProbe)
			wo.only = func(name string) bool {
				_, tracked := nameToLabel[name]
				return generatedMemberName(name) && tracked
			}
			wo.Down = func(name string) bool { return health.down(nameToLabel[name]) }
		} else if o.Node != nil || (o.GlobalPost != "" && !isNoChain(o.GlobalPost)) {
			// MATCH traffic leaves through the global post when there is one
			// (behind the default node, if any): probe that whole chain.
			want := nodeName
			if o.GlobalPost != "" && !isNoChain(o.GlobalPost) {
				want = globalPostProxyName(0)
			}
			wo.only = func(name string) bool { return name == want }
		}
		warm, err = startWarm(wo, o.Node)
		if err != nil {
			_ = ln.Close()
			executor.Shutdown()
			resetAllowedClients()
			return nil, err
		}
	}
	if len(plans) > 0 {
		poller = newThroughputPoller(managerSource{statistic.DefaultManager}, health, o.Observer, memberNameLabels(plans), realStickyClock{})
		poller.start()
	}

	inst := &Instance{
		ln:               ln,
		done:             make(chan struct{}),
		route:            route,
		tt:               tt,
		poller:           poller,
		restoredBindings: restored,
	}
	inst.routing.Store(&routingState{
		accountRoutes: copyAccountRoutes(o.AccountRoute),
		warm:          warm,
		table:         table,
		health:        health,
		memberLabels:  memberNameLabels(plans),
		connLabels:    memberNameLabels(plans),
		routeLabels:   routeLabels(plans),
		names:         append(planProxyNames(plans), proxyMappingNames(proxies)...),
	})
	current = inst

	// Optional lifecycle: cancel ctx to stop the instance.
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = inst.Close()
			case <-inst.done:
			}
		}()
	}
	return inst, nil
}

// state returns the current routing generation; an Instance that was never
// published (tests) has an empty one.
func (i *Instance) state() *routingState {
	if st := i.routing.Load(); st != nil {
		return st
	}
	return &routingState{}
}

// Addr returns the actual host:port the listener is bound to.
func (i *Instance) Addr() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ln == nil {
		return ""
	}
	return i.ln.Address()
}

// Route reports how traffic leaves this instance: "direct" or "node".
func (i *Instance) Route() string {
	return i.route
}

// WarmStats returns the warm-up counters and node health, and false when the
// instance runs without Options.Warm.
func (i *Instance) WarmStats() (WarmStats, bool) {
	warm := i.state().warm
	if warm == nil {
		return WarmStats{}, false
	}
	return warm.Stats(), true
}

// AffinityStats returns the live number of site bindings and the rebind
// counter; it is safe to call while the instance runs.
func (i *Instance) AffinityStats() AffinityStats {
	table := i.state().table
	if table == nil {
		return AffinityStats{}
	}
	return AffinityStats{
		Bindings:  table.count(),
		Rebinds:   table.rebindCount(),
		Hedges:    table.hedgeCount(),
		HedgeWins: table.hedgeWinCount(),
	}
}

// RestoredBindings reports how many persisted bindings Start loaded into
// the affinity table: records with an unknown login or label, an expired
// binding or a duplicate key are not counted.
func (i *Instance) RestoredBindings() int {
	return i.restoredBindings
}

// AccountRoutes returns a copy of the configured login-to-route-key routes.
func (i *Instance) AccountRoutes() map[string]string {
	return copyAccountRoutes(i.state().accountRoutes)
}

// MemberLabels returns a copy of the route member proxy name to node label
// mapping used for Activity; empty when the instance runs without routes.
func (i *Instance) MemberLabels() map[string]string {
	labels := i.state().memberLabels
	out := make(map[string]string, len(labels))
	for name, label := range labels {
		out[name] = label
	}
	return out
}

// Close stops the listener and the embedded core. Idempotent.
func (i *Instance) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	ln := i.ln
	i.ln = nil
	i.mu.Unlock()

	// Instance locks are never held while waiting for lifecycleMu.
	lifecycleMu.Lock()
	if ln != nil {
		_ = ln.Close()
	}
	// No connection may outlive the instance: close the tracked ones and
	// wait for their handlers before Mihomo's global state is torn down.
	i.tt.closeAll(closeDrainTimeout)
	// The throughput poller reads Mihomo's connection trackers: stop it
	// before Mihomo's global state is torn down.
	i.poller.stop()
	// The probes and the pool dial through Mihomo's proxies: stop them first.
	i.state().warm.Close()
	executor.Shutdown()
	// Allow-all again: the filter is process-global state.
	resetAllowedClients()
	if current == i {
		current = nil // claim released only after the core is fully down
	}
	lifecycleMu.Unlock()

	close(i.done)
	return nil
}
