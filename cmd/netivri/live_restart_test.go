package main

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
	mihauth "github.com/metacubex/mihomo/component/auth"
)

func TestRetryRestoresAppliedButKeepsNewestValidConfig(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{2: errors.New("new start failed"), 3: errors.New("rollback failed"), 4: errors.New("recovery failed")}
	waits := make(chan time.Duration, 2)
	steps := make(chan struct{}, 2)
	f.l.retryApplied = make(chan struct{}, 1)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		select {
		case <-steps:
			return true
		case <-ctx.Done():
			return false
		}
	}
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("retry delay=%s", got)
	}
	cfg, err := config.Load(config.ConfigPath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Fetch.UpdateInterval = "2h"
	cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: "backup", URL: "https://backup.invalid/sub"})
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	steps <- struct{}{}
	if got := receiveRetrySignal(t, waits); got != 2*time.Second {
		t.Fatalf("next retry delay=%s", got)
	}
	steps <- struct{}{}
	select {
	case <-f.l.retryApplied:
	case <-time.After(time.Second):
		t.Fatal("retry did not restore previous core")
	}
	f.l.mu.Lock()
	appliedServer := onlyRoute(f.l.applied.pool.nodes)[0]["server"]
	applied, latest, opts := f.l.applied.cfg, f.l.latest, f.l.startOpts
	f.l.mu.Unlock()
	if appliedServer != "192.0.2.1" {
		t.Errorf("applied node=%v", appliedServer)
	}
	if applied.Fetch.UpdateInterval != "1h" {
		t.Errorf("applied interval=%q", applied.Fetch.UpdateInterval)
	}
	if latest.Fetch.UpdateInterval != "2h" || len(latest.Subscriptions) != 2 {
		t.Errorf("latest config interval=%q subscriptions=%d", latest.Fetch.UpdateInterval, len(latest.Subscriptions))
	}
	if serverOf(opts) != "192.0.2.1" {
		t.Errorf("start options node=%v", serverOf(opts))
	}
}

func TestFixBFingerprintBoundaries(t *testing.T) {
	base := testDesired()
	baseFingerprints := []string{listenerFingerprint(base), routesFingerprint(base), accountClientsFingerprint(base.cfg)}

	policy := testDesired()
	policy.cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
	if accountClientsFingerprint(policy.cfg) == baseFingerprints[2] {
		t.Fatal("account client policy did not change its fingerprint")
	}
	if listenerFingerprint(policy) != baseFingerprints[0] || routesFingerprint(policy) != baseFingerprints[1] {
		t.Fatal("account client policy changed listener or route fingerprint")
	}

	route := testDesired()
	route.pool.nodes["p~DE"][0]["server"] = "192.0.2.9"
	if routesFingerprint(route) == baseFingerprints[1] {
		t.Fatal("node change did not change route fingerprint")
	}

	for name, mutate := range map[string]func(*desiredState){
		"bind":             func(d *desiredState) { d.bind = "0.0.0.0" },
		"port":             func(d *desiredState) { d.port++ },
		"account password": func(d *desiredState) { d.cfg.Accounts[0].Password = "other-hash" },
		"global clients":   func(d *desiredState) { d.cfg.Listen.AllowedClients = "192.0.2.0/24" },
	} {
		t.Run(name, func(t *testing.T) {
			d := testDesired()
			mutate(&d)
			if listenerFingerprint(d) == baseFingerprints[0] {
				t.Fatal("listener fingerprint did not change")
			}
		})
	}

	ordered := testDesired()
	ordered.cfg.Accounts[0], ordered.cfg.Accounts[1] = ordered.cfg.Accounts[1], ordered.cfg.Accounts[0]
	if fingerprint(ordered) != fingerprint(base) {
		t.Fatal("account order changed fingerprint")
	}
	spelled := testDesired()
	spelled.cfg.Listen.AllowedClients = " 10.0.0.0/8 , loopback "
	canonical := testDesired()
	canonical.cfg.Listen.AllowedClients = "10.0.0.0/8,loopback"
	if listenerFingerprint(spelled) != listenerFingerprint(canonical) {
		t.Fatal("normalized client-rule spelling changed listener fingerprint")
	}
}

