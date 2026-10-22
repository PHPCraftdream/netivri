//go:build e2e

package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// The daemon applies changes made to its files while it runs: no down and up.

const liveSettle = 7 * time.Second // more than the watcher's two polls

type liveStatus struct {
	Data struct {
		Accounts []struct {
			Login string `json:"login"`
		} `json:"accounts"`
		Reload struct {
			Count     uint64 `json:"count"`
			LastError string `json:"last_error"`
		} `json:"reload"`
		Refresh struct {
			IntervalSeconds int64 `json:"interval_seconds"`
		} `json:"refresh"`
	} `json:"data"`
}

func liveStatusOf(t *testing.T, home string) liveStatus {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "status")
	if code != 0 {
		t.Fatalf("status exit %d: %s%s", code, out, stderr)
	}
	var st liveStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return st
}

func (s liveStatus) hasAccount(login string) bool {
	for _, a := range s.Data.Accounts {
		if a.Login == login {
			return true
		}
	}
	return false
}

func waitLive(t *testing.T, home, what string, cond func(liveStatus) bool) liveStatus {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		st := liveStatusOf(t, home)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("not applied within 40s: %s (reloads %d, last error %q)", what, st.Data.Reload.Count, st.Data.Reload.LastError)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type liveFixture struct {
	home, listen, backend, pass, hash string
	p1, p2                            *accountsUpstream
	writeCfg                          func()
}

// startLive runs a foreground daemon with alice (provider p1, exit DE) and one
// stored DE node that leads to the upstream p1.
func startLive(t *testing.T, more ...string) *liveFixture {
	t.Helper()
	testutil.RequireKtavLib(t)
	f := &liveFixture{home: t.TempDir(), pass: "live-e2e-password"}
	var err error
	if f.hash, err = auth.Hash(f.pass); err != nil {
		t.Fatal(err)
	}
	f.backend = accountsEcho(t)
	f.p1, f.p2 = accountsServer(t), accountsServer(t)
	f.writeCfg = func() {
		writeConfig(t, f.home, func(cfg *config.Config) {
			cfg.Accounts = []config.Account{{Login: "alice", Password: f.hash, Provider: "p1", ExitCountry: "DE"}}
			for _, login := range more {
				cfg.Accounts = append(cfg.Accounts, config.Account{Login: login, Password: f.hash, Provider: "p1", ExitCountry: "DE"})
			}
			cfg.Subscriptions = []config.Subscription{
				{Name: "p1", URL: "https://p1.invalid/sub"},
				{Name: "p2", URL: "https://p2.invalid/sub"},
			}
		})
	}
	f.writeCfg()
	accountsSaveSnapshot(t, f.home, "p1", []subscription.Node{accountsSocksNode("de-old", "DE", f.p1)})
	accountsSaveSnapshot(t, f.home, "p2", []subscription.Node{accountsSocksNode("de-p2", "DE", f.p2)})
	p := startUp(t, f.home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	f.listen = p.Status().Data.Listen
	return f
}

func TestE2ELiveAccountAddedByCLI(t *testing.T) {
	f := startLive(t)
	accountsClient(t, f.listen, "alice", f.pass, f.backend)

	code, out, stderr := accountsCommand(t, f.home, "add", "bob", "--provider", "p1", "--country", "DE")
	if code != 0 {
		t.Fatalf("account add: %s%s", out, stderr)
	}
	st := waitLive(t, f.home, "bob appears in the running daemon", func(s liveStatus) bool {
		return s.hasAccount("bob") && s.Data.Reload.Count >= 1
	})
	if st.Data.Refresh.IntervalSeconds != 3600 {
		t.Errorf("refresh interval = %d seconds, want the default 3600", st.Data.Refresh.IntervalSeconds)
	}

	// bob's first connection claims the init placeholder.
	accountsClient(t, f.listen, "bob", "bob-claimed-password", f.backend)
	if f.p1.count.Load() != 2 {
		t.Fatalf("upstream p1 connections = %d, want 2 (alice, bob)", f.p1.count.Load())
	}
	cfg := loadConfigFile(t, f.home)
	alice, _ := cfg.Account("alice")
	bob, _ := cfg.Account("bob")
	if alice == nil || bob == nil || alice.Password != f.hash || !strings.HasPrefix(bob.Password, "$argon2id$") {
		t.Fatalf("accounts on disk after the claim: %+v", cfg.Accounts)
	}

	// Storing the claim must not restart the listener, and alice goes on.
	time.Sleep(liveSettle)
	if got := liveStatusOf(t, f.home).Data.Reload.Count; got != 1 {
		t.Errorf("reloads after the claim = %d, want 1", got)
	}
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	assertNoSecrets(t, out, stderr, f.pass, f.hash, "bob-claimed-password")
}

func TestE2ELiveAccountRemoved(t *testing.T) {
	f := startLive(t, "bob")
	open := socks5AuthConnect(t, f.listen, "bob", f.pass, f.backend)
	echoRoundTrip(t, open, open)

	code, out, stderr := accountsCommand(t, f.home, "rm", "bob")
	if code != 0 {
		t.Fatalf("account rm: %s%s", out, stderr)
	}
	waitLive(t, f.home, "bob is gone from the running daemon", func(s liveStatus) bool {
		return !s.hasAccount("bob") && s.Data.Reload.Count >= 1
	})

	// bob's open connection ended with the old listener...
	_ = open.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := open.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("bob's connection is still open after the account was removed: %v", err)
	}
	// ...and bob cannot log in again, while alice is not affected.
	c := dialProxy(t, f.listen)
	if m := socks5Greet(t, c, 0, 2); m != 0x02 {
		t.Fatalf("greeting method = %#x, want 0x02", m)
	}
	if code := socks5AuthReply(t, c, "bob", f.pass); code == 0 {
		t.Fatal("a removed account was accepted")
	}
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
}

// `account set-provider` moves an account to another subscription in a
// running daemon, and a change that would leave the account without nodes is
// refused before anything is written.
func TestE2ELiveSwitchSubscription(t *testing.T) {
	f := startLive(t)
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if f.p1.count.Load() != 1 || f.p2.count.Load() != 0 {
		t.Fatalf("upstream counts p1=%d p2=%d", f.p1.count.Load(), f.p2.count.Load())
	}

	// No subscription has a Finnish node: refused, nothing changes.
	code, out, stderr := accountsCommand(t, f.home, "set-country", "alice", "FI")
	if code != 5 {
		t.Fatalf("set-country FI exit = %d, want 5 (%s%s)", code, out, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("error code = %q, want NO_NODES_FOR_COUNTRY", got)
	}
	if a, _ := loadConfigFile(t, f.home).Account("alice"); a == nil || a.ExitCountry != "DE" || a.Provider != "p1" {
		t.Fatalf("a refused command changed alice: %+v", a)
	}
	time.Sleep(liveSettle)
	if got := liveStatusOf(t, f.home).Data.Reload.Count; got != 0 {
		t.Errorf("reloads after a refused command = %d, want 0", got)
	}

	// p2 has a German node: accepted and applied to the running daemon.
	code, out, stderr = accountsCommand(t, f.home, "set-provider", "alice", "p2")
	if code != 0 {
		t.Fatalf("set-provider: %s%s", out, stderr)
	}
	waitLive(t, f.home, "alice is routed through p2", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if f.p1.count.Load() != 1 || f.p2.count.Load() != 1 {
		t.Fatalf("after the switch upstream counts p1=%d p2=%d, want 1/1", f.p1.count.Load(), f.p2.count.Load())
	}
}

func TestE2ELiveSnapshotReplaced(t *testing.T) {
	f := startLive(t)
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if f.p1.count.Load() != 1 || f.p2.count.Load() != 0 {
		t.Fatalf("upstream counts p1=%d p2=%d", f.p1.count.Load(), f.p2.count.Load())
	}

	// What `sub update` or the hourly refresh does: a new node list on disk.
	accountsSaveSnapshot(t, f.home, "p1", []subscription.Node{accountsSocksNode("de-new", "DE", f.p2)})
	waitLive(t, f.home, "the new node list is applied", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if f.p1.count.Load() != 1 || f.p2.count.Load() != 1 {
		t.Fatalf("after the update upstream counts p1=%d p2=%d, want 1/1", f.p1.count.Load(), f.p2.count.Load())
	}

	// A provider that only renames the node (a traffic counter in the name)
	// must not restart the listener.
	renamed := accountsSocksNode("de-new", "DE", f.p2)
	renamed.Name = "de-new, 5 GB left"
	renamed.Raw["name"] = renamed.Name
	accountsSaveSnapshot(t, f.home, "p1", []subscription.Node{renamed})
	time.Sleep(liveSettle)
	if got := liveStatusOf(t, f.home).Data.Reload.Count; got != 1 {
		t.Errorf("reloads after a rename = %d, want 1", got)
	}
}

func TestE2ELiveBadConfigKeepsRunning(t *testing.T) {
	f := startLive(t)
	accountsClient(t, f.listen, "alice", f.pass, f.backend)

	if err := os.WriteFile(config.ConfigPath(f.home), []byte("selection.mode: turbo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := waitLive(t, f.home, "the broken file is reported", func(s liveStatus) bool {
		return strings.Contains(s.Data.Reload.LastError, "CONFIG_INVALID")
	})
	if st.Data.Reload.Count != 0 {
		t.Errorf("reloads after a broken file = %d, want 0", st.Data.Reload.Count)
	}
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if f.p1.count.Load() != 2 {
		t.Fatalf("upstream p1 connections = %d, want 2", f.p1.count.Load())
	}

	// Fixing the file clears the error without a restart.
	f.writeCfg()
	st = waitLive(t, f.home, "the error clears after the fix", func(s liveStatus) bool { return s.Data.Reload.LastError == "" })
	if st.Data.Reload.Count != 0 {
		t.Errorf("reloads after the fix = %d, want 0 (nothing changed)", st.Data.Reload.Count)
	}
}
