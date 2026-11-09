package main

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func newAuthReloadFixture(t *testing.T, route, legacy bool) *reloadFixture {
	t.Helper()
	f := &reloadFixture{home: t.TempDir(), script: &coreScript{port: 43210, inPlace: true}}
	var err error
	if f.hash, err = auth.Hash("fixture-password"); err != nil {
		t.Fatal(err)
	}
	account := config.Account{Login: "alice", Password: f.hash, AllowedClients: "127.0.0.1"}
	if route {
		account.Provider, account.ExitCountry = "p", "DE"
	}
	cfg := config.Default()
	cfg.Accounts = []config.Account{account}
	if route {
		cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.invalid/sub"}}
		f.saveNodes(t, "192.0.2.1", "n1")
	}
	if legacy {
		cfg.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	} else {
		cfg.Accounts[0].Password = config.InitPlaceholder
	}
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	old := coreStart
	coreStart = f.script.start
	t.Cleanup(func() { coreStart = old })
	f.l = newLiveAngel(upOptions{Home: f.home}, func(format string, args ...any) {
		f.mu.Lock()
		f.logs = append(f.logs, format)
		f.mu.Unlock()
	}, func() { f.stopped.Store(true) })
	d, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAuthenticator(d.cfg, f.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.l.start(t.Context(), d, a, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.l.close)
	return f
}

func TestInitClaimsSurviveReloadConfigLoadedBeforeApply(t *testing.T) {
	for _, tc := range []struct {
		name   string
		route  bool
		legacy bool
	}{
		{name: "direct policy account claim", legacy: false},
		{name: "direct policy legacy claim", legacy: true},
		{name: "route account claim", route: true},
		{name: "route legacy claim", route: true, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthReloadFixture(t, tc.route, tc.legacy)
			if tc.route {
				f.saveNodes(t, "192.0.2.2", "n1")
			} else {
				cfg, err := config.Load(config.ConfigPath(f.home))
				if err != nil {
					t.Fatal(err)
				}
				cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
				if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
					t.Fatal(err)
				}
			}
			// This is the state loaded by reload; the successful init claim lands
			// on disk and the authenticator's original config before apply.
			claimGeneration := persistClaimGeneration(f.l.startOpts.Authenticator)
			d, err := f.l.load()
			if err != nil {
				t.Fatal(err)
			}
			login := "alice"
			if tc.legacy {
				login = "legacy-user"
			}
			if !f.l.startOpts.Authenticator.Verify(login, "claimed-password") {
				t.Fatal("init credential claim was rejected")
			}
			if !f.l.reconfigureSince(d, claimGeneration) {
				t.Fatal("loaded configuration was not applied")
			}
			f.l.mu.Lock()
			applied := f.l.applied.cfg
			authState := f.l.startOpts.Authenticator
			f.l.mu.Unlock()
			persisted, err := config.Load(config.ConfigPath(f.home))
			if err != nil {
				t.Fatal(err)
			}
			if liveAuth(applied) != "enabled" || liveAuth(persisted) != "enabled" {
				t.Fatalf("claimed credentials are not enabled in applied/disk config: %q/%q", liveAuth(applied), liveAuth(persisted))
			}
			if tc.legacy {
				if applied.Auth.Login != login || persisted.Auth.Login != login || applied.Auth.Password != persisted.Auth.Password {
					t.Fatal("legacy claim differs between applied and disk config")
				}
			} else {
				appliedAccount, _ := applied.Account("alice")
				diskAccount, _ := persisted.Account("alice")
				if appliedAccount.Password == config.InitPlaceholder || appliedAccount.Password != diskAccount.Password {
					t.Fatal("account claim differs between applied and disk config")
				}
			}
			if !authState.Verify(login, "claimed-password") {
				t.Fatal("the installed authenticator lost its successful claim")
			}
			f.l.reload(t.Context())
			if f.script.calls() != 1 || f.reloads() != 1 {
				t.Fatalf("reload after claim restarted or reapplied: starts=%d reloads=%d", f.script.calls(), f.reloads())
			}
		})
	}
}

func TestReloadComparisonIncludesClaimsCompletedAfterLoad(t *testing.T) {
	for _, tc := range []struct {
		name        string
		routeChange bool
		legacy      bool
	}{
		{name: "account unchanged"},
		{name: "legacy unchanged", legacy: true},
		{name: "account route change", routeChange: true},
		{name: "legacy route change", routeChange: true, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthReloadFixture(t, tc.routeChange, tc.legacy)
			if tc.routeChange {
				f.saveNodes(t, "192.0.2.2", "n1")
			}
			f.l.mu.Lock()
			authenticator := f.l.startOpts.Authenticator
			applied := f.l.applied
			f.l.mu.Unlock()
			generation := persistClaimGeneration(authenticator)
			d, err := f.l.load()
			if err != nil {
				t.Fatal(err)
			}
			login := "alice"
			if tc.legacy {
				login = "legacy-user"
			}
			if !authenticator.Verify(login, "claimed-after-load") {
				t.Fatal("init credential claim was rejected")
			}
			d.port = f.l.actualPort
			candidate, same, inPlace := reloadComparison(authenticator, applied, d, generation)
			if tc.routeChange {
				if same || !inPlace {
					t.Fatalf("route comparison same=%v inPlace=%v, want changed/in-place", same, inPlace)
				}
				if !f.l.reconfigureSince(candidate, generation) {
					t.Fatal("changed route was not reconfigured")
				}
				if f.script.calls() != 1 {
					t.Fatalf("route update started core %d times, want 1", f.script.calls())
				}
				f.l.mu.Lock()
				claimed := f.l.applied.cfg
				f.l.mu.Unlock()
				if tc.legacy {
					if claimed.Auth.Login != login || claimed.Auth.Password == config.InitPlaceholder {
						t.Fatal("legacy claim was not retained by route reconfiguration")
					}
				} else {
					account, _ := claimed.Account(login)
					if account == nil || account.Password == config.InitPlaceholder {
						t.Fatal("account claim was not retained by route reconfiguration")
					}
				}
			} else if !same || inPlace {
				t.Fatalf("unchanged comparison same=%v inPlace=%v, want same/not-in-place", same, inPlace)
			}
		})
	}
}