func TestFixBRoutedAccountPolicyReconfiguresInPlace(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.inst(0).inPlace = true
	cfg, err := config.Load(config.ConfigPath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 1 || f.reloads() != 1 {
		t.Fatalf("starts=%d reloads=%d, want 1 and 1", f.script.calls(), f.reloads())
	}
	reconfigs := f.script.inst(0).reconfigured()
	if len(reconfigs) != 1 {
		t.Fatalf("reconfigurations=%d, want 1", len(reconfigs))
	}
	var allowed bool
	for _, prefix := range reconfigs[0].AccountAllowedClients["alice"] {
		if prefix.Contains(netip.MustParseAddr("192.0.2.1")) {
			allowed = true
		}
	}
	if !allowed {
		t.Fatalf("alice allowed clients = %v, want prefix covering 192.0.2.1", reconfigs[0].AccountAllowedClients["alice"])
	}
}

func TestFixBDirectAccountPolicyUpdatesWithoutRestart(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Password: config.InitPlaceholder})
	cfg, err := config.Load(config.ConfigPath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 1 || f.script.inst(0).isClosed() {
		t.Fatalf("direct policy update restarted or closed core: starts=%d", f.script.calls())
	}
}

func TestFixBAccountPolicyAuthenticatorUpdatesAndPreservesInitClaim(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, AllowedClients: "127.0.0.1"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	a, err := newAuthenticator(cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: netip.MustParseAddr("192.0.2.1").AsSlice(), Port: 1234}
	if mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote) {
		t.Fatal("disallowed remote verified")
	}
	if cfg.Accounts[0].Password != config.InitPlaceholder {
		t.Fatal("disallowed remote claimed the init password")
	}
	updated := *cfg
	updated.Accounts = append([]config.Account(nil), cfg.Accounts...)
	updated.Accounts[0].AllowedClients = "192.0.2.0/24"
	updateAccountPolicy(a, &updated)
	if !mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote) {
		t.Fatal("allowed remote did not verify")
	}
	persisted, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Accounts[0].Password == config.InitPlaceholder {
		t.Fatal("successful init claim was not persisted")
	}
}

func TestFixBNewAuthenticatorReturnsNilWhenDisabled(t *testing.T) {
	a, err := newAuthenticator(config.Default(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("authenticator = %T, want nil", a)
	}
}

func TestLiveReloadUpdatesInstalledAuthenticatorPolicy(t *testing.T) {
	home := t.TempDir()
	hash, err := auth.Hash("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: hash, AllowedClients: "192.0.2.0/24"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	script := &coreScript{port: 43210}
	old := coreStart
	coreStart = script.start
	t.Cleanup(func() { coreStart = old })
	l := newLiveAngel(upOptions{Home: home}, func(string, ...any) {}, func() {})
	t.Cleanup(l.close)
	d, err := l.load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAuthenticator(d.cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.start(t.Context(), d, a, nil); err != nil {
		t.Fatal(err)
	}
	installed := script.call(0).Authenticator
	if installed == nil {
		t.Fatal("core was not started with an authenticator")
	}
	l.mu.Lock()
	before := l.inst
	l.mu.Unlock()
	remote := &net.TCPAddr{IP: netip.MustParseAddr("192.0.2.1").AsSlice(), Port: 1234}
	if !mihauth.VerifyWithRemote(installed, "alice", "fixture-password", remote) {
		t.Fatal("initially allowed client did not verify")
	}
	cfg.Accounts[0].AllowedClients = "198.51.100.0/24"
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	l.reload(t.Context())
	if mihauth.VerifyWithRemote(installed, "alice", "fixture-password", remote) {
		t.Fatal("client remained allowed after policy reload")
	}
	l.mu.Lock()
	current := l.startOpts.Authenticator
	currentInst := l.inst
	l.mu.Unlock()
	if current != installed {
		t.Fatal("reload replaced authenticator instance")
	}
	if currentInst != before {
		t.Fatal("reload replaced core instance")
	}
	if script.calls() != 1 || l.reloads != 1 {
		t.Fatalf("starts=%d reloads=%d", script.calls(), l.reloads)
	}
	l.reload(t.Context())
	if script.calls() != 1 || l.reloads != 1 {
		t.Fatalf("unchanged reload: starts=%d reloads=%d", script.calls(), l.reloads)
	}
}

func TestRetryRestartsOnListenerAndCredentialChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
	}{
		{name: "bind", change: func(cfg *config.Config) { cfg.Listen.Bind = "127.0.0.2" }},
		{name: "port", change: func(cfg *config.Config) { cfg.Listen.Port++ }},
		{name: "account password", change: func(cfg *config.Config) { cfg.Accounts[0].Password = config.InitPlaceholder }},
		{name: "global clients", change: func(cfg *config.Config) { cfg.Listen.AllowedClients = "192.0.2.0/24" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice"})
			cfg, err := config.Load(config.ConfigPath(f.home))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(cfg)
			if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
				t.Fatal(err)
			}
			f.l.reload(t.Context())
			if f.script.calls() != 2 || f.reloads() != 1 || !f.script.inst(0).isClosed() {
				t.Fatalf("starts=%d reloads=%d", f.script.calls(), f.reloads())
			}
		})
	}
}

