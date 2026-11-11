package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/stats"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

// watchEvery is how often the angel looks at the configuration files.
const watchEvery = 2 * time.Second

// coreInstance is what the angel needs from a running core; tests start
// fakes through coreStart.
type coreInstance interface {
	Addr() string
	Route() string
	AccountRoutes() map[string]string
	WarmStats() (core.WarmStats, bool)
	AffinityStats() core.AffinityStats
	Activity() []core.AccountActivity
	MemberLabels() map[string]string
	SnapshotBindings() []core.BindingRecord
	RestoredBindings() int
	// ProbeNow probes every node again at once; see core.Instance.ProbeNow.
	ProbeNow()
	// Reconfigure takes new routes without closing the listener; see
	// core.Instance.Reconfigure.
	Reconfigure(o core.Options) error
	Close() error
}

var coreStart = func(ctx context.Context, o core.Options) (coreInstance, error) {
	inst, err := core.Start(ctx, o)
	if err != nil {
		return nil, err
	}
	return inst, nil
}

// A port that the closed instance has not released yet is retried this many
// times, this far apart.
const portRetries = 10

var portRetryWait = 100 * time.Millisecond

// desiredState is everything the running core is built from.
type desiredState struct {
	cfg     *config.Config
	bind    string
	port    int
	pool    routePool
	servers []servers.Spec
}

// liveAngel keeps the running core in step with the files in the state
// directory (docs/design/live-reload.md). New nodes and routes go into the
// running core and keep its open connections; a change of the listener or
// the accounts closes the listener and starts a new one on the same port.
// Either happens only when something the core is built from really changed.
type liveAngel struct {
	opts upOptions
	logf func(format string, args ...any) // INFO
	// warnf and errorf write the WARN and ERROR lines; they default to logf.
	warnf  func(format string, args ...any)
	errorf func(format string, args ...any)
	// debugf writes the DEBUG lines (hidden by `logs` unless asked); it
	// defaults to logf.
	debugf func(format string, args ...any)
	// conns reports what happens to the connections of the accounts; see
	// connLog().
	conns     *connLog
	connsOnce sync.Once
	nodeMu    sync.Mutex
	nodeLog   *nodeLog
	// sleeper notices that the computer slept; made on first use.
	sleeper     *sleepWatch
	sleeperOnce sync.Once
	stop        func() // asks the angel to shut down; idempotent

	reloadMu sync.Mutex // one reload at a time, held across a restart

	// Lock order: mu, then persistMu.
	mu         sync.Mutex
	closed     bool
	inst       coreInstance
	applied    desiredState   // what the running core was built from
	latest     *config.Config // newest valid file: subscriptions and refresh interval
	startOpts  core.Options
	store      *stats.Store
	actualPort int

	reloads     uint64
	lastReload  time.Time
	reloadErr   string
	refreshOK   uint64
	refreshFail uint64
	lastRefresh time.Time
	refreshErr  string
	// refreshRuns tracks the failed refreshes in a row by subscription.
	refreshRuns map[string]*refreshRun
	// refreshClock is the clock of the refresh log; nil is time.Now.
	refreshClock func() time.Time
	loopsCancel  context.CancelFunc
	loopsRunning sync.WaitGroup
	// watchTickC replaces the real ticker when a test needs deterministic polls.
	watchTickC    <-chan time.Time
	watchPollDone chan struct{}
	retryCancel   context.CancelFunc
	retryRunning  sync.WaitGroup
	retryWait     func(context.Context, time.Duration) bool
	retryApplied  chan struct{}
	retryTarget   *desiredState
	retryDesired  string
	retryOpts     *core.Options
	retryNodeLog  *nodeLog
	retryFailures int

	// Persisted site bindings: when the stored set was last written, how
	// many bindings the latest core start loaded, and the test seams of the
	// writer loop (an injectable interval, tick channel and write call).
	// bindingsGen is bumped under l.mu whenever an instance is finally
	// written (a reload or shutdown), which retires every periodic writer
	// that captured an earlier (instance, generation) pair; bindingsMu
	// orders the final write after one that is already in flight. Lock
	// order: l.mu, then bindingsMu; the periodic writer never holds both.
	bindingsSavedAt  time.Time
	bindingsRestored int
	bindingInterval  time.Duration
	bindingTickerC   <-chan time.Time
	bindingsMu       sync.Mutex
	bindingsGen      atomic.Uint64
	writeBindings    func(*stats.Store, []stats.Binding) error

	hopBinderOnce sync.Once
	hopBinder     *hoppool.Binder
	hopHealthOnce sync.Once
	hopMu         sync.Mutex
	hopHealth     *hopHealth

	serverTickerC   <-chan time.Time
	serverLogMu     sync.Mutex
	serverPrevDrops map[string]uint64
}

func newLiveAngel(opts upOptions, logf func(format string, args ...any), stop func()) *liveAngel {
	l := &liveAngel{opts: opts, logf: logf, warnf: logf, errorf: logf, debugf: logf, stop: stop}
	l.writeBindings = func(store *stats.Store, rows []stats.Binding) error {
		return store.ReplaceBindings(rows)
	}
	return l
}

// errText is the one-line form of an error kept in the status answer and the
// angel log: the stable code and message, never a URL or a credential.
func errText(err error) string {
	var ce *cli.Error
	if errors.As(err, &ce) {
		return ce.Code + ": " + ce.Message
	}
	return err.Error()
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}

