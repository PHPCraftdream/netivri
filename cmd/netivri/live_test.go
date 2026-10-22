package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func testDesired() desiredState {
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Login: "alice", Password: "$argon2id$alice", Provider: "p", ExitCountry: "DE"},
		{Login: "bob", Password: config.InitPlaceholder},
	}
	return desiredState{
		cfg:  cfg,
		bind: "127.0.0.1",
		port: 7890,
		pool: routePool{
			nodes:         map[string][]map[string]any{"p~DE": {{"name": "n1", "type": "socks5", "server": "192.0.2.1", "port": 1080}}},
			labels:        map[string][]string{"p~DE": {"p/n1"}},
			accountRoutes: map[string]string{"alice": "p~DE"},
		},
	}
}

func TestFingerprint(t *testing.T) {
	base := fingerprint(testDesired())
	if base != fingerprint(testDesired()) {
		t.Fatal("the same state gives different fingerprints")
	}
	same := map[string]func(*desiredState){
		"display name of a node": func(d *desiredState) { d.pool.nodes["p~DE"][0]["name"] = "n1 (12 GB left)" },
		"subscription list": func(d *desiredState) {
			d.cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.invalid/sub"}}
		},
		"refresh interval": func(d *desiredState) { d.cfg.Fetch.UpdateInterval = "30m" },
		"account order": func(d *desiredState) {
			d.cfg.Accounts[0], d.cfg.Accounts[1] = d.cfg.Accounts[1], d.cfg.Accounts[0]
		},
	}
	for name, mutate := range same {
		d := testDesired()
		mutate(&d)
		if got := fingerprint(d); got != base {
			t.Errorf("%s changed the fingerprint, which would restart the listener for nothing", name)
		}
	}
	differs := map[string]func(*desiredState){
		"node server":   func(d *desiredState) { d.pool.nodes["p~DE"][0]["server"] = "192.0.2.2" },
		"node removed":  func(d *desiredState) { d.pool.nodes["p~DE"] = nil },
		"node label":    func(d *desiredState) { d.pool.labels["p~DE"][0] = "p/n2" },
		"account route": func(d *desiredState) { d.pool.accountRoutes["alice"] = "p~NL" },
		"account added": func(d *desiredState) {
			d.cfg.Accounts = append(d.cfg.Accounts, config.Account{Login: "carol", Password: "init"})
		},
		"account password": func(d *desiredState) { d.cfg.Accounts[0].Password = "$argon2id$other" },
		"claimed password": func(d *desiredState) { d.cfg.Accounts[1].Password = "$argon2id$bob" },
		"auth login":       func(d *desiredState) { d.cfg.Auth.Login = "legacy" },
		"auth password":    func(d *desiredState) { d.cfg.Auth.Password = "$argon2id$legacy" },
		"port":             func(d *desiredState) { d.port = 7891 },
		"bind":             func(d *desiredState) { d.bind = "0.0.0.0" },
	}
	for name, mutate := range differs {
		d := testDesired()
		mutate(&d)
		if got := fingerprint(d); got == base {
			t.Errorf("%s did not change the fingerprint, so the change would never be applied", name)
		}
	}
}

func TestWatchSignature(t *testing.T) {
	home := t.TempDir()
	empty := watchSignature(home)
	if empty != watchSignature(home) {
		t.Fatal("signature of an unchanged directory is not stable")
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := config.ConfigPath(home)
	subPath := filepath.Join(home, "subscriptions", "main.json")

	write(cfgPath, "listen.port: 7890\n")
	withConfig := watchSignature(home)
	if withConfig == empty {
		t.Error("a new config file does not change the signature")
	}
	write(subPath, "{}")
	withSub := watchSignature(home)
	if withSub == withConfig {
		t.Error("a new subscription does not change the signature")
	}
	write(filepath.Join(home, "subscriptions", ".netivri-sub-1.tmp"), "half written")
	write(filepath.Join(home, "stats.db"), "samples")
	if got := watchSignature(home); got != withSub {
		t.Error("temporary files and unrelated files change the signature")
	}
	write(subPath, `{"nodes": []}`)
	if got := watchSignature(home); got == withSub {
		t.Error("a rewritten subscription does not change the signature")
	}
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if got := watchSignature(home); got == withSub {
		t.Error("a removed config file does not change the signature")
	}
}

func TestPersistAccountKeepsEditsMadeOnDisk(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	path := config.ConfigPath(home)
	hash, err := auth.Hash("claimed-password")
	if err != nil {
		t.Fatal(err)
	}
	// What the daemon loaded: alice only.
	loaded := config.Default()
	loaded.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	// What is on disk now: another command added carol and bob since.
	onDisk := config.Default()
	onDisk.Accounts = []config.Account{
		{Login: "alice", Password: config.InitPlaceholder},
		{Login: "bob", Password: config.InitPlaceholder, ExitCountry: "NL"},
		{Login: "carol", Password: config.InitPlaceholder},
	}
	if err := config.Save(path, onDisk); err != nil {
		t.Fatal(err)
	}

	if err := newPersistAccount(path, loaded)("alice", hash); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 3 {
		t.Fatalf("accounts on disk = %+v, want the three that were there", got.Accounts)
	}
	if got.Accounts[0].Password != hash || got.Accounts[1].Password != config.InitPlaceholder ||
		got.Accounts[1].ExitCountry != "NL" || got.Accounts[2].Login != "carol" {
		t.Fatalf("accounts on disk = %+v", got.Accounts)
	}
	if loaded.Accounts[0].Password != hash {
		t.Error("the in-memory account was not updated")
	}
}

func TestPersistRefusesAClaimOnAChangedFile(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	path := config.ConfigPath(home)
	newHash, err := auth.Hash("someone-elses-password")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := auth.Hash("late-claim")
	if err != nil {
		t.Fatal(err)
	}
	loaded := config.Default()
	loaded.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	loaded.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	onDisk := config.Default()
	onDisk.Auth = config.Auth{Login: "legacy", Password: newHash}
	onDisk.Accounts = []config.Account{{Login: "alice", Password: newHash}}
	if err := config.Save(path, onDisk); err != nil {
		t.Fatal(err)
	}

	if err := newPersistAccount(path, loaded)("alice", claimed); !errors.Is(err, errClaimStale) {
		t.Fatalf("account claim error = %v, want errClaimStale", err)
	}
	if err := newPersist(path, loaded)("alice", claimed); !errors.Is(err, errClaimStale) {
		t.Fatalf("legacy claim error = %v, want errClaimStale", err)
	}
	if loaded.Accounts[0].Password != config.InitPlaceholder || loaded.Auth.Login != config.InitPlaceholder {
		t.Error("a refused claim changed the in-memory configuration")
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts[0].Password != newHash || got.Auth.Login != "legacy" {
		t.Error("a refused claim rewrote the file")
	}
}

func TestErrText(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", &cli.Error{Code: "NO_NODES_FOR_COUNTRY", Message: "no nodes available for country DE"})
	if got := errText(wrapped); got != "NO_NODES_FOR_COUNTRY: no nodes available for country DE" {
		t.Errorf("errText(cli error) = %q", got)
	}
	if got := errText(errors.New("boom")); got != "boom" {
		t.Errorf("errText(plain error) = %q", got)
	}
}

func TestPortOf(t *testing.T) {
	cases := map[string]int{"127.0.0.1:7890": 7890, "[::1]:8080": 8080, "": 0, "nonsense": 0, "127.0.0.1:x": 0}
	for addr, want := range cases {
		if got := portOf(addr); got != want {
			t.Errorf("portOf(%q) = %d, want %d", addr, got, want)
		}
	}
}