func TestRetryUnchangedDesiredKeepsBackoff(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{2: errors.New("new start failed"), 3: errors.New("rollback failed")}
	waits := make(chan time.Duration, 1)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		<-ctx.Done()
		return false
	}
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("retry delay=%s, want 1s", got)
	}
	lastError := f.reloadErr()
	f.l.reload(t.Context())
	if f.script.calls() != 3 || f.reloadErr() != lastError || lastError == "" {
		t.Fatalf("unchanged retry: starts=%d error=%q want 3 and retained error %q", f.script.calls(), f.reloadErr(), lastError)
	}
}

func TestNewFailedConfigReplacesRecoveryTarget(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{
		2: errors.New("first start failed"),
		3: errors.New("rollback failed"),
		4: errors.New("second config start failed"),
	}
	waits := make(chan time.Duration, 2)
	steps := make(chan struct{}, 2)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		select {
		case <-steps:
			return true
		case <-ctx.Done():
			return false
		}
	}
	f.l.retryApplied = make(chan struct{}, 1)
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("first retry delay=%s, want 1s", got)
	}
	f.saveNodes(t, "192.0.2.3", "n1")
	f.l.reload(t.Context())
	if f.script.calls() != 4 {
		t.Fatalf("starts after changed config=%d, want 4", f.script.calls())
	}
	steps <- struct{}{}
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("new target retry delay=%s, want 1s", got)
	}
	select {
	case <-f.l.retryApplied:
	case <-time.After(time.Second):
		t.Fatal("retry did not apply latest target")
	}
	if f.script.calls() != 5 || serverOf(f.script.call(4)) != "192.0.2.3" {
		t.Fatalf("retry starts=%d node=%v, want 5 and latest node", f.script.calls(), serverOf(f.script.call(4)))
	}
	st := f.l.status("")
	if st.Core == "down" || st.Reload == nil || st.Reload.LastError != "" {
		t.Fatalf("status after latest target retry=%+v", st)
	}
}

func TestRetryBackoffCapsAtThirtySeconds(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: time.Second},
		{failures: 1, want: time.Second},
		{failures: 2, want: 2 * time.Second},
		{failures: 3, want: 4 * time.Second},
		{failures: 4, want: 8 * time.Second},
		{failures: 5, want: 16 * time.Second},
		{failures: 6, want: 30 * time.Second},
		{failures: math.MaxInt, want: 30 * time.Second},
	} {
		t.Run(strconv.Itoa(tc.failures), func(t *testing.T) {
			if got := retryBackoff(tc.failures); got != tc.want {
				t.Fatalf("retryBackoff(%d) = %s, want %s", tc.failures, got, tc.want)
			}
		})
	}
}