func scalarHopMap(in map[string]config.HopList) map[string]string {
	out := make(map[string]string, len(in))
	for key, hops := range in {
		value := ""
		if len(hops) == 1 {
			value = hops[0]
		}
		out[key] = value
	}
	return out
}

func poolHopMap(in map[string]config.HopList) map[string][]string {
	out := make(map[string][]string)
	for key, hops := range in {
		if len(hops) > 1 {
			out[key] = append([]string(nil), hops...)
		}
	}
	return out
}

func scalarNodeHopMap(in map[string][]config.HopList) map[string][]string {
	out := make(map[string][]string, len(in))
	for key, lists := range in {
		values := make([]string, len(lists))
		for i, hops := range lists {
			if len(hops) == 1 {
				values[i] = hops[0]
			}
		}
		out[key] = values
	}
	return out
}

func nodePoolHopMap(in map[string][]config.HopList, routePools map[string]config.HopList) map[string][][]string {
	out := make(map[string][][]string)
	for key, lists := range in {
		needsPool := len(routePools[key]) > 1
		for _, hops := range lists {
			if len(hops) > 1 {
				needsPool = true
				break
			}
		}
		if !needsPool {
			continue
		}
		values := make([][]string, len(lists))
		for i, hops := range lists {
			values[i] = append([]string(nil), hops...)
		}
		out[key] = values
	}
	return out
}

func warmOptionsForPool(pool routePool) *core.WarmOptions {
	if options := warmOptions(pool.nodes); options != nil {
		return options
	}
	for _, route := range pool.accountRoutes {
		if len(pool.nodes[route]) == 0 && len(pool.routePost[route]) > 1 {
			return &core.WarmOptions{ConfirmProbe: true}
		}
	}
	return nil
}

func (l *liveAngel) coreOptions(d desiredState, a mihauth.Authenticator, store *stats.Store) core.Options {
	o, _ := l.coreOptionsChecked(d, a, store)
	return o
}

func (l *liveAngel) coreOptionsChecked(d desiredState, a mihauth.Authenticator, store *stats.Store) (core.Options, error) {
	o, _, err := l.coreOptionsWithLogger(d, a, store)
	return o, err
}

func (l *liveAngel) coreOptionsWithLogger(d desiredState, a mihauth.Authenticator, store *stats.Store) (core.Options, *nodeLog, error) {
	return l.coreOptionsWithLoggerChecked(d, a, store)
}

func (l *liveAngel) coreOptionsWithLoggerChecked(d desiredState, a mihauth.Authenticator, store *stats.Store) (core.Options, *nodeLog, error) {
	o, nodesLog, err := l.routeOptionsWithLogger(d, a, store)
	if err != nil {
		return core.Options{}, nil, err
	}
	o.InitialBindings = initialBindings(store, l.warnf)
	return o, nodesLog, nil
}

// routeOptions is coreOptions without the stored bindings, which only a
// starting core reads.
func (l *liveAngel) routeOptions(d desiredState, a mihauth.Authenticator, store *stats.Store) core.Options {
	o, _, _ := l.routeOptionsWithLogger(d, a, store)
	return o
}

func (l *liveAngel) routeOptionsWithLogger(d desiredState, a mihauth.Authenticator, store *stats.Store) (core.Options, *nodeLog, error) {
	return l.routeOptionsWithLoggerChecked(d, a, store)
}

func (l *liveAngel) routeOptionsWithLoggerChecked(d desiredState, a mihauth.Authenticator, store *stats.Store) (core.Options, *nodeLog, error) {
	pre := scalarHopMap(d.pool.routePre)
	post := scalarHopMap(d.pool.routePost)
	routePrePool := poolHopMap(d.pool.routePre)
	routePostPool := poolHopMap(d.pool.routePost)
	nodePre := scalarNodeHopMap(d.pool.nodePre)
	nodePost := scalarNodeHopMap(d.pool.nodePost)
	nodePrePool := nodePoolHopMap(d.pool.nodePre, d.pool.routePre)
	nodePostPool := nodePoolHopMap(d.pool.nodePost, d.pool.routePost)
	globalPost := ""
	// GlobalPost feeds only MATCH traffic; with account-only authentication it is unreachable.
	if len(d.cfg.Chain.PostSOCKS5) <= 1 {
		if len(d.cfg.Chain.PostSOCKS5) == 1 {
			globalPost = d.cfg.Chain.PostSOCKS5[0]
		}
	} else if err := config.ValidateGlobalPostFallback(d.cfg); err == nil {
		// Keep account inheritance in RoutePost/NodePost, but don't configure an unused fallback pool.
		globalPost = ""
	} else {
		return core.Options{}, nil, err
	}
	var observer core.Observer
	if store != nil {
		observer = statsObserver{store: store}
	}
	l.connLog().setNodes(d.pool.infos)
	nodesLog := makeNodeHealthLogger(d.pool.infos, d.pool.firstTierLabels(), l.sleep().quiet, l.debugf, l.logf, l.warnf, time.Now)
	var nodeHook func(core.NodeEvent)
	if nodesLog != nil {
		nodesLog.asleep = l.sleep().asleep
		nodeHook = nodesLog.event
	}
	optionAuth := a
	if logged, ok := a.(*loggedAuth); !ok || logged.log != l.connLog() {
		optionAuth = l.connLog().wrapAuth(a)
	}
	o := core.Options{
		HomeDir:               l.opts.Home,
		Bind:                  d.bind,
		Port:                  d.port,
		Authenticator:         optionAuth,
		AllowedClients:        config.AllowedClientPrefixes(d.cfg.Listen.AllowedClients),
		RouteNodes:            d.pool.nodes,
		AccountRoute:          d.pool.accountRoutes,
		RoutePre:              pre,
		RoutePost:             post,
		RoutePrePool:          routePrePool,
		RoutePostPool:         routePostPool,
		NodePre:               nodePre,
		NodePost:              nodePost,
		NodePrePool:           nodePrePool,
		NodePostPool:          nodePostPool,
		GlobalPost:            globalPost,
		Servers:               cloneServerSpecs(d.servers),
		AccountAllowedClients: accountAllowedClients(d.cfg),
		NodeLabels:            d.pool.labels,
		NodeTiers:             d.pool.tiers,
		OnConnEvent:           l.connLog().event,
		OnNodeHealth:          nodeHook,
		Observer:              observer,
		Warm:                  warmOptionsForPool(d.pool),
		AffinityTTL:           stats.BindingTTL,
	}
	l.configureHopOptions(&o)
	return o, nodesLog, nil
}

