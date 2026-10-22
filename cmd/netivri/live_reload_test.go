package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// fakeCore stands in for a running core.
type fakeCore struct {
	addr   string
	mu     sync.Mutex
	closed bool
}

func (f *fakeCore) Addr() string                      { return f.addr }
func (f *fakeCore) Route() string                     { return "direct" }
func (f *fakeCore) AccountRoutes() map[string]string  { return map[string]string{"alice": "p~DE"} }
func (f *fakeCore) WarmStats() (core.WarmStats, bool) { return core.WarmStats{}, false }
func (f *fakeCore) AffinityStats() core.AffinityStats { return core.AffinityStats{} }
func (f *fakeCore) Close() error                      { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
func (f *fakeCore) isClosed() bool                    { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

// coreScript replaces coreStart: it records every call and fails the ones
// listed in fail (1-based, counting failed calls too).
type coreScript struct {
	mu      sync.Mutex
	opts    []core.Options
	insts   []*fakeCore
	fail    map[int]error
	port    int           // what a start with port 0 gets
	started chan struct{} // one signal per successful start, when set
}

func (s *coreScript) start(_ context.Context, o core.Options) (coreInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts = append(s.opts, o)
	if err, bad := s.fail[len(s.opts)]; bad {
		return nil, err
	}
	port := o.Port
	if port == 0 {
		port = s.port
	}
	f := &fakeCore{addr: fmt.Sprintf("127.0.0.1:%d", port)}
	s.insts = append(s.insts, f)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	return f, nil
}

func (s *coreScript) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.opts)
}

func (s *coreScript) call(i int) core.Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts[i]
}

func (s *coreScript) inst(i int) *fakeCore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insts[i]
}

type reloadFixture struct {
	home    string
	hash    string
	l       *liveDaemon
	script  *coreScript
	stopped atomic.Bool
	mu      sync.Mutex
	logs    []string
}

func (f *reloadFixture) logText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.logs, "\n")
}

func (f *reloadFixture) routed() config.Account {
	return config.Account{Login: "alice", Password: f.hash, Provider: "p", ExitCountry: "DE"}
}

func (f *reloadFixture) writeConfig(t *testing.T, accounts ...config.Account) {
	t.Helper()
	cfg := config.Default()
	cfg.Accounts = accounts
	cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.invalid/sub"}}
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
}

func (f *reloadFixture) saveNodes(t *testing.T, server, name string) {
	t.Helper()
	n := subscription.Node{ID: "n1", Name: name, Type: "socks5", Server: server, Port: 1080, Country: "DE",
		Raw: map[string]any{"name": name, "type": "socks5", "server": server, "port": 1080}}
	store := subscription.NewStore(filepath.Join(f.home, "subscriptions"))
	if err := store.Save("p", subscription.Snapshot{FetchedAt: time.Now().UTC(), Nodes: []subscription.Node{n}}); err != nil {
		t.Fatal(err)
	}
}

// newReloadFixture starts a liveDaemon on a fake core. Without accounts the
// daemon starts with no routes.
func newReloadFixture(t *testing.T, opts upOptions, accounts ...config.Account) *reloadFixture {
	t.Helper()
	f := &reloadFixture{home: t.TempDir(), script: &coreScript{port: 43210}}
	var err error
	if f.hash, err = auth.Hash("fixture-password"); err != nil {
		t.Fatal(err)
	}
	opts.Home = f.home
	f.saveNodes(t, "192.0.2.1", "n1")
	if len(accounts) == 0 {
		f.writeConfig(t)
	} else {
		for i := range accounts {
			if accounts[i].Password == "" {
				accounts[i].Password = f.hash
			}
		}
		f.writeConfig(t, accounts...)
	}
	old := coreStart
	coreStart = f.script.start
	t.Cleanup(func() { coreStart = old })

	f.l = newLiveDaemon(opts,
		func(format string, args ...any) {
			f.mu.Lock()
			f.logs = append(f.logs, fmt.Sprintf(format, args...))
			f.mu.Unlock()
		},
		func() { f.stopped.Store(true) })
	d, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.l.start(t.Context(), d, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.l.close)
	return f
}

