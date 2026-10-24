package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func loadedAccount(t *testing.T, home, login string) config.Account {
	t.Helper()
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Account(login)
	if a == nil {
		t.Fatalf("account %q is not stored", login)
	}
	return *a
}

func TestAccountAddWithLists(t *testing.T) {
	home := accountProviderHome(t)
	out := runAccountOK(t, home, "add", "alice", "--provider", "p2, p1", "--country", "nl,de,NL")
	if !bytes.Contains(out, []byte(`"provider":"p2,p1","exit_country":"NL,DE"`)) {
		t.Fatalf("add output %s", out)
	}
	if a := loadedAccount(t, home, "alice"); a.Provider != "p2,p1" || a.ExitCountry != "NL,DE" {
		t.Fatalf("stored %+v", a)
	}
	// The same lists in another spelling are the same account.
	out = runAccountOK(t, home, "add", "alice", "--provider", "p2,p1", "--country", "NL,DE")
	if !bytes.Contains(out, []byte(`"existing":true`)) {
		t.Fatalf("idempotent output %s", out)
	}
	// Another order is another route.
	if ce := runAccountErr(t, home, "add", "alice", "--provider", "p1,p2", "--country", "NL,DE"); ce.Code != "ACCOUNT_EXISTS" {
		t.Fatalf("unexpected error: %#v", ce)
	}
}

func TestAccountAddListsRejectBadItems(t *testing.T) {
	home := accountProviderHome(t)
	for _, tc := range []struct {
		args []string
		code string
		exit int
	}{
		{[]string{"--country", "DE,"}, "COUNTRY_INVALID", cli.ExitUsage},
		{[]string{"--country", "DE,NLD"}, "COUNTRY_INVALID", cli.ExitUsage},
		{[]string{"--provider", "p1,,p2"}, "USAGE", cli.ExitUsage},
		{[]string{"--provider", "p1,ghost"}, "SUB_NOT_FOUND", cli.ExitNotFound},
	} {
		ce := runAccountErr(t, home, append([]string{"add", "bob"}, tc.args...)...)
		if ce.Exit != tc.exit || (tc.code != "USAGE" && ce.Code != tc.code) {
			t.Fatalf("%v: %#v", tc.args, ce)
		}
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("bob"); a != nil {
		t.Fatalf("a refused add stored %+v", a)
	}
}

func TestAccountSetListsAndPreCheck(t *testing.T) {
	home := accountProviderHome(t)
	runAccountOK(t, home, "add", "alice")
	runAccountOK(t, home, "set-provider", "alice", "p2,p1")
	runAccountOK(t, home, "set-country", "alice", "nl, de")
	if a := loadedAccount(t, home, "alice"); a.Provider != "p2,p1" || a.ExitCountry != "NL,DE" {
		t.Fatalf("stored %+v", a)
	}
	// Repeating a list in another spelling changes nothing and succeeds.
	runAccountOK(t, home, "set-country", "alice", "NL,DE")
	// One country with nodes is enough for the route to exist.
	runAccountOK(t, home, "set-country", "alice", "FI,NL")
	if a := loadedAccount(t, home, "alice"); a.ExitCountry != "FI,NL" {
		t.Fatalf("stored %+v", a)
	}
	// No node anywhere on the route: refused, nothing written.
	if ce := runAccountErr(t, home, "set-country", "alice", "FI,SE"); ce.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "set-provider", "alice", "p1,ghost"); ce.Code != "SUB_NOT_FOUND" {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if ce := runAccountErr(t, home, "set-country", "alice", ""); ce.Code != "COUNTRY_INVALID" {
		t.Fatalf("unexpected error: %#v", ce)
	}
	if a := loadedAccount(t, home, "alice"); a.Provider != "p2,p1" || a.ExitCountry != "FI,NL" {
		t.Fatalf("a refused change wrote %+v", a)
	}
}

// A provider that is in any account's list cannot be removed.
func TestSubRmRefusesProviderInAccountList(t *testing.T) {
	app, _, _ := seedCountriesApp(t, config.Subscription{Name: "p1", URL: "https://x.invalid/s"}, config.Subscription{Name: "p2", URL: "https://y.invalid/s"})
	cfg, err := app.load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = append(cfg.Accounts, config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "p1,p2"})
	if err := config.Save(app.configPath(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p1", "p2"} {
		err := app.rm([]string{name})
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "SUB_IN_USE" {
			t.Fatalf("rm %s: %#v", name, err)
		}
	}
}

// "*" is accepted as "any other country", also alone, and keeps its place.
func TestAccountCountryAnyStar(t *testing.T) {
	home := accountProviderHome(t)
	out := runAccountOK(t, home, "add", "alice", "--provider", "p1", "--country", "nl, *, de")
	if !bytes.Contains(out, []byte(`"exit_country":"NL,*,DE"`)) {
		t.Fatalf("add output %s", out)
	}
	runAccountOK(t, home, "set-country", "alice", "*")
	if a := loadedAccount(t, home, "alice"); a.ExitCountry != "*" {
		t.Fatalf("stored %+v", a)
	}
	// Any country always has a node while the provider has one.
	runAccountOK(t, home, "set-country", "alice", "FI,*")
	if ce := runAccountErr(t, home, "set-country", "alice", "FI,**"); ce.Code != "COUNTRY_INVALID" {
		t.Fatalf("unexpected error: %#v", ce)
	}
	// A star alone without providers is a route through every node.
	runAccountOK(t, home, "add", "bob", "--country", "*")
	if a := loadedAccount(t, home, "bob"); a.Provider != "" || a.ExitCountry != "*" {
		t.Fatalf("stored %+v", a)
	}
}