func (l *liveAngel) activeNodeLogger() *nodeLog {
	l.nodeMu.Lock()
	defer l.nodeMu.Unlock()
	return l.nodeLog
}

func (l *liveAngel) installNodeLogger(next *nodeLog) {
	l.nodeMu.Lock()
	defer l.nodeMu.Unlock()
	if l.nodeLog != nil && l.nodeLog != next {
		l.nodeLog.flushNow()
	}
	l.nodeLog = next
}

func (l *liveAngel) flushNodeLogger() {
	l.nodeMu.Lock()
	defer l.nodeMu.Unlock()
	if l.nodeLog != nil {
		l.nodeLog.flush()
	}
}

// start opens the first core instance.
func (l *liveAngel) start(ctx context.Context, d desiredState, a mihauth.Authenticator, store *stats.Store) error {
	o, nodesLog, err := l.coreOptionsWithLogger(d, a, store)
	if err != nil {
		return err
	}
	health := l.prepareHopStart(&o, false)
	inst, err := coreStart(ctx, o)
	if err != nil {
		health.RejectPending()
		health.Close()
		return err
	}
	// A random port (0) becomes the real one, so that a later restart and the
	// fingerprint keep it.
	port := portOf(inst.Addr())
	d.port, o.Port = port, port
	l.mu.Lock()
	l.inst, l.applied, l.latest, l.startOpts, l.store = inst, d, d.cfg, o, store
	l.actualPort = port
	l.noteBindingsRestored(inst)
	l.mu.Unlock()
	l.attachHopHealth(inst)
	l.installNodeLogger(nodesLog)
	l.logServerDelta(nil, d.servers)
	return nil
}

// load reads the configuration and the stored snapshots the way `up` does and
// validates the result. Nothing is applied.
func (l *liveAngel) load() (desiredState, error) {
	cfg, err := loadConfig(l.opts.Home)
	if err != nil {
		return desiredState{}, err
	}
	bind, port, err := mergeUp(l.opts, cfg)
	if err != nil {
		return desiredState{}, err
	}
	pool, err := accountRoutePool(l.opts.Home, cfg)
	if err != nil {
		return desiredState{}, err
	}
	resolved, err := cfg.ResolveServers()
	if err != nil {
		return desiredState{}, err
	}
	store := servers.NewStore(l.opts.Home)
	for i := range resolved {
		resolved[i], err = store.EnsureSecrets(resolved[i])
		if err != nil {
			return desiredState{}, err
		}
	}
	return desiredState{cfg: cfg, bind: bind, port: port, pool: pool, servers: resolved}, nil
}

// nodesWithoutNames drops the display name of every node: the core replaces
// it with its own, and providers often put changing text (traffic left, the
// date) into it, which must not restart the listener.
func nodesWithoutNames(routes map[string][]map[string]any) map[string][]map[string]any {
	out := make(map[string][]map[string]any, len(routes))
	for key, nodes := range routes {
		list := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			c := make(map[string]any, len(n))
			for k, v := range n {
				if k != "name" {
					c[k] = v
				}
			}
			list = append(list, c)
		}
		out[key] = list
	}
	return out
}

// fingerprint identifies all settings a core is built from. Settings the core
// does not read (subscription URLs and refresh interval) are left out.
func fingerprint(d desiredState) string {
	return listenerFingerprint(d) + accountClientsFingerprint(d.cfg) + routesFingerprint(d) + serverFingerprint(d.servers)
}