func TestReloadReusesConnectionLoggedAuthenticatorAcrossRollback(t *testing.T) {
	f := newAuthReloadFixture(t, true, false)
	for i := 2; i <= 5; i++ {
		f.saveNodes(t, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}).String(), "n1")
		f.l.reload(t.Context())
	}
	if got := len(f.script.inst(0).reconfigured()); got != 4 {
		t.Fatalf("successful route reconfigurations=%d, want 4", got)
	}
	f.script.fail = map[int]error{2: errors.New("replacement failed")}
	cfg, err := config.Load(config.ConfigPath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen.Port++
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 3 {
		t.Fatalf("starts=%d, want failed replacement and rollback", f.script.calls())
	}
	f.l.mu.Lock()
	a := f.l.startOpts.Authenticator
	f.l.mu.Unlock()
	logged, ok := a.(*loggedAuth)
	if !ok || logged.inner == nil {
		t.Fatalf("rollback authenticator = %T, want one loggedAuth wrapper", a)
	}
	if users := a.Users(); len(users) != 1 || users[0] != "alice" {
		t.Fatalf("Users() = %v, want alice", users)
	}
	loopback := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000}
	outside := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 9), Port: 5000}
	if !mihauth.VerifyWithRemote(a, "alice", "fixture-password", loopback) {
		t.Fatal("remote-aware verification refused an allowed client")
	}
	if a.Verify("alice", "wrong-password") {
		t.Fatal("wrong password verified")
	}
	clog := f.l.connLog()
	clog.mu.Lock()
	failures := clog.loginFails["alice"]
	clog.mu.Unlock()
	if failures != 1 {
		t.Fatalf("failed login count=%d, want exactly one", failures)
	}
	if mihauth.VerifyWithRemote(a, "alice", "fixture-password", outside) {
		t.Fatal("remote-aware verification accepted a disallowed client")
	}
	clog.mu.Lock()
	failures = clog.loginFails["alice"]
	clog.mu.Unlock()
	if failures != 2 {
		t.Fatalf("login failure count after the remote rejection=%d, want 2", failures)
	}
	if !mihauth.VerifyWithRemote(a, "alice", "fixture-password", loopback) {
		t.Fatal("successful Users/Verify behavior changed after a rejected login")
	}
}

func TestInitClaimsAfterAppliedConfigStayInAppliedAndDisk(t *testing.T) {
	for _, tc := range []struct {
		name   string
		route  bool
		legacy bool
	}{
		{name: "direct", legacy: false},
		{name: "route", route: true},
		{name: "legacy direct", legacy: true},
		{name: "legacy route", route: true, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthReloadFixture(t, tc.route, tc.legacy)
			if tc.route {
				f.saveNodes(t, "192.0.2.2", "n1")
			} else {
				cfg, err := config.Load(config.ConfigPath(f.home))
				if err != nil {
					t.Fatal(err)
				}
				cfg.Accounts[0].AllowedClients = "192.0.2.0/24"
				if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
					t.Fatal(err)
				}
			}
			f.l.reload(t.Context())
			if f.script.calls() != 1 {
				t.Fatalf("policy/route reload starts=%d, want 1", f.script.calls())
			}
			login := "alice"
			if tc.legacy {
				login = "legacy-user"
			}
			if !f.l.startOpts.Authenticator.Verify(login, "claimed-after-apply") {
				t.Fatal("init credential claim after apply was rejected")
			}
			f.l.mu.Lock()
			applied := f.l.applied.cfg
			f.l.mu.Unlock()
			persisted, err := config.Load(config.ConfigPath(f.home))
			if err != nil {
				t.Fatal(err)
			}
			if liveAuth(applied) != "enabled" || liveAuth(persisted) != "enabled" {
				t.Fatalf("claimed credentials not enabled in applied/disk config: %q/%q", liveAuth(applied), liveAuth(persisted))
			}
			f.l.reload(t.Context())
			if f.script.calls() != 1 || f.reloads() != 1 {
				t.Fatalf("reload after claim restarted/reapplied: starts=%d reloads=%d", f.script.calls(), f.reloads())
			}
		})
	}
}

func TestPersistConfigRebindKeepsClaimDiskPrecondition(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	initial := config.Default()
	initial.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	if err := config.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	persist := newPersistConfig(initial)
	callback := newPersistWithTarget(path, initial, persist)
	generation := persist.generation
	current := *initial
	persist.rebind(&current, generation)
	changed := config.Default()
	changed.Auth = config.Auth{Login: "someone-else", Password: config.InitPlaceholder}
	if err := config.Save(path, changed); err != nil {
		t.Fatal(err)
	}
	if err := callback("alice", "not-a-real-hash"); !errors.Is(err, errClaimStale) {
		t.Fatalf("claim error=%v, want immutable original-pair refusal", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth.Login != "someone-else" || current.Auth.Login != config.InitPlaceholder {
		t.Fatal("a stale claim changed disk or rebound in-memory credentials")
	}
}