func receiveRetrySignal(t *testing.T, ch <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry signal")
		return 0
	}
}

func TestRetryWarningsThrottled(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	failures := map[int]error{2: errors.New("new start failed"), 3: errors.New("rollback failed")}
	for call := 4; call <= 14; call++ {
		failures[call] = errors.New("background retry failed")
	}
	f.script.fail = failures
	waits := make(chan time.Duration)
	steps := make(chan struct{})
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		select {
		case <-steps:
			return true
		case <-ctx.Done():
			return false
		}
	}
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("initial retry delay = %s, want 1s", got)
	}
	defer f.l.close()
	for failure := 1; failure <= 11; failure++ {
		steps <- struct{}{}
		if got := receiveRetrySignal(t, waits); got != retryBackoff(failure+1) {
			t.Fatalf("retry delay after failure %d = %s, want %s", failure, got, retryBackoff(failure+1))
		}
		count := 0
		for _, line := range strings.Split(f.logText(), "\n") {
			if strings.HasPrefix(line, "previous configuration restart failed:") {
				count++
			}
		}
		want := 1
		if failure >= 5 {
			want = 2
		}
		if failure >= 10 {
			want = 3
		}
		if count != want {
			t.Fatalf("warning count after failure %d = %d, want %d", failure, count, want)
		}
	}
}

func TestAccountPolicyConcurrentVerificationAndReplacement(t *testing.T) {
	hash, err := auth.Hash("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: hash, AllowedClients: "192.0.2.0/24"}}
	a, err := newAuthenticator(cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: netip.MustParseAddr("192.0.2.1").AsSlice(), Port: 1234}
	if !mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote) {
		t.Fatal("initial allowed client did not verify")
	}
	const readers = 8
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote)
				if mihauth.VerifyWithRemote(a, "alice", "wrong-password", remote) {
					select {
					case errs <- errors.New("wrong password verified"):
					default:
					}
					return
				}
			}
		}()
	}
	close(start)
	for i := 0; i < 100; i++ {
		updated := *cfg
		updated.Accounts = append([]config.Account(nil), cfg.Accounts...)
		if i%2 == 0 {
			updated.Accounts[0].AllowedClients = "192.0.2.0/24"
		} else {
			updated.Accounts[0].AllowedClients = "198.51.100.0/24"
		}
		updateAccountPolicy(a, &updated)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	updated := *cfg
	updated.Accounts = append([]config.Account(nil), cfg.Accounts...)
	updated.Accounts[0].AllowedClients = "198.51.100.0/24"
	updateAccountPolicy(a, &updated)
	if mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote) {
		t.Fatal("client verified after policy denied it")
	}
	updated.Accounts[0].AllowedClients = "192.0.2.0/24"
	updateAccountPolicy(a, &updated)
	if !mihauth.VerifyWithRemote(a, "alice", "fixture-password", remote) {
		t.Fatal("client did not verify after policy allowed it")
	}
}

func TestReconfigurePolicyOnlyWithServers(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice"})
	f.script.inst(0).inPlace = true
	server := servers.Spec{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 12345, Accounts: []string{"alice"}}
	f.l.applied.servers = []servers.Spec{server}
	d := f.l.applied
	cfg := *d.cfg
	cfg.Accounts = append([]config.Account(nil), d.cfg.Accounts...)
	cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
	d.cfg = &cfg
	if !f.l.reconfigure(d) {
		t.Fatal("policy-only reconfigure rejected")
	}
	if f.script.calls() != 1 {
		t.Fatalf("starts=%d, want 1", f.script.calls())
	}
	if got := len(f.script.inst(0).reconfigured()); got != 1 {
		t.Fatalf("reconfigurations=%d, want 1", got)
	}
	prefixes := f.script.inst(0).reconfigured()[0].AccountAllowedClients["alice"]
	if len(prefixes) != 1 || prefixes[0].String() != "192.0.2.0/24" {
		t.Fatal("account policy not forwarded")
	}
}