// routesFingerprint covers nodes, labels, tiers, account routes and SOCKS5
// chains. Route-specific chains are applied by Reconfigure; global post is
// included because it affects core construction and requires a restart.
func routesFingerprint(d desiredState) string {
	v := struct {
		Nodes      map[string][]map[string]any
		Labels     map[string][]string
		Tiers      map[string][]int
		Routes     map[string]string
		Exit       []struct{ Login, Exit string }
		Pre        map[string]config.HopList
		Post       map[string]config.HopList
		NodePre    map[string][]config.HopList
		NodePost   map[string][]config.HopList
		GlobalPost config.HopList
	}{
		Nodes: nodesWithoutNames(d.pool.nodes), Labels: d.pool.labels, Tiers: d.pool.tiers,
		Routes: d.pool.accountRoutes, Pre: d.pool.routePre, Post: d.pool.routePost, NodePre: d.pool.nodePre, NodePost: d.pool.nodePost,
		Exit: normalizedExitFingerprint(d.cfg.Accounts), GlobalPost: d.cfg.Chain.PostSOCKS5,
	}
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizedExitFingerprint(accounts []config.Account) []struct{ Login, Exit string } {
	out := make([]struct{ Login, Exit string }, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, struct{ Login, Exit string }{a.Login, func() string {
			if a.Exit == "direct" {
				return "direct"
			}
			return "route"
		}()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}

// listenerFingerprint covers the listener: address, allowed clients and the
// credentials. A change here needs a new core.
func listenerFingerprint(d desiredState) string {
	type acct struct{ Login, Password string }
	v := struct {
		Bind, AuthLogin, AuthPassword string
		Port                          int
		AllowedClients                string
		Accounts                      []acct
	}{Bind: d.bind, Port: d.port, AllowedClients: config.NormalizeAllowedClients(d.cfg.Listen.AllowedClients)}
	// The persist callbacks rewrite credentials of this config in place.
	persistMu.Lock()
	v.AuthLogin, v.AuthPassword = d.cfg.Auth.Login, d.cfg.Auth.Password
	for _, a := range d.cfg.Accounts {
		v.Accounts = append(v.Accounts, acct{a.Login, a.Password})
	}
	persistMu.Unlock()
	sort.Slice(v.Accounts, func(i, j int) bool { return v.Accounts[i].Login < v.Accounts[j].Login })
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func accountClientsFingerprint(cfg *config.Config) string {
	type entry struct{ Login, Clients string }
	entries := make([]entry, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		entries = append(entries, entry{a.Login, config.NormalizeAllowedClients(a.AllowedClients)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Login < entries[j].Login })
	data, _ := json.Marshal(entries)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func updateAccountPolicy(a mihauth.Authenticator, cfg *config.Config) {
	switch v := a.(type) {
	case *loggedAuth:
		updateAccountPolicy(v.inner, cfg)
	case *accountPolicyAuthenticator:
		v.UpdateConfig(cfg)
	}
}

func scalarGlobalPost(hops config.HopList) string {
	if len(hops) == 1 {
		return hops[0]
	}
	return ""
}

func globalPostFallbackReachable(cfg *config.Config) bool {
	return cfg == nil || len(cfg.Accounts) == 0 || cfg.Auth.Login != "" || cfg.Auth.Password != ""
}

func reloadComparison(a mihauth.Authenticator, applied, candidate desiredState, since uint64) (desiredState, bool, bool) {
	applied.cfg, candidate.cfg = snapshotReloadCredentials(a, applied.cfg, candidate.cfg, since)
	same := fingerprint(applied) == fingerprint(candidate)
	globalPostChanged := scalarGlobalPost(applied.cfg.Chain.PostSOCKS5) != scalarGlobalPost(candidate.cfg.Chain.PostSOCKS5) &&
		(globalPostFallbackReachable(applied.cfg) || globalPostFallbackReachable(candidate.cfg))
	inPlace := !same && !globalPostChanged && listenerFingerprint(applied) == listenerFingerprint(candidate) &&
		(((len(applied.pool.accountRoutes) > 0) == (len(candidate.pool.accountRoutes) > 0)) || routesFingerprint(applied) == routesFingerprint(candidate) || (accountClientsFingerprint(applied.cfg) != accountClientsFingerprint(candidate.cfg)))
	return candidate, same, inPlace
}

// watchRetryLimit bounds reload attempts for one unchanged file signature.
// A later file change starts a fresh retry budget.
const watchRetryLimit = 3

type reloadOutcome uint8

const (
	reloadHandled reloadOutcome = iota
	reloadRetry
)

// reload applies the files on disk when they differ from what runs. A file
// that does not load, or routes that cannot be built, leave the running core
// untouched and show up as reload.last_error. reloadRetry means the replacement
// returned PORT_IN_USE after its bounded immediate retries but the previous core
// was restored, so the watcher may retry.
func (l *liveAngel) reload(ctx context.Context) reloadOutcome {
	l.reloadMu.Lock()
	defer l.reloadMu.Unlock()
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return reloadHandled
	}
	l.mu.Lock()
	claimAuth := l.startOpts.Authenticator
	l.mu.Unlock()
	claimGeneration := persistClaimGeneration(claimAuth)
	d, err := l.load()
	if err != nil {
		l.noteReloadError("configuration not applied", err)
		return reloadHandled
	}
	l.mu.Lock()
	if d.port == 0 {
		d.port = l.actualPort
	}
	d, same, inPlace := reloadComparison(l.startOpts.Authenticator, l.applied, d, claimGeneration)
	l.latest = d.cfg
	instDown := l.inst == nil
	if same && !instDown {
		l.reloadErr = ""
	}
	l.mu.Unlock()
	if instDown {
		l.mu.Lock()
		unchangedFailed := l.retryDesired == fingerprint(d)
		l.mu.Unlock()
		if unchangedFailed {
			return reloadHandled
		}
		return l.restart(ctx, d)
	}
	switch {
	case same:
	case inPlace && l.reconfigureSince(d, claimGeneration):
	default:
		return l.restart(ctx, d)
	}
	return reloadHandled
}

type queuedNodeEvents struct {
	mu      sync.Mutex
	target  func(core.NodeEvent)
	pending []core.NodeEvent
	ready   bool
}

func (q *queuedNodeEvents) event(ev core.NodeEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.ready {
		q.pending = append(q.pending, ev)
		return
	}
	if q.target != nil {
		q.target(ev)
	}
}

func (q *queuedNodeEvents) activate() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ev := range q.pending {
		if q.target != nil {
			q.target(ev)
		}
	}
	q.pending = nil
	q.ready = true
}

func (q *queuedNodeEvents) discard() {
	q.mu.Lock()
	q.pending = nil
	q.target = nil
	q.ready = true
	q.mu.Unlock()
}

// reconfigure gives the running core new routes without closing its
// listener, so open connections go on and the nodes that stay keep their
// health and bindings. False means the core could not take them in place and
// must be restarted instead.
func (l *liveAngel) reconfigure(d desiredState) bool {
	l.mu.Lock()
	auth := l.startOpts.Authenticator
	l.mu.Unlock()
	return l.reconfigureSince(d, persistClaimGeneration(auth))
}

func (l *liveAngel) reconfigureSince(d desiredState, claimGeneration uint64) bool {
	l.mu.Lock()
	inst, store, auth := l.inst, l.store, l.startOpts.Authenticator
	l.mu.Unlock()
	if inst == nil {
		return false
	}
	l.mu.Lock()
	oldServers := cloneServerSpecs(l.applied.servers)
	serverChanged := serverFingerprint(oldServers) != serverFingerprint(d.servers)
	serverOnly := serverChanged && routesFingerprint(l.applied) == routesFingerprint(d)
	policyOnly := accountClientsFingerprint(l.applied.cfg) != accountClientsFingerprint(d.cfg) && routesFingerprint(l.applied) == routesFingerprint(d) && !serverChanged
	if len(d.pool.accountRoutes) == 0 && !serverOnly && !policyOnly {
		l.mu.Unlock()
		return false
	}
	// Core route reconfiguration requires a live node table/poller, not merely
	// account route entries. Keep policy/server-only cases independent.
	if (len(d.pool.nodes) == 0 || len(l.applied.pool.nodes) == 0) && !serverOnly && !policyOnly {
		l.mu.Unlock()
		return false
	}
	l.mu.Unlock()
	if len(d.pool.accountRoutes) == 0 && policyOnly && len(d.servers) == 0 && len(oldServers) == 0 {
		// Direct mode has no core route/server policy to reconfigure; the
		// stable authenticator is the policy enforcement point.
		l.mu.Lock()
		if l.closed || l.inst != inst {
			l.mu.Unlock()
			return true
		}
		updateAccountPolicy(auth, d.cfg)
		rebindPersistConfigSince(auth, d.cfg, claimGeneration)
		d.port = l.actualPort
		l.startOpts.AccountAllowedClients = accountAllowedClients(d.cfg)
		l.applied, l.latest = d, d.cfg
		l.reloads++
		l.lastReload, l.reloadErr = time.Now(), ""
		l.mu.Unlock()
		return true
	}
	if store != nil && len(d.pool.infos) > 0 {
		if err := store.SetNodes(d.pool.infos); err != nil {
			l.warnf("statistics: node details not stored: %v", err)
		}
	}
	o, nodesLog, err := l.routeOptionsWithLogger(d, auth, store)
	if err != nil {
		l.noteReloadError("routes not applied", err)
		return false
	}
	activeNodeLog := l.activeNodeLogger()
	var queuedEvents *queuedNodeEvents
	if activeNodeLog != nil && len(d.pool.infos) > 0 {
		if serverOnly {
			o.OnNodeHealth = activeNodeLog.event
		} else {
			queuedEvents = &queuedNodeEvents{target: activeNodeLog.event}
			o.OnNodeHealth = queuedEvents.event
		}
	}
	if err := inst.Reconfigure(o); err != nil {
		l.rejectHopHealth()
		if queuedEvents != nil {
			queuedEvents.discard()
		}
		if serverOnly && !errors.Is(err, core.ErrReconfigureUnsupported) {
			l.noteReloadError("server configuration not applied", err)
			return true
		}
		if !errors.Is(err, core.ErrReconfigureUnsupported) {
			l.warnf("routes not updated in place, restarting the listener: %s", errText(err))
		}
		return false
	}
	l.mu.Lock()
	if l.closed || l.inst != inst {
		l.mu.Unlock()
		l.rejectHopHealth()
		if queuedEvents != nil {
			queuedEvents.discard()
		}
		return true
	}
	o.Port, d.port = l.actualPort, l.actualPort
	updateAccountPolicy(o.Authenticator, d.cfg)
	rebindPersistConfigSince(o.Authenticator, d.cfg, claimGeneration)
	l.applied, l.latest, l.startOpts = d, d.cfg, o
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.mu.Unlock()
	l.activateHopHealth()
	if activeNodeLog != nil {
		activeNodeLog.refresh(d.pool.infos, d.pool.firstTierLabels())
		if len(d.pool.infos) == 0 {
			l.installNodeLogger(nil)
		}
	} else {
		l.installNodeLogger(nodesLog)
	}
	if queuedEvents != nil {
		queuedEvents.activate()
	}
	l.logServerDelta(oldServers, d.servers)
	l.logf("configuration applied, connections kept: routes=%d accounts=%d", len(d.pool.accountRoutes), len(d.cfg.Accounts))
	return true
}

func (l *liveAngel) noteReloadError(what string, err error) {
	text := errText(err)
	l.mu.Lock()
	l.reloadErr = text
	l.mu.Unlock()
	l.warnf("%s: %s", what, text)
}

// startCore starts a core instance, retrying briefly while the port of the
// instance that was just closed is still being released.
func startCore(ctx context.Context, o core.Options) (coreInstance, error) {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inst, err := coreStart(ctx, o)
		var ce *cli.Error
		if err == nil || !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" || attempt == portRetries {
			return inst, err
		}
		t := time.NewTimer(portRetryWait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// restart replaces the core. Open connections end with it; the caller only
// gets here when the fingerprint changed. A failed start of the new core
// brings the previous one back.
func (l *liveAngel) restart(ctx context.Context, d desiredState) reloadOutcome {
	a, err := newAuthenticator(d.cfg, l.opts.Home)
	if err != nil {
		l.noteReloadError("configuration not applied", err)
		return reloadHandled
	}
	l.mu.Lock()
	store, prev, old := l.store, l.startOpts, l.inst
	l.mu.Unlock()
	oldNodeLog := l.activeNodeLogger()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return reloadHandled
	}
	l.retryDesired = fingerprint(d)
	oldServers := cloneServerSpecs(l.applied.servers)
	wasDown := old == nil
	if old != nil {
		l.writeBindingsFinalLocked(old, store)
	}
	l.inst = nil
	l.mu.Unlock()
	if old != nil {
		l.detachHopHealth()
		_ = old.Close()
	}
	if len(d.pool.infos) > 0 {
		if store == nil {
			store = openStats(l.opts.Home, d.pool.infos, l.warnf)
		} else if err := store.SetNodes(d.pool.infos); err != nil {
			l.warnf("statistics: node details not stored: %v", err)
		}
	}
	o, nodesLog, err := l.coreOptionsWithLogger(d, a, store)
	if err != nil {
		l.noteReloadError("configuration not applied", err)
		return reloadHandled
	}
	l.mu.Lock()
	l.store = store
	l.mu.Unlock()
	l.prepareHopStart(&o, true)
	inst, err := startCore(ctx, o)
	if err != nil {
		l.rejectHopHealth()
		text := errText(err)
		if wasDown {
			l.mu.Lock()
			l.retryTarget, l.retryOpts, l.retryNodeLog = cloneDesired(&d), &o, nodesLog
			l.retryFailures = 0
			l.reloadErr = text
			l.mu.Unlock()
			l.warnf("configuration restart failed; retrying: %s", text)
			l.startRetry()
			return reloadHandled
		}
		l.mu.Lock()
		l.reloadErr = text
		l.mu.Unlock()
		l.warnf("new configuration not applied, restoring the previous one: %s", text)
		back := prev
		back.InitialBindings = initialBindings(store, l.warnf)
		l.prepareHopStart(&back, true)
		backInst, err2 := startCore(ctx, back)
		if err2 != nil {
			l.rejectHopHealth()
			l.mu.Lock()
			l.retryTarget, l.retryOpts, l.retryNodeLog = cloneDesired(&l.applied), &back, oldNodeLog
			l.retryFailures = 0
			l.reloadErr = text + "; previous configuration could not be restored: " + errText(err2)
			l.mu.Unlock()
			l.warnf("previous configuration could not be restarted; retrying: %s", errText(err2))
			l.startRetry()
			return reloadHandled
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			l.rejectHopHealth()
			_ = backInst.Close()
			return reloadHandled
		}
		l.inst, l.startOpts = backInst, back
		l.noteBindingsRestored(backInst)
		l.mu.Unlock()
		l.attachHopHealth(backInst)
		l.installNodeLogger(oldNodeLog)
		var ce *cli.Error
		if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
			return reloadRetry
		}
		return reloadHandled
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.rejectHopHealth()
		_ = inst.Close()
		return reloadHandled
	}
	l.inst, l.applied, l.latest, l.startOpts = inst, d, d.cfg, o
	l.retryTarget, l.retryOpts, l.retryNodeLog, l.retryDesired = nil, nil, nil, ""
	l.noteBindingsRestored(inst)
	l.actualPort = portOf(inst.Addr())
	l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.mu.Unlock()
	l.attachHopHealth(inst)
	l.installNodeLogger(nodesLog)
	l.logf("configuration applied: routes=%d accounts=%d", len(d.pool.accountRoutes), len(d.cfg.Accounts))
	l.logServerDelta(oldServers, d.servers)
	return reloadHandled
}

func cloneDesired(d *desiredState) *desiredState {
	c := *d
	return &c
}

const retryBase = time.Second
const retryMax = 30 * time.Second

func retryBackoff(failures int) time.Duration {
	d := retryBase
	for i := 1; i < failures && d < retryMax; i++ {
		d *= 2
	}
	if d > retryMax {
		return retryMax
	}
	return d
}

func (l *liveAngel) retryLoop(ctx context.Context) {
	defer l.retryRunning.Done()
	for {
		l.mu.Lock()
		n, wait := l.retryFailures+1, l.retryWait
		l.mu.Unlock()
		if wait != nil {
			if !wait(ctx, retryBackoff(n)) {
				return
			}
		} else if !waitContext(ctx, retryBackoff(n)) {
			return
		}

		l.reloadMu.Lock()
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			l.reloadMu.Unlock()
			return
		}
		if l.inst != nil || l.retryTarget == nil {
			l.mu.Unlock()
			l.reloadMu.Unlock()
			continue
		}
		d := cloneDesired(l.retryTarget)
		o := l.startOpts
		if l.retryOpts != nil {
			o = *l.retryOpts
		}
		l.mu.Unlock()
		l.prepareHopStart(&o, true)
		inst, err := startCore(ctx, o)
		if err != nil {
			l.rejectHopHealth()
		}
		l.mu.Lock()
		success := err == nil && !l.closed && l.inst == nil && l.retryTarget != nil && fingerprint(*l.retryTarget) == fingerprint(*d)
		var failures int
		var nodesLog *nodeLog
		if success {
			l.inst = inst
			l.applied, l.startOpts = *d, o
			l.actualPort = portOf(inst.Addr())
			l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
			nodesLog = l.retryNodeLog
			l.retryTarget, l.retryOpts, l.retryNodeLog = nil, nil, nil
			l.retryDesired = ""
			l.retryFailures, l.reloadErr = 0, ""
			l.noteBindingsRestored(inst)
		} else if err != nil && !l.closed {
			l.retryFailures++
			failures = l.retryFailures
			l.reloadErr = errText(err)
		}
		closed := l.closed
		l.mu.Unlock()
		if success {
			l.attachHopHealth(inst)
			l.installNodeLogger(nodesLog)
			l.reloadMu.Unlock()
			l.logf("previous configuration restored")
			if l.retryApplied != nil {
				select {
				case l.retryApplied <- struct{}{}:
				default:
				}
			}
			continue
		}
		l.reloadMu.Unlock()
		if closed {
			if err == nil {
				l.rejectHopHealth()
				_ = inst.Close()
			}
			return
		}
		if err == nil {
			l.rejectHopHealth()
			_ = inst.Close()
			continue
		}
		if failures == 1 || failures%5 == 0 {
			l.warnf("previous configuration restart failed: %s", errText(err))
		}
	}
}

func (l *liveAngel) startRetry() {
	l.mu.Lock()
	if l.closed || l.retryCancel != nil || l.retryTarget == nil {
		l.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.retryCancel = cancel
	l.retryRunning.Add(1)
	go l.retryLoop(ctx)
	l.mu.Unlock()
}

func waitContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// watchSignature condenses the size and modification time of config.ktav,
// server secrets and every stored subscription.
func watchSignature(home string) string {
	var b strings.Builder
	add := func(path string) {
		fi, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s:-;", filepath.Base(path))
			return
		}
		fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(path), fi.Size(), fi.ModTime().UnixNano())
	}
	add(config.ConfigPath(home))
	add(servers.NewStore(home).Path())
	dir := filepath.Join(home, "subscriptions")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".ktav" || ext == ".json") {
			add(filepath.Join(dir, e.Name()))
		}
	}
	return b.String()
}