func (f *reloadFixture) reloads() uint64 {
	f.l.mu.Lock()
	defer f.l.mu.Unlock()
	return f.l.reloads
}

func (f *reloadFixture) reloadErr() string {
	f.l.mu.Lock()
	defer f.l.mu.Unlock()
	return f.l.reloadErr
}

func serverOf(o core.Options) any { return o.RouteNodes["p~DE"][0]["server"] }

func TestReloadRestartsOnlyOnChange(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	ctx := t.Context()
	if f.script.calls() != 1 {
		t.Fatalf("starts = %d, want 1", f.script.calls())
	}

	f.l.reload(ctx)
	if f.script.calls() != 1 || f.reloads() != 0 {
		t.Fatalf("an unchanged state restarted the core: starts=%d reloads=%d", f.script.calls(), f.reloads())
	}

	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(ctx)
	if f.script.calls() != 2 || f.reloads() != 1 {
		t.Fatalf("a changed node did not restart the core: starts=%d reloads=%d", f.script.calls(), f.reloads())
	}
	if !f.script.inst(0).isClosed() || f.script.inst(1).isClosed() {
		t.Error("the old core must be closed and the new one running")
	}
	if serverOf(f.script.call(1)) != "192.0.2.2" {
		t.Errorf("the new core got node %v, want 192.0.2.2", serverOf(f.script.call(1)))
	}

	f.saveNodes(t, "192.0.2.2", "n1 (5 GB left)")
	f.l.reload(ctx)
	if f.script.calls() != 2 {
		t.Fatal("a renamed node restarted the core")
	}

	f.writeConfig(t, f.routed(), config.Account{Login: "bob", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"})
	f.l.reload(ctx)
	if f.script.calls() != 3 || f.reloads() != 2 {
		t.Fatalf("a new account did not restart the core: starts=%d reloads=%d", f.script.calls(), f.reloads())
	}
	if got := f.script.call(2).AccountRoute; got["bob"] != "p~DE" || got["alice"] != "p~DE" {
		t.Errorf("account routes of the new core = %v", got)
	}
	f.l.mu.Lock()
	subs := len(f.l.latest.Subscriptions)
	f.l.mu.Unlock()
	if subs != 1 {
		t.Errorf("latest configuration has %d subscriptions", subs)
	}
}

func TestReloadKeepsTheRandomPort(t *testing.T) {
	f := newReloadFixture(t, upOptions{Port: 0, PortSet: true}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	if got := f.script.call(0).Port; got != 0 {
		t.Fatalf("first start asked for port %d, want 0 (random)", got)
	}
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if f.script.calls() != 2 {
		t.Fatalf("starts = %d, want 2", f.script.calls())
	}
	if got := f.script.call(1).Port; got != 43210 {
		t.Errorf("the restart asked for port %d, want the port clients know (43210)", got)
	}
	// And the next reload of the same files finds nothing to do.
	f.l.reload(t.Context())
	if f.script.calls() != 2 {
		t.Error("a random port made every reload look like a change")
	}
}

func TestReloadRollsBackWhenTheNewCoreDoesNotStart(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{2: errors.New("listen failed")}
	f.saveNodes(t, "192.0.2.2", "n1")

	f.l.reload(t.Context())
	if f.script.calls() != 3 {
		t.Fatalf("starts = %d, want the failed one and the restore", f.script.calls())
	}
	if serverOf(f.script.call(2)) != "192.0.2.1" {
		t.Errorf("the restore used node %v, want the previous 192.0.2.1", serverOf(f.script.call(2)))
	}
	if f.script.inst(1).isClosed() {
		t.Error("the restored core is closed")
	}
	if f.reloads() != 0 || !strings.Contains(f.reloadErr(), "listen failed") {
		t.Errorf("reloads=%d error=%q", f.reloads(), f.reloadErr())
	}
	f.l.mu.Lock()
	applied := f.l.applied.pool.nodes["p~DE"][0]["server"]
	f.l.mu.Unlock()
	if applied != "192.0.2.1" {
		t.Errorf("the applied state moved to %v although it did not start", applied)
	}

	// The files still differ from what runs, so the next reload tries again.
	f.l.reload(t.Context())
	if f.script.calls() != 4 || f.reloads() != 1 || f.reloadErr() != "" {
		t.Fatalf("retry: starts=%d reloads=%d error=%q", f.script.calls(), f.reloads(), f.reloadErr())
	}
	if serverOf(f.script.call(3)) != "192.0.2.2" {
		t.Errorf("the retry used node %v", serverOf(f.script.call(3)))
	}
}

func TestReloadStopsTheDaemonWhenNoCoreStarts(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{2: errors.New("first failure"), 3: errors.New("second failure")}
	f.saveNodes(t, "192.0.2.2", "n1")

	f.l.reload(t.Context())
	if !f.stopped.Load() {
		t.Fatal("the daemon kept running without a listener")
	}
	if !strings.Contains(f.logText(), "stopping") {
		t.Errorf("log does not say why: %q", f.logText())
	}
	st := f.l.status("2026-10-22T09:00:00Z")
	if st.Listen != "" || st.Warm != nil || st.Affinity != nil {
		t.Errorf("status without a core reports a listener: %+v", st)
	}
	if st.Reload == nil || st.Reload.LastError == "" || len(st.Accounts) != 1 {
		t.Errorf("status without a core: reload=%+v accounts=%+v", st.Reload, st.Accounts)
	}
}

func TestReloadKeepsTheCoreOnBadFiles(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	cfgPath := config.ConfigPath(f.home)
	if err := os.WriteFile(cfgPath, []byte("selection.mode: turbo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 1 || !strings.Contains(f.reloadErr(), "CONFIG_INVALID") {
		t.Fatalf("broken file: starts=%d error=%q", f.script.calls(), f.reloadErr())
	}

	// A route without nodes is refused as well.
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Provider: "p", ExitCountry: "JP"})
	f.l.reload(t.Context())
	if f.script.calls() != 1 || !strings.Contains(f.reloadErr(), "NO_NODES_FOR_COUNTRY") {
		t.Fatalf("empty route: starts=%d error=%q", f.script.calls(), f.reloadErr())
	}
	if f.script.inst(0).isClosed() {
		t.Fatal("a refused change closed the running core")
	}

	f.writeConfig(t, f.routed())
	f.l.reload(t.Context())
	if f.script.calls() != 1 || f.reloadErr() != "" {
		t.Fatalf("after the fix: starts=%d error=%q", f.script.calls(), f.reloadErr())
	}
}

func TestReloadOpensTheStatsStoreWhenRoutesAppear(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Password: config.InitPlaceholder})
	if f.l.currentStore() != nil {
		t.Fatal("a daemon without routes opened the statistics store")
	}
	f.writeConfig(t, f.routed())
	f.l.reload(t.Context())
	if f.reloads() != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads())
	}
	if f.l.currentStore() == nil {
		t.Fatal("the statistics store was not opened for the new routes")
	}
	if _, err := os.Stat(statsPath(f.home)); err != nil {
		t.Errorf("statistics database: %v", err)
	}
	if f.script.call(1).Observer == nil {
		t.Error("the new core has no observer for the statistics")
	}
}

