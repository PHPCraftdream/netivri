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
	"time"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// watchEvery is how often the daemon looks at the configuration files.
const watchEvery = 2 * time.Second

// coreInstance is what the daemon needs from a running core; tests start
// fakes through coreStart.
type coreInstance interface {
	Addr() string
	Route() string
	AccountRoutes() map[string]string
	WarmStats() (core.WarmStats, bool)
	AffinityStats() core.AffinityStats
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
	cfg  *config.Config
	bind string
	port int
	pool routePool
}

// liveDaemon keeps the running core in step with the files in the state
// directory (docs/design/live-reload.md). The core takes its routes and its
// accounts at Start, so applying a change means closing the listener and
// starting a new one on the same port; that happens only when something the
// core is built from really changed.
type liveDaemon struct {
	opts upOptions
	logf func(format string, args ...any)
	stop func() // asks the daemon to shut down; idempotent

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

	reloads      uint64
	lastReload   time.Time
	reloadErr    string
	refreshOK    uint64
	refreshFail  uint64
	lastRefresh  time.Time
	refreshErr   string
	loopsCancel  context.CancelFunc
	loopsRunning sync.WaitGroup
}

func newLiveDaemon(opts upOptions, logf func(format string, args ...any), stop func()) *liveDaemon {
	return &liveDaemon{opts: opts, logf: logf, stop: stop}
}

// errText is the one-line form of an error kept in the status answer and the
// daemon log: the stable code and message, never a URL or a credential.
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

func (l *liveDaemon) coreOptions(d desiredState, a mihauth.Authenticator, store *stats.Store) core.Options {
	var observer core.Observer
	if store != nil {
		observer = statsObserver{store: store}
	}
	return core.Options{
		HomeDir:       l.opts.Home,
		Bind:          d.bind,
		Port:          d.port,
		Authenticator: a,
		RouteNodes:    d.pool.nodes,
		AccountRoute:  d.pool.accountRoutes,
		NodeLabels:    d.pool.labels,
		Observer:      observer,
		Warm:          warmOptions(d.pool.nodes),
	}
}

// start opens the first core instance.
func (l *liveDaemon) start(ctx context.Context, d desiredState, a mihauth.Authenticator, store *stats.Store) error {
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
	l.mu.Unlock()
	return nil
}

// load reads the configuration and the stored snapshots the way `up` does and
// validates the result. Nothing is applied.
func (l *liveDaemon) load() (desiredState, error) {
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
	return desiredState{cfg: cfg, bind: bind, port: port, pool: pool}, nil
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

// fingerprint identifies what a core is built from: the bind address, the
// port, the proxy credentials, and the nodes and routes of the accounts.
// Settings the core does not read (subscription URLs, the refresh interval)
// are left out, so changing them never restarts the listener.
func fingerprint(d desiredState) string {
	type acct struct{ Login, Password string }
	v := struct {
		Bind, AuthLogin, AuthPassword string
		Port                          int
		Accounts                      []acct
		Nodes                         map[string][]map[string]any
		Labels                        map[string][]string
		Routes                        map[string]string
	}{Bind: d.bind, Port: d.port, Nodes: nodesWithoutNames(d.pool.nodes), Labels: d.pool.labels, Routes: d.pool.accountRoutes}
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

// reload applies the files on disk when they differ from what runs. A file
// that does not load, or routes that cannot be built, leave the running core
// untouched and show up as reload.last_error in the status.
func (l *liveDaemon) reload(ctx context.Context) {
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
	if same {
		l.reloadErr = ""
	}
	l.mu.Unlock()
	if !same {
		l.restart(ctx, d)
	}
}

func (l *liveDaemon) noteReloadError(what string, err error) {
	text := errText(err)
	l.mu.Lock()
	l.reloadErr = text
	l.mu.Unlock()
	l.logf("%s: %s", what, text)
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
func (l *liveDaemon) restart(ctx context.Context, d desiredState) {
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
			store = openStats(l.opts.Home, d.pool.infos, l.logf)
		} else if err := store.SetNodes(d.pool.infos); err != nil {
			l.logf("statistics: node details not stored: %v", err)
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
	if l.closed {
		return
	}
	prev := l.startOpts
	if l.inst != nil {
		_ = l.inst.Close()
		l.inst = nil
	}
	o := l.coreOptions(d, a, store)
	inst, err := startCore(ctx, o)
	if err != nil {
		l.reloadErr = errText(err)
		l.logf("new configuration not applied, restoring the previous one: %s", l.reloadErr)
		back, err2 := startCore(ctx, prev)
		if err2 != nil {
			l.logf("the previous configuration did not start either, stopping: %s", errText(err2))
			l.stop()
			return
		}
		l.inst = back
		return
	}
	l.inst, l.applied, l.latest, l.startOpts = inst, d, d.cfg, o
	l.actualPort = portOf(inst.Addr())
	l.startOpts.Port, l.applied.port = l.actualPort, l.actualPort
	l.reloads++
	l.lastReload = time.Now()
	l.reloadErr = ""
	l.logf("configuration applied: routes=%d accounts=%d", len(d.pool.accountRoutes), len(d.cfg.Accounts))
}

// watchSignature condenses the size and modification time of config.ktav and
// of every stored subscription.
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
func (l *liveDaemon) watch(ctx context.Context, every time.Duration) {
	applied := watchSignature(l.opts.Home)
	// The daemon loaded its files before this started: catch up once.
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

// startLoops starts the file watcher and the subscription refresh.
func (l *liveDaemon) startLoops() {
	ctx, cancel := context.WithCancel(context.Background())
	l.loopsCancel = cancel
	l.loopsRunning.Add(2)
	go func() {
		defer l.loopsRunning.Done()
		l.watch(ctx, watchEvery)
	}()
	go func() {
		defer l.loopsRunning.Done()
		l.refreshLoop(ctx)
	}()
}

// close stops the loops, then the core and the statistics store.
func (l *liveDaemon) close() {
	if l.loopsCancel != nil {
		l.loopsCancel()
	}
	l.loopsRunning.Wait()
	l.mu.Lock()
	l.closed = true
	inst, store := l.inst, l.store
	l.inst = nil
	l.mu.Unlock()
	if inst != nil {
		_ = inst.Close()
	}
	closeStats(store)
}

// currentStore is the statistics store, which may be opened late.
func (l *liveDaemon) currentStore() *stats.Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.store
}

// status is the answer of GET /v1/status.
func (l *liveDaemon) status(startedAt string) daemon.Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := daemon.Status{
		Daemon:     "running",
		Foreground: l.opts.Handshake == "",
		PID:        os.Getpid(),
		Protocols:  daemonProtocols,
		Auth:       liveAuth(l.applied.cfg),
		StartedAt:  startedAt,
		Version:    version,
		Stats:      statsInfo(l.store, l.opts.Home),
		Reload:     &daemon.ReloadInfo{Count: l.reloads, LastError: l.reloadErr},
		Refresh:    l.refreshInfoLocked(),
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
	st.Warm = warmInfo(l.inst)
	st.Affinity = affinityInfo(l.inst, len(l.applied.pool.accountRoutes) > 0)
	return st
}