// watch calls reload when the files changed. A change must be seen on two
// polls in a row before it is applied, so an edit that is still being written
// is not read half done. A transient busy-port failure is retried only a
// bounded number of times for the same file signature.
func (l *liveAngel) watch(ctx context.Context, every time.Duration) {
	applied := watchSignature(l.opts.Home)
	// The angel loaded its files before this started: catch up once.
	initial := l.reload(ctx)
	pending := ""
	retries := 0
	if initial == reloadRetry {
		pending, applied = watchSignature(l.opts.Home), ""
		retries = 1 // the startup reload was the initial failed attempt
	}
	var ticker *time.Ticker
	ticks := l.watchTickC
	if ticks == nil {
		ticker = time.NewTicker(every)
		ticks = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		cur := watchSignature(l.opts.Home)
		ackPoll := func() {
			if l.watchPollDone != nil {
				select {
				case l.watchPollDone <- struct{}{}:
				case <-ctx.Done():
				}
			}
		}
		switch {
		case cur == applied:
			pending, retries = "", 0
		case cur != pending:
			pending, retries = cur, 0
		default:
			if l.reload(ctx) == reloadRetry {
				retries++
				if retries <= watchRetryLimit {
					ackPoll()
					continue
				}
			}
			applied, pending, retries = cur, "", 0
		}
		ackPoll()
	}
}