func TestStartCoreRetriesOnlyWhileThePortIsBusy(t *testing.T) {
	old, oldWait := coreStart, portRetryWait
	portRetryWait = time.Millisecond
	t.Cleanup(func() { coreStart, portRetryWait = old, oldWait })
	busy := &cli.Error{Code: "PORT_IN_USE", Message: "busy", Exit: cli.ExitConflict}

	calls := 0
	coreStart = func(context.Context, core.Options) (coreInstance, error) {
		calls++
		if calls <= 3 {
			return nil, busy
		}
		return &fakeCore{addr: "127.0.0.1:1"}, nil
	}
	if inst, err := startCore(t.Context(), core.Options{}); err != nil || inst == nil || calls != 4 {
		t.Fatalf("busy then free: inst=%v err=%v calls=%d, want a started core after 4 calls", inst, err, calls)
	}

	calls = 0
	coreStart = func(context.Context, core.Options) (coreInstance, error) {
		calls++
		return nil, errors.New("config rejected")
	}
	if _, err := startCore(t.Context(), core.Options{}); err == nil || calls != 1 {
		t.Fatalf("another error: err=%v calls=%d, want one call and the error", err, calls)
	}

	calls = 0
	coreStart = func(context.Context, core.Options) (coreInstance, error) {
		calls++
		return nil, busy
	}
	if _, err := startCore(t.Context(), core.Options{}); err == nil || calls != portRetries {
		t.Fatalf("always busy: err=%v calls=%d, want %d calls and the error", err, calls, portRetries)
	}
}

