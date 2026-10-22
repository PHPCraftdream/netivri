package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// accountProviderHome seeds a home with two providers and returns it.
func accountProviderHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// Both providers have stored nodes: switching an account to one needs them.
	seedProviders(t, home, map[string][]subscription.Node{
		"p1": {node("p1-de", "DE"), node("p1-nl", "NL")},
		"p2": {node("p2-de", "DE")},
	})
	return home
}

func runAccountOK(t *testing.T, home string, args ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := runAccount(append(args, "--home", home), &out); err != nil {
		t.Fatalf("account %v: %v (%s)", args, err, out.String())
	}
	return out.Bytes()
}

func runAccountErr(t *testing.T, home string, args ...string) *cli.Error {
	t.Helper()
	var out bytes.Buffer
	err := runAccount(append(args, "--home", home), &out)
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("account %v: expected a *cli.Error, got %v (%s)", args, err, out.String())
	}
	return ce
}

func TestAccountAddWithProvider(t *testing.T) {
	home := accountProviderHome(t)
	out := runAccountOK(t, home, "add", "alice", "--provider", "p1", "--country", "de")
	if !bytes.Contains(out, []byte(`"login":"alice","provider":"p1","exit_country":"DE","password_set":false`)) {
		t.Fatalf("add output %s", out)
	}
	// Persisted, and reloaded from disk.
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("alice"); a == nil || a.Provider != "p1" || a.ExitCountry != "DE" {
		t.Fatalf("persisted account %+v", cfg.Accounts)
	}
	// Idempotent with the same provider and country.
	out = runAccountOK(t, home, "add", "alice", "--provider", "p1", "--country", "DE")
	if !bytes.Contains(out, []byte(`"existing":true`)) {
		t.Fatalf("idempotent output %s", out)
	}
	// Any difference is a conflict.
	if ce := runAccountErr(t, home, "add", "alice", "--provider", "p2", "--country", "DE"); ce.Code != "ACCOUNT_EXISTS" || ce.Exit != cli.ExitConflict {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "add", "alice", "--provider", "p1", "--country", "NL"); ce.Code != "ACCOUNT_EXISTS" || ce.Exit != cli.ExitConflict {
		t.Fatalf("unexpected error: %#v", ce)
	}
	// Unknown or invalid provider.
	if ce := runAccountErr(t, home, "add", "bob", "--provider", "ghost"); ce.Code != "SUB_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "add", "bob", "--provider", "bad name"); ce.Exit != cli.ExitUsage {
		t.Fatalf("unexpected error: %#v", ce)
	}
	// Neither add persisted the account.
	cfg, err = config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.Account("bob"); i >= 0 {
		t.Fatalf("bob persisted: %+v", cfg.Accounts)
	}
}

func TestAccountSetProvider(t *testing.T) {
	home := accountProviderHome(t)
	runAccountOK(t, home, "add", "alice")
	out := runAccountOK(t, home, "set-provider", "alice", "p2")
	if !bytes.Contains(out, []byte(`"login":"alice","provider":"p2","exit_country":""`)) {
		t.Fatalf("set-provider output %s", out)
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("alice"); a == nil || a.Provider != "p2" {
		t.Fatalf("persisted account %+v", cfg.Accounts)
	}
	// --none clears the provider.
	out = runAccountOK(t, home, "set-provider", "alice", "--none")
	if !bytes.Contains(out, []byte(`"provider":""`)) {
		t.Fatalf("clear output %s", out)
	}
	cfg, err = config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("alice"); a == nil || a.Provider != "" {
		t.Fatalf("cleared account %+v", cfg.Accounts)
	}
	// Error paths.
	if ce := runAccountErr(t, home, "set-provider", "ghost", "p1"); ce.Code != "ACCOUNT_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "set-provider", "alice", "ghost"); ce.Code != "SUB_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "set-provider", "alice", "bad name"); ce.Exit != cli.ExitUsage {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "set-provider", "alice"); ce.Exit != cli.ExitUsage {
		t.Fatalf("unexpected error: %#v", ce)
	}
}

func TestAccountListShowsProviderField(t *testing.T) {
	home := accountProviderHome(t)
	runAccountOK(t, home, "add", "zoe", "--provider", "p1", "--country", "NL")
	runAccountOK(t, home, "add", "amy")
	out := runAccountOK(t, home, "list")
	// provider sits between login and exit_country; sorted by login.
	if !bytes.Contains(out, []byte(`"login":"amy","provider":"","exit_country":""`)) {
		t.Fatalf("list output %s", out)
	}
	if !bytes.Contains(out, []byte(`"login":"zoe","provider":"p1","exit_country":"NL"`)) {
		t.Fatalf("list output %s", out)
	}
	var result struct {
		Data struct {
			Accounts []struct {
				Provider string `json:"provider"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Accounts) != 2 {
		t.Fatalf("accounts = %+v", result.Data.Accounts)
	}
}