// connLog is the connection report, made on first use (a liveAngel built as a
// literal in a test has none); its writers are looked up at each line, since
// up.go sets warnf after the angel is made.
func (l *liveAngel) connLog() *connLog {
	l.connsOnce.Do(func() {
		l.conns = newConnLog(
			func(format string, args ...any) { l.logf(format, args...) },
			func(format string, args ...any) { l.warnf(format, args...) })
		l.conns.debug = func(format string, args ...any) { l.debugf(format, args...) }
		l.conns.quiet = l.sleep().quiet
	})
	return l.conns
}

// sleep is the watch for the computer's sleep, made on first use.
func (l *liveAngel) sleep() *sleepWatch {
	l.sleeperOnce.Do(func() { l.sleeper = newSleepWatch() })
	return l.sleeper
}

// woke is called when the computer woke up after sleeping for slept: the nodes
// are probed again at once and the failures of the next minutes are expected.
func (l *liveAngel) woke(slept time.Duration) {
	l.logf("the computer was asleep for %s (or its clock jumped forward); probing the nodes again at once, failures in the next %d minutes are expected",
		slept, int(l.sleep().grace/time.Minute))
	l.mu.Lock()
	inst := l.inst
	l.mu.Unlock()
	if inst != nil {
		inst.ProbeNow()
	}
}

