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
	// refreshStreaks counts the failed refreshes in a row by subscription.
	refreshStreaks map[string]int
	loopsCancel    context.CancelFunc
	loopsRunning   sync.WaitGroup
	retryCancel    context.CancelFunc
	retryRunning   sync.WaitGroup
	retryWait      func(context.Context, time.Duration) bool
	retryApplied   chan struct{}
	retryTarget    *desiredState
	retryDesired   string
	retryOpts      *core.Options
	retryFailures  int

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

func (l *liveAngel) coreOptions(d desiredState, a mihauth.Authenticator, store *stats.Store) core.Options {
	o := l.routeOptions(d, a, store)
	o.InitialBindings = initialBindings(store, l.warnf)
	return o
}

// routeOptions is coreOptions without the stored bindings, which only a
// starting core reads.
func (l *liveAngel) routeOptions(d desiredState, a mihauth.Authenticator, store *stats.Store) core.Options {
	var observer core.Observer
	if store != nil {
		observer = statsObserver{store: store}
	}
	l.connLog().setNodes(d.pool.infos)
	return core.Options{
		HomeDir:               l.opts.Home,
		Bind:                  d.bind,
		Port:                  d.port,
		Authenticator:         l.connLog().wrapAuth(a),
		AllowedClients:        config.AllowedClientPrefixes(d.cfg.Listen.AllowedClients),
		RouteNodes:            d.pool.nodes,
		AccountRoute:          d.pool.accountRoutes,
		RoutePre:              d.pool.routePre,
		RoutePost:             d.pool.routePost,
		GlobalPost:            d.cfg.Chain.PostSOCKS5,
		Servers:               cloneServerSpecs(d.servers),
		AccountAllowedClients: accountAllowedClients(d.cfg),
		NodeLabels:            d.pool.labels,
		NodeTiers:             d.pool.tiers,
		OnConnEvent:           l.connLog().event,
		OnNodeHealth:          nodeHealthLogger(d.pool.infos, d.pool.firstTierLabels(), l.sleep().quiet, l.debugf, l.logf, l.warnf),
		Observer:              observer,
		Warm:                  warmOptions(d.pool.nodes),
		AffinityTTL:           stats.BindingTTL,
	}
}

