package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func TestAuthStatusAccounts(t *testing.T) {
	const shapeHash = "$argon2id$v=19$m=8,t=2,p=1"
	cases := []struct {
		name     string
		login    string
		password string
		accounts []config.Account
		want     string
	}{
		{name: "none", want: "none"},
		{name: "legacy enabled", login: "alice", password: shapeHash, want: "enabled"},
		{name: "legacy login init", login: config.InitPlaceholder, password: shapeHash, want: "init-pending"},
		{name: "legacy password init", login: "alice", password: config.InitPlaceholder, want: "init-pending"},
		{name: "accounts hash enabled", accounts: []config.Account{{Login: "alice", Password: shapeHash}}, want: "enabled"},
		{name: "accounts init", accounts: []config.Account{{Login: "alice", Password: config.InitPlaceholder}}, want: "init-pending"},
		{name: "mixed legacy init and hash account", login: config.InitPlaceholder, password: shapeHash, accounts: []config.Account{{Login: "bob", Password: shapeHash}}, want: "init-pending"},
		{name: "mixed legacy hash and init account", login: "alice", password: shapeHash, accounts: []config.Account{{Login: "bob", Password: config.InitPlaceholder}}, want: "init-pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authStatus(tc.login, tc.password, tc.accounts); got != tc.want {
				t.Errorf("authStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthStatusAccountHash(t *testing.T) {
	testutil.RequireKtavLib(t)
	hash, err := auth.Hash("test password")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: hash}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if got := authStatus("", "", cfg.Accounts); got != "enabled" {
		t.Fatalf("authStatus() = %q, want enabled", got)
	}
	var out bytes.Buffer
	if err := runAccount([]string{"list", "--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data struct {
			Accounts []struct {
				Login       string `json:"login"`
				PasswordSet bool   `json:"password_set"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Accounts) != 1 || !result.Data.Accounts[0].PasswordSet || result.Data.Accounts[0].Login != "alice" {
		t.Fatalf("account list = %+v", result.Data.Accounts)
	}
	if strings.Contains(out.String(), hash) || strings.Contains(out.String(), "password\":\"") {
		t.Fatalf("hash leaked in account output: %s", out.String())
	}
}

func TestAccountPersistUpdatesOnlyMatchingPassword(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := config.Default()
	legacyHash, err := auth.Hash("legacy-test-password")
	if err != nil {
		t.Fatal(err)
	}
	claimedHash, err := auth.Hash("claimed-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.Login, cfg.Auth.Password = "legacy", legacyHash
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := newPersistAccount(path, cfg)("alice", claimedHash); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadOrDefault(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts[0].Password != claimedHash || got.Accounts[1].Password != config.InitPlaceholder || got.Auth.Login != "legacy" || got.Auth.Password != legacyHash {
		t.Fatal("persisted config unexpectedly changed")
	}
}

func TestAccountStatusJSONSortedAndRedacted(t *testing.T) {
	old := daemonProbeLive
	daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) {
		return daemon.State{}, daemon.Status{Accounts: []daemon.AccountInfo{{Login: "z", Provider: "main", ExitCountry: "DE"}, {Login: "alice"}}}, nil
	}
	t.Cleanup(func() { daemonProbeLive = old })
	var out bytes.Buffer
	if err := runStatus([]string{"--home", t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data struct {
			Accounts []struct {
				Login    string `json:"login"`
				Provider string `json:"provider"`
				Country  string `json:"exit_country"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	accts := result.Data.Accounts
	if len(accts) != 2 || accts[0].Login != "alice" || accts[0].Provider != "" || accts[0].Country != "" ||
		accts[1].Login != "z" || accts[1].Provider != "main" || accts[1].Country != "DE" || strings.Contains(out.String(), "password") {
		t.Fatalf("unexpected status output: %s", out.String())
	}
}

func TestUpMissingAccountCountryDoesNotSpawn(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, ExitCountry: "DE"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	oldProbe, oldSpawn := daemonProbeLive, daemonSpawn
	spawns := 0
	daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) {
		return daemon.State{}, daemon.Status{}, errors.New("not running")
	}
	daemonSpawn = func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		spawns++
		return daemon.State{}, errors.New("unexpected spawn")
	}
	t.Cleanup(func() { daemonProbeLive, daemonSpawn = oldProbe, oldSpawn })
	err := runUp([]string{"--home", home}, &bytes.Buffer{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound || spawns != 0 {
		t.Fatalf("error=%v spawns=%d", err, spawns)
	}
}

func TestAccountCommandErrors(t *testing.T) {
	testutil.RequireKtavLib(t)
	cases := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{name: "add invalid login", args: []string{"add", "bad login"}, code: "ACCOUNT_INVALID", exit: cli.ExitUsage},
		{name: "add invalid country", args: []string{"add", "alice", "--country", "nope"}, code: "COUNTRY_INVALID", exit: cli.ExitUsage},
		{name: "same login different country", args: []string{"add", "alice", "--country", "NL"}, code: "ACCOUNT_EXISTS", exit: cli.ExitConflict},
		{name: "set-country missing login", args: []string{"set-country", "missing", "DE"}, code: "ACCOUNT_NOT_FOUND", exit: cli.ExitNotFound},
		{name: "rm missing login", args: []string{"rm", "missing"}, code: "ACCOUNT_NOT_FOUND", exit: cli.ExitNotFound},
		{name: "rm invalid login", args: []string{"rm", "bad login"}, code: "ACCOUNT_INVALID", exit: cli.ExitUsage},
		{name: "set-country invalid country", args: []string{"set-country", "alice", "nope"}, code: "COUNTRY_INVALID", exit: cli.ExitUsage},
		{name: "none with country", args: []string{"set-country", "alice", "--none", "DE"}, code: "USAGE", exit: cli.ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var out bytes.Buffer
			if err := runAccount([]string{"add", "alice", "--country", "DE", "--home", home}, &out); err != nil {
				t.Fatalf("seed account: %v", err)
			}
			args := append(append([]string{}, tc.args...), "--home", home)
			err := runAccount(args, &out)
			var ce *cli.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error = %v, want *cli.Error", err)
			}
			if ce.Code != tc.code || ce.Exit != tc.exit {
				t.Errorf("code/exit = %q/%d, want %q/%d", ce.Code, ce.Exit, tc.code, tc.exit)
			}
		})
	}
}