// startLoops starts the file watcher and the subscription refresh.
func (l *liveAngel) startLoops() {
	ctx, cancel := context.WithCancel(context.Background())
	l.loopsCancel = cancel
	l.loopsRunning.Add(6)
	go func() {
		defer l.loopsRunning.Done()
		l.watch(ctx, watchEvery)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.refreshLoop(ctx)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.bindingWriterLoop(ctx)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.connLog().run(ctx, connLogEvery, connBeatEvery, l.flushNodeLogger)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.watchSleep(ctx)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.serverDiagnosticsLoop(ctx)
	}()
}

// watchSleep runs the sleep watch until ctx ends; a wake-up probes the nodes
// again (see woke).
func (l *liveAngel) watchSleep(ctx context.Context) {
	l.sleep().run(ctx, l.woke)
}

// close stops the loops, then the core and the statistics store.
func (l *liveAngel) close() {
	l.mu.Lock()
	l.closed = true
	if l.retryCancel != nil {
		l.retryCancel()
	}
	l.mu.Unlock()
	if l.loopsCancel != nil {
		l.loopsCancel()
	}
	l.loopsRunning.Wait()
	l.retryRunning.Wait()
	l.mu.Lock()
	inst, store := l.inst, l.store
	l.inst = nil
	oldServers := cloneServerSpecs(l.applied.servers)
	l.mu.Unlock()
	// The last snapshot is taken while the instance still runs and is on
	// disk before the store is closed (the shutdown order in up.go).
	l.writeBindingsFinal(inst, store)
	l.logServerDelta(oldServers, nil)
	l.closeHopHealth()
	if inst != nil {
		_ = inst.Close()
	}
	closeStats(store)
}

