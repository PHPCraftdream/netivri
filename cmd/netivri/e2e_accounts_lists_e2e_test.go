//go:build e2e

package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// Accounts with provider and country lists, end to end through the daemon:
// the first listed provider and country that has a node gets the traffic, and
// a listed country without nodes is skipped instead of failing `up`.
func TestE2EAccountsListsPriority(t *testing.T) {
	home := t.TempDir()
	pass := "lists-e2e-password"
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	p1Up := accountsServer(t)
	p2Up := accountsServer(t)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			// Both providers have a DE node: the first listed one wins.
			{Login: "alice", Password: hash, Provider: "p2,p1", ExitCountry: "DE"},
			{Login: "bob", Password: hash, Provider: "p1,p2", ExitCountry: "DE"},
			// Only p2 has an NL node: the first country beats the first provider.
			{Login: "carol", Password: hash, Provider: "p1,p2", ExitCountry: "NL,DE"},
			// JP has no node anywhere: skipped, DE of p1 is used.
			{Login: "dave", Password: hash, Provider: "p1", ExitCountry: "JP,DE"},
		}
		cfg.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://p1.invalid/sub"}, {Name: "p2", URL: "https://p2.invalid/sub"}}
	})
	accountsSaveSnapshot(t, home, "p1", []subscription.Node{accountsSocksNode("de-p1", "DE", p1Up)})
	accountsSaveSnapshot(t, home, "p2", []subscription.Node{accountsSocksNode("nl-p2", "NL", p2Up), accountsSocksNode("de-p2", "DE", p2Up)})

	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	listen := st.Data.Listen

	step := func(login string, wantP1, wantP2 int64) {
		t.Helper()
		accountsClient(t, listen, login, pass, backend)
		if p1Up.count.Load() != wantP1 || p2Up.count.Load() != wantP2 {
			t.Fatalf("%s: upstream counts p1=%d p2=%d, want p1=%d p2=%d",
				login, p1Up.count.Load(), p2Up.count.Load(), wantP1, wantP2)
		}
	}
	step("alice", 0, 1)
	step("bob", 1, 1)
	step("carol", 1, 2)
	step("dave", 2, 2)
	p.Stop()
	assertNoSecrets(t, p.Stdout(), p.Stderr(), pass, hash)
}

// The account commands take and print lists in the canonical form, and
// refuse a list that leaves the route without any node.
func TestE2EAccountsListCommands(t *testing.T) {
	home := t.TempDir()
	p1Up := accountsServer(t)
	p2Up := accountsServer(t)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://p1.invalid/sub"}, {Name: "p2", URL: "https://p2.invalid/sub"}}
	})
	accountsSaveSnapshot(t, home, "p1", []subscription.Node{accountsSocksNode("de-p1", "DE", p1Up)})
	accountsSaveSnapshot(t, home, "p2", []subscription.Node{accountsSocksNode("nl-p2", "NL", p2Up)})

	code, out, stderr := accountsCommand(t, home, "add", "alice", "--provider", "p2, p1", "--country", "nl,de")
	if code != 0 || !strings.Contains(out, `"provider":"p2,p1"`) || !strings.Contains(out, `"exit_country":"NL,DE"`) {
		t.Fatalf("add: exit %d, out %s, stderr %s", code, out, stderr)
	}
	// A country list needs only one country with a node.
	if code, out, stderr = accountsCommand(t, home, "set-country", "alice", "JP,DE"); code != 0 {
		t.Fatalf("set-country JP,DE: exit %d, out %s, stderr %s", code, out, stderr)
	}
	code, out, stderr = accountsCommand(t, home, "set-country", "alice", "JP,SE")
	if code != 5 || decodeErrEnvelope(t, stderr).Error.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("set-country JP,SE: exit %d, stderr %s", code, stderr)
	}
	if code, out, stderr = accountsCommand(t, home, "set-provider", "alice", "p1,ghost"); code != 5 ||
		decodeErrEnvelope(t, stderr).Error.Code != "SUB_NOT_FOUND" {
		t.Fatalf("set-provider with an unknown name: exit %d, stderr %s", code, stderr)
	}
	code, out, stderr = accountsCommand(t, home, "list")
	if code != 0 || !strings.Contains(out, `"provider":"p2,p1"`) || !strings.Contains(out, `"exit_country":"JP,DE"`) {
		t.Fatalf("list: exit %d, out %s, stderr %s", code, out, stderr)
	}
	// A provider in an account's list cannot be removed.
	code, out, stderr = accountsRun(t, home, "sub", "rm", "p2")
	if code != 6 || decodeErrEnvelope(t, stderr).Error.Code != "SUB_IN_USE" {
		t.Fatalf("sub rm: exit %d, out %s, stderr %s", code, out, stderr)
	}
}