func TestRetryContextCancellationDoesNotCancelRecovery(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	ctx, cancel := context.WithCancel(context.Background())
	f.script.fail = map[int]error{2: errors.New("replacement failed"), 3: errors.New("rollback failed")}
	waits := make(chan time.Duration, 1)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		<-ctx.Done()
		return false
	}
	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(ctx)
	cancel()
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("retry delay=%s, want 1s", got)
	}
	f.l.mu.Lock()
	retrying := f.l.retryCancel != nil && f.l.retryTarget != nil
	f.l.mu.Unlock()
	if !retrying {
		t.Fatal("reload context cancellation stopped the owned recovery loop")
	}
	f.l.close()
}

func TestDoubleFailureRetryIsJoinedOnClose(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{
		2: errors.New("new start failed"),
		3: errors.New("rollback failed"),
	}
	waits := make(chan time.Duration, 1)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		<-ctx.Done()
		return false
	}

	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	st := f.l.status("")
	if st.Angel != "running" || st.Core != "down" || st.Reload == nil || st.Reload.LastError == "" || f.stopped.Load() {
		t.Fatalf("status after failed start and rollback = %+v", st)
	}
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("first retry delay = %s, want %s", got, time.Second)
	}

	done := make(chan struct{})
	go func() {
		f.l.close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not join the retry loop")
	}
}

func TestRetryEventuallyRestoresPreviousCore(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{
		2: errors.New("new start failed"),
		3: errors.New("rollback failed"),
		4: errors.New("retry failed"),
		5: errors.New("retry failed"),
	}
	waits := make(chan time.Duration, 3)
	steps := make(chan struct{}, 3)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		select {
		case <-steps:
			return true
		case <-ctx.Done():
			return false
		}
	}
	f.l.retryApplied = make(chan struct{}, 1)

	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if got := receiveRetrySignal(t, waits); got != want {
			t.Fatalf("retry delay %d = %s, want %s", i+1, got, want)
		}
		steps <- struct{}{}
	}
	select {
	case <-f.l.retryApplied:
	case <-time.After(time.Second):
		t.Fatal("retry did not restore the previous core")
	}

	if got := f.script.calls(); got != 6 {
		t.Fatalf("core starts = %d, want 6", got)
	}
	if got := serverOf(f.script.call(5)); got != "192.0.2.1" {
		t.Fatalf("restored node = %v, want previous node", got)
	}
	st := f.l.status("")
	if st.Core == "down" || st.Reload == nil || st.Reload.LastError != "" {
		t.Fatalf("status after restoration = %+v", st)
	}
}

func TestConfigChangeDuringRetryBackoffAppliesImmediately(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.script.fail = map[int]error{
		2: errors.New("new start failed"),
		3: errors.New("rollback failed"),
	}
	waits := make(chan time.Duration, 2)
	steps := make(chan struct{}, 1)
	f.l.retryWait = func(ctx context.Context, d time.Duration) bool {
		select {
		case waits <- d:
		case <-ctx.Done():
			return false
		}
		select {
		case <-steps:
			return true
		case <-ctx.Done():
			return false
		}
	}

	f.saveNodes(t, "192.0.2.2", "n1")
	f.l.reload(t.Context())
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("retry delay = %s, want %s", got, time.Second)
	}

	f.saveNodes(t, "192.0.2.3", "n1")
	f.l.reload(t.Context())
	if got := f.script.calls(); got != 4 {
		t.Fatalf("core starts after applying new config = %d, want 4", got)
	}
	if got := serverOf(f.script.call(3)); got != "192.0.2.3" {
		t.Fatalf("new core node = %v, want 192.0.2.3", got)
	}
	st := f.l.status("")
	if st.Core == "down" || st.Reload == nil || st.Reload.LastError != "" {
		t.Fatalf("status after applying new config = %+v", st)
	}

	steps <- struct{}{}
	if got := receiveRetrySignal(t, waits); got != time.Second {
		t.Fatalf("subsequent retry delay = %s, want %s", got, time.Second)
	}
	if got := f.script.calls(); got != 4 {
		t.Fatalf("stale retry started another core: starts = %d", got)
	}
}
