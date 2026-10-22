package main

import (
	"bytes"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// accountCommand runs `account <args> --home <home>` and fails the test on error.
func accountCommand(t *testing.T, home string, args ...string) {
	t.Helper()
	var out bytes.Buffer
	if err := runAccount(append(args, "--home", home), &out); err != nil {
		t.Fatalf("account %v: %v", args, err)
	}
}

// loadAccounts reads the accounts back from the config file on disk.
func loadAccounts(t *testing.T, home string) []config.Account {
	t.Helper()
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg.Accounts
}

func TestAccountSetCountryPersists(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	// A country can only be set when some stored node has it.
	seedProviders(t, home, map[string][]subscription.Node{"s": {node("nl-1", "NL")}})
	accountCommand(t, home, "add", "alice")
	accountCommand(t, home, "set-country", "alice", "nl")
	got := loadAccounts(t, home)
	if len(got) != 1 || got[0].Login != "alice" || got[0].ExitCountry != "NL" {
		t.Fatalf("accounts after set-country = %+v, want alice with NL", got)
	}
	accountCommand(t, home, "set-country", "alice", "--none")
	got = loadAccounts(t, home)
	if len(got) != 1 || got[0].ExitCountry != "" {
		t.Fatalf("accounts after --none = %+v, want alice without a country", got)
	}
}

func TestAccountRmPersistsAndKeepsOthers(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	accountCommand(t, home, "add", "alice", "--country", "DE")
	accountCommand(t, home, "add", "bob", "--country", "NL")
	accountCommand(t, home, "rm", "alice")
	got := loadAccounts(t, home)
	if len(got) != 1 || got[0].Login != "bob" || got[0].ExitCountry != "NL" {
		t.Fatalf("accounts after rm = %+v, want only bob with NL", got)
	}
}