func TestWatchAppliesChangesAndStopsWithItsContext(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.started = make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { f.l.watch(ctx, 10*time.Millisecond); close(done) }()

	time.Sleep(150 * time.Millisecond)
	if f.script.calls() != 1 {
		t.Fatalf("untouched files restarted the core: starts = %d", f.script.calls())
	}
	f.saveNodes(t, "192.0.2.9", "n1")
	select {
	case <-f.script.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not apply the changed node list")
	}
	if serverOf(f.script.call(1)) != "192.0.2.9" || !f.script.inst(0).isClosed() {
		t.Error("the watcher started the wrong core or left the old one open")
	}
	time.Sleep(150 * time.Millisecond)
	if f.script.calls() != 2 {
		t.Errorf("the same change was applied again: starts = %d", f.script.calls())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop with its context")
	}
}

// fetchCounter serves the Finnish test list and counts the calls.
type fetchCounter struct {
	calls atomic.Int64
	err   error
}

func (c *fetchCounter) get(context.Context, string, fetch.Options) (*fetch.Result, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &fetch.Result{Body: []byte(testBodyFI), Status: 200}, nil
}

func runRefreshLoop(t *testing.T, interval string, fc *fetchCounter) (*liveDaemon, *subscription.Store, func()) {
	t.Helper()
	oldWake, oldDelay, oldFetch := refreshWake, refreshStartDelay, subFetch
	refreshWake, refreshStartDelay, subFetch = 5*time.Millisecond, 0, fc.get
	t.Cleanup(func() { refreshWake, refreshStartDelay, subFetch = oldWake, oldDelay, oldFetch })

	home := t.TempDir()
	l := newLiveDaemon(upOptions{Home: home}, func(string, ...any) {}, func() {})
	cfg := config.Default()
	cfg.Fetch.UpdateInterval = interval
	cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://main.invalid/sub"}}
	l.latest = cfg
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { l.refreshLoop(ctx); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the refresh loop did not stop with its context")
		}
	}
	t.Cleanup(cancel)
	return l, subscription.NewStore(filepath.Join(home, "subscriptions")), stop
}

func TestRefreshLoopFetchesOnceAndWaitsForTheInterval(t *testing.T) {
	fc := &fetchCounter{}
	l, store, stop := runRefreshLoop(t, "", fc)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if snap, err := store.Load("main"); err == nil && len(snap.Nodes) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the subscription was not refreshed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := fc.calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1: the next one is an hour away", got)
	}
	l.mu.Lock()
	info := l.refreshInfoLocked()
	l.mu.Unlock()
	if info.OK != 1 || info.Failed != 0 || info.IntervalSeconds != 3600 || info.LastAt == "" {
		t.Errorf("refresh info = %+v", info)
	}
	stop()
}

func TestRefreshLoopDoesNothingWhenOff(t *testing.T) {
	fc := &fetchCounter{}
	_, store, stop := runRefreshLoop(t, config.UpdateOff, fc)
	time.Sleep(150 * time.Millisecond)
	stop()
	if got := fc.calls.Load(); got != 0 {
		t.Errorf("fetches = %d, want none with update_interval off", got)
	}
	if _, err := store.Load("main"); err == nil {
		t.Error("a snapshot was stored with update_interval off")
	}
}

