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
	return listenerFingerprint(d) + routesFingerprint(d) + serverFingerprint(d.servers)
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
		Pre        map[string]string
		Post       map[string]string
		GlobalPost string
	}{
		Nodes: nodesWithoutNames(d.pool.nodes), Labels: d.pool.labels, Tiers: d.pool.tiers,
		Routes: d.pool.accountRoutes, Pre: d.pool.routePre, Post: d.pool.routePost,
		GlobalPost: d.cfg.Chain.PostSOCKS5,
	}
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// listenerFingerprint covers the listener: address, allowed clients and the
// credentials. A change here needs a new core.
func listenerFingerprint(d desiredState) string {
	type acct struct{ Login, Password, Clients string }
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
		v.Accounts = append(v.Accounts, acct{a.Login, a.Password, config.NormalizeAllowedClients(a.AllowedClients)})
	}
	persistMu.Unlock()
	sort.Slice(v.Accounts, func(i, j int) bool { return v.Accounts[i].Login < v.Accounts[j].Login })
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
		d.port = l.actualPort // a random port stays the one clients know
	}
	same := fingerprint(l.applied) == fingerprint(d)
	globalPostChanged := l.applied.cfg.Chain.PostSOCKS5 != d.cfg.Chain.PostSOCKS5
	inPlace := !same && !globalPostChanged && listenerFingerprint(l.applied) == listenerFingerprint(d) &&
		(((len(l.applied.pool.accountRoutes) > 0) == (len(d.pool.accountRoutes) > 0)) || (serverFingerprint(l.applied.servers) != serverFingerprint(d.servers) && routesFingerprint(l.applied) == routesFingerprint(d)))
	if same {
		l.reloadErr = ""
	}
	l.mu.Unlock()
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
	l.mu.Unlock()
	if len(d.pool.accountRoutes) == 0 && !serverOnly {
		return false
	}
	if store != nil && len(d.pool.infos) > 0 {
		if err := store.SetNodes(d.pool.infos); err != nil {
			l.warnf("statistics: node details not stored: %v", err)
		}
	}
	o := l.routeOptions(d, auth, store)
	if err := inst.Reconfigure(o); err != nil {
		if serverOnly {
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
		inst, err := coreStart(ctx, o)
		var ce *cli.Error
		if err == nil || !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" || attempt == portRetries {
			return inst, err
		}
		time.Sleep(portRetryWait)
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
	store := l.store
	l.mu.Unlock()
	if len(d.pool.infos) > 0 {
		if store == nil {
			store = openStats(l.opts.Home, d.pool.infos, l.warnf)
		} else if err := store.SetNodes(d.pool.infos); err != nil {
			l.warnf("statistics: node details not stored: %v", err)
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
	if l.closed {
		return
	}
	prev := l.startOpts
	oldServers := cloneServerSpecs(l.applied.servers)
	if l.inst != nil {
		// The last snapshot is taken while the instance still runs and is
		// on disk before it closes; the new core loads it as InitialBindings.
		l.writeBindingsFinalLocked(l.inst, store)
		_ = l.inst.Close()
		l.inst = nil
	}
	o := l.coreOptions(d, a, store)
	inst, err := startCore(ctx, o)
	if err != nil {
		l.reloadErr = errText(err)
		l.warnf("new configuration not applied, restoring the previous one: %s", l.reloadErr)
		// The rollback restarts the previous configuration with the bindings
		// that were just saved from the old instance, not with the ones it
		// was originally started with.
		back := prev
		back.InitialBindings = initialBindings(store, l.warnf)
		backInst, err2 := startCore(ctx, back)
		if err2 != nil {
			l.errorf("the previous configuration did not start either, stopping: %s", errText(err2))
			l.stop()
			return
		}
		l.inst = backInst
		l.noteBindingsRestored(backInst)
		return
	}
	l.inst, l.applied, l.latest, l.startOpts = inst, d, d.cfg, o
	l.noteBindingsRestored(inst)
	l.actualPort = portOf(inst.Addr())
	l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.logf("configuration applied: routes=%d accounts=%d", len(d.pool.accountRoutes), len(d.cfg.Accounts))
	l.logServerDelta(oldServers, d.servers)
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
	if l.loopsCancel != nil {
		l.loopsCancel()
	}
	l.loopsRunning.Wait()
	l.mu.Lock()
	l.closed = true
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
			st.Servers = append(st.Servers, angel.ServerInfo{
				Name: info.Name, Protocol: info.Protocol, Listen: info.Listen,
				Accounts: append([]string(nil), info.Accounts...), Connections: info.Connections,
				DroppedClients: info.DroppedClients,
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
