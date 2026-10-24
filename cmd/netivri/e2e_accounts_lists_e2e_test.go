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
//
// Every account has providers and nodes of its own. The fake upstreams cannot
// answer the background probes, so a node that sits in several routes is
// marked down at start, and which tier is "up" would then depend on timing.
func TestE2EAccountsListsPriority(t *testing.T) {
	home := t.TempDir()
	pass := "lists-e2e-password"
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	ups := map[string]*accountsUpstream{}
	for _, name := range []string{"a1", "a2", "c1", "c2", "d1", "e1", "ffr", "fde"} {
		ups[name] = accountsServer(t)
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			// Both providers have a DE node: the first listed one wins.
			{Login: "alice", Password: hash, Provider: "a2,a1", ExitCountry: "DE"},
			// Only c2 has an NL node: the first country beats the first provider.
			{Login: "carol", Password: hash, Provider: "c1,c2", ExitCountry: "NL,DE"},
			// JP has no node anywhere: skipped, DE of d1 is used.
			{Login: "dave", Password: hash, Provider: "d1", ExitCountry: "JP,DE"},
			// "*" takes whatever is left: DE has no node, so the FR node of e1 is used.
			{Login: "erin", Password: hash, Provider: "e1", ExitCountry: "DE,*"},
			// FR is listed first, so the FR node wins over the DE node that "*" covers.
			{Login: "frank", Password: hash, Provider: "f1", ExitCountry: "FR,*"},
		}
		for _, name := range []string{"a1", "a2", "c1", "c2", "d1", "e1", "f1"} {
			cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: name, URL: "https://" + name + ".invalid/sub"})
		}
	})
	accountsSaveSnapshot(t, home, "a1", []subscription.Node{accountsSocksNode("de-a1", "DE", ups["a1"])})
	accountsSaveSnapshot(t, home, "a2", []subscription.Node{accountsSocksNode("de-a2", "DE", ups["a2"])})
	accountsSaveSnapshot(t, home, "c1", []subscription.Node{accountsSocksNode("de-c1", "DE", ups["c1"])})
	accountsSaveSnapshot(t, home, "c2", []subscription.Node{accountsSocksNode("nl-c2", "NL", ups["c2"]), accountsSocksNode("de-c2", "DE", ups["c2"])})
	accountsSaveSnapshot(t, home, "d1", []subscription.Node{accountsSocksNode("de-d1", "DE", ups["d1"])})
	accountsSaveSnapshot(t, home, "e1", []subscription.Node{accountsSocksNode("fr-e1", "FR", ups["e1"])})
	accountsSaveSnapshot(t, home, "f1", []subscription.Node{accountsSocksNode("de-f1", "DE", ups["fde"]), accountsSocksNode("fr-f1", "FR", ups["ffr"])})

	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	listen := st.Data.Listen

	counts := func() map[string]int64 {
		out := map[string]int64{}
		for name, up := range ups {
			out[name] = up.count.Load()
		}
		return out
	}
	step := func(login, wantUpstream string) {
		t.Helper()
		before := counts()
		accountsClient(t, listen, login, pass, backend)
		after := counts()
		for name := range ups {
			want := before[name]
			if name == wantUpstream {
				want++
			}
			if after[name] != want {
				t.Fatalf("%s: upstream %s count %d, want %d (all: %v)", login, name, after[name], want, after)
			}
		}
	}
	step("alice", "a2")
	step("carol", "c2")
	step("dave", "d1")
	step("erin", "e1")
	step("frank", "ffr")
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