func TestRefreshLoopRetriesAFailureLater(t *testing.T) {
	fc := &fetchCounter{err: &cli.Error{Code: "FETCH_HTTP_STATUS", Message: "unexpected HTTP status 403", Exit: cli.ExitNetwork}}
	l, store, stop := runRefreshLoop(t, "", fc)
	deadline := time.Now().Add(10 * time.Second)
	for fc.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no fetch attempt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	stop()
	if got := fc.calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1: a failed one is retried after minutes, not milliseconds", got)
	}
	if _, err := store.Load("main"); err == nil {
		t.Error("a failed fetch stored a snapshot")
	}
	l.mu.Lock()
	info := l.refreshInfoLocked()
	l.mu.Unlock()
	if info.Failed != 1 || !strings.Contains(info.LastError, "FETCH_HTTP_STATUS") || info.OK != 0 {
		t.Errorf("refresh info = %+v", info)
	}
}

func TestLiveStatusWithACore(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.l.noteRefresh(true, nil)
	f.l.noteRefresh(false, errors.New("boom"))
	st := f.l.status("2026-10-22T09:00:00Z")
	if st.Listen != "127.0.0.1:7890" || st.Route != "direct" || st.Daemon != "running" || st.StartedAt != "2026-10-22T09:00:00Z" {
		t.Errorf("status = %+v", st)
	}
	if len(st.Accounts) != 1 || st.Accounts[0].Login != "alice" || st.Accounts[0].ExitCountry != "DE" {
		t.Errorf("accounts = %+v", st.Accounts)
	}
	if st.Refresh == nil || st.Refresh.OK != 1 || st.Refresh.Failed != 1 || st.Refresh.LastError != "boom" {
		t.Errorf("refresh = %+v", st.Refresh)
	}
	if st.Reload == nil || st.Reload.Count != 0 || st.Reload.LastError != "" || st.Reload.LastAt != "" {
		t.Errorf("reload = %+v", st.Reload)
	}
	if st.Affinity == nil {
		t.Error("a daemon with routed accounts reports no affinity")
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"refresh":{`, `"reload":{`, `"interval_seconds":3600`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("status JSON lacks %s: %s", key, data)
		}
	}
}

func TestRunStatusPassesRefreshAndReload(t *testing.T) {
	old := daemonProbeLive
	t.Cleanup(func() { daemonProbeLive = old })
	run := func(st daemon.Status) map[string]any {
		daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) {
			return daemon.State{}, st, nil
		}
		var out bytes.Buffer
		if err := runStatus([]string{"--home", t.TempDir()}, &out); err != nil {
			t.Fatal(err)
		}
		return decodeData(t, &out)
	}

	data := run(daemon.Status{
		Refresh: &daemon.RefreshInfo{IntervalSeconds: 3600, OK: 2, Failed: 1, LastAt: "2026-10-22T09:00:00Z", LastError: "FETCH_HTTP_STATUS: 403"},
		Reload:  &daemon.ReloadInfo{Count: 3, LastAt: "2026-10-22T09:01:00Z"},
	})
	refresh, _ := data["refresh"].(map[string]any)
	reload, _ := data["reload"].(map[string]any)
	if refresh["interval_seconds"] != float64(3600) || refresh["ok"] != float64(2) || refresh["failed"] != float64(1) ||
		refresh["last_error"] != "FETCH_HTTP_STATUS: 403" {
		t.Errorf("refresh = %v", refresh)
	}
	if reload["count"] != float64(3) || reload["last_at"] != "2026-10-22T09:01:00Z" {
		t.Errorf("reload = %v", reload)
	}
	if _, has := reload["last_error"]; has {
		t.Error("an empty reload error is printed")
	}

	data = run(daemon.Status{})
	if _, has := data["refresh"]; has {
		t.Error("refresh is printed for a daemon that did not report it")
	}
	if _, has := data["reload"]; has {
		t.Error("reload is printed for a daemon that did not report it")
	}
}