// currentStore is the statistics store, which may be opened late.
func (l *liveAngel) currentStore() *stats.Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.store
}

// status is the answer of GET /v1/status.
func (l *liveAngel) status(startedAt string) angel.Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := angel.Status{
		Angel:          "running",
		Foreground:     l.opts.Handshake == "",
		PID:            os.Getpid(),
		Protocols:      angelProtocols,
		Auth:           liveAuth(l.applied.cfg),
		StartedAt:      startedAt,
		Version:        version,
		Stats:          statsInfo(l.store, l.opts.Home),
		Reload:         &angel.ReloadInfo{Count: l.reloads, LastError: l.reloadErr},
		Refresh:        l.refreshInfoLocked(),
		AllowedClients: config.NormalizeAllowedClients(l.applied.cfg.Listen.AllowedClients),
	}
	if !l.lastReload.IsZero() {
		st.Reload.LastAt = l.lastReload.UTC().Format(time.RFC3339)
	}
	if l.inst == nil {
		st.Core = "down"
	}
	if l.inst == nil {
		st.Accounts = accountStatus(l.applied.cfg, nil)
		return st
	}
	st.Listen = l.inst.Addr()
	st.Route = l.inst.Route()
	st.Accounts = accountStatus(l.applied.cfg, l.inst.AccountRoutes())
	st.Activity = activityInfo(st.Accounts, l.inst.Activity(), l.applied.pool.infos)
	st.Warm = warmInfo(l.inst, l.applied.pool.infos)
	st.Affinity = affinityInfo(l.inst, len(l.applied.pool.accountRoutes) > 0)
	if source, ok := l.inst.(serverInfoSource); ok {
		infos := source.Servers()
		st.Servers = make([]angel.ServerInfo, 0, len(infos))
		for _, info := range infos {
			members := append([]string(nil), info.Members...)
			sort.Strings(members)
			st.Servers = append(st.Servers, angel.ServerInfo{
				Name: info.Name, Protocol: info.Protocol, Listen: info.Listen,
				Accounts: append([]string(nil), info.Accounts...), Connections: info.Connections,
				DroppedClients: info.DroppedClients, Members: members,
				Default: info.Default, Unclassified: info.Unclassified, SlowFirstByte: info.SlowFirstByte,
				Replayed: info.Replayed, Ambiguous: info.Ambiguous,
			})
		}
		sort.Slice(st.Servers, func(i, j int) bool { return st.Servers[i].Name < st.Servers[j].Name })
	}
	if st.Affinity != nil {
		st.Affinity.Restored = l.bindingsRestored
		if !l.bindingsSavedAt.IsZero() {
			st.Affinity.SavedAt = l.bindingsSavedAt.UTC().Format(time.RFC3339)
		}
	}
	return st
}