// start opens the first core instance.
func (l *liveAngel) start(ctx context.Context, d desiredState, a mihauth.Authenticator, store *stats.Store) error {
	o := l.coreOptions(d, a, store)
	inst, err := coreStart(ctx, o)
	if err != nil {
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
		Pre        map[string]string
		Post       map[string]string
		GlobalPost string
	}{
		Nodes: nodesWithoutNames(d.pool.nodes), Labels: d.pool.labels, Tiers: d.pool.tiers,
		Routes: d.pool.accountRoutes, Pre: d.pool.routePre, Post: d.pool.routePost,
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

// reload applies the files on disk when they differ from what runs. A file
// that does not load, or routes that cannot be built, leave the running core
// untouched and show up as reload.last_error in the status.
func (l *liveAngel) reload(ctx context.Context) {
	l.reloadMu.Lock()
	defer l.reloadMu.Unlock()
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return
	}
	d, err := l.load()
	if err != nil {
		l.noteReloadError("configuration not applied", err)
		return
	}
	l.mu.Lock()
	l.latest = d.cfg
	if d.port == 0 {
		d.port = l.actualPort
	}
	same := fingerprint(l.applied) == fingerprint(d)
	instDown := l.inst == nil
	if same && !instDown {
		l.reloadErr = ""
	}
	globalPostChanged := l.applied.cfg.Chain.PostSOCKS5 != d.cfg.Chain.PostSOCKS5
	inPlace := !same && !globalPostChanged && listenerFingerprint(l.applied) == listenerFingerprint(d) &&
		(((len(l.applied.pool.accountRoutes) > 0) == (len(d.pool.accountRoutes) > 0)) || routesFingerprint(l.applied) == routesFingerprint(d) || (accountClientsFingerprint(l.applied.cfg) != accountClientsFingerprint(d.cfg)))
	l.mu.Unlock()
	if instDown {
		l.mu.Lock()
		unchangedFailed := l.retryDesired == fingerprint(d)
		l.mu.Unlock()
		if unchangedFailed {
			return
		}
		l.restart(ctx, d)
		return
	}
	switch {
	case same:
	case inPlace && l.reconfigure(d):
	default:
		l.restart(ctx, d)
	}
}

// reconfigure gives the running core new routes without closing its
// listener, so open connections go on and the nodes that stay keep their
// health and bindings. False means the core could not take them in place and
// must be restarted instead.
func (l *liveAngel) reconfigure(d desiredState) bool {
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
	o := l.routeOptions(d, auth, store)
	if err := inst.Reconfigure(o); err != nil {
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
		return true
	}
	o.Port, d.port = l.actualPort, l.actualPort
	updateAccountPolicy(o.Authenticator, d.cfg)
	l.applied, l.latest, l.startOpts = d, d.cfg, o
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.mu.Unlock()
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
func (l *liveAngel) restart(ctx context.Context, d desiredState) {
	a, err := newAuthenticator(d.cfg, l.opts.Home)
	if err != nil {
		l.noteReloadError("configuration not applied", err)
		return
	}
	l.mu.Lock()
	store, prev, old := l.store, l.startOpts, l.inst
	if l.closed {
		l.mu.Unlock()
		return
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
		_ = old.Close()
	}
	if len(d.pool.infos) > 0 {
		if store == nil {
			store = openStats(l.opts.Home, d.pool.infos, l.warnf)
		} else if err := store.SetNodes(d.pool.infos); err != nil {
			l.warnf("statistics: node details not stored: %v", err)
		}
	}
	o := l.coreOptions(d, a, store)
	l.mu.Lock()
	l.store = store
	l.mu.Unlock()
	inst, err := startCore(ctx, o)
	if err != nil {
		text := errText(err)
		if wasDown {
			l.mu.Lock()
			l.retryTarget, l.retryOpts = cloneDesired(&d), &o
			l.retryFailures = 0
			l.reloadErr = text
			l.mu.Unlock()
			l.warnf("configuration restart failed; retrying: %s", text)
			l.startRetry()
			return
		}
		l.mu.Lock()
		l.reloadErr = text
		l.mu.Unlock()
		l.warnf("new configuration not applied, restoring the previous one: %s", text)
		back := prev
		back.InitialBindings = initialBindings(store, l.warnf)
		backInst, err2 := startCore(ctx, back)
		if err2 != nil {
			l.mu.Lock()
			l.retryTarget, l.retryOpts = cloneDesired(&l.applied), &back
			l.retryFailures = 0
			l.reloadErr = text + "; previous configuration could not be restored: " + errText(err2)
			l.mu.Unlock()
			l.warnf("previous configuration could not be restarted; retrying: %s", errText(err2))
			l.startRetry()
			return
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = backInst.Close()
			return
		}
		l.inst = backInst
		l.noteBindingsRestored(backInst)
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = inst.Close()
		return
	}
	l.inst, l.applied, l.latest, l.startOpts = inst, d, d.cfg, o
	l.retryTarget, l.retryOpts, l.retryDesired = nil, nil, ""
	l.noteBindingsRestored(inst)
	l.actualPort = portOf(inst.Addr())
	l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.mu.Unlock()
	l.logf("configuration applied: routes=%d accounts=%d", len(d.pool.accountRoutes), len(d.cfg.Accounts))
	l.logServerDelta(oldServers, d.servers)
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
		inst, err := startCore(ctx, o)
		l.mu.Lock()
		success := err == nil && !l.closed && l.inst == nil && l.retryTarget != nil && fingerprint(*l.retryTarget) == fingerprint(*d)
		var failures int
		if success {
			l.inst = inst
			l.applied, l.startOpts = *d, o
			l.actualPort = portOf(inst.Addr())
			l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
			l.retryTarget, l.retryOpts = nil, nil
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
				_ = inst.Close()
			}
			return
		}
		if err == nil {
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
// is not read half done.
func (l *liveAngel) watch(ctx context.Context, every time.Duration) {
	applied := watchSignature(l.opts.Home)
	// The angel loaded its files before this started: catch up once.
	l.reload(ctx)
	pending := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur := watchSignature(l.opts.Home)
		switch {
		case cur == applied:
			pending = ""
		case cur != pending:
			pending = cur
		default:
			l.reload(ctx)
			applied, pending = cur, ""
		}
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
		l.connLog().run(ctx, connLogEvery, connBeatEvery)
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
