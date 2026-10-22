package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// seedProviders configures the subscriptions and stores their node snapshots
// in home; accounts already in the config are kept.
func seedProviders(t *testing.T, home string, subs map[string][]subscription.Node) {
	t.Helper()
	testutil.RequireKtavLib(t)
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	names := make([]string, 0, len(subs))
	for name := range subs {
		names = append(names, name)
	}
	sort.Strings(names)
	cfg.Subscriptions = nil
	for _, name := range names {
		cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: name, URL: "https://" + name + ".invalid/sub"})
		if err := store.Save(name, subscription.Snapshot{Nodes: subs[name]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
}

// switchHome is a home with two providers and three accounts:
//
//	p1 offers DE and NL, p2 offers DE and FI
//	alice: p1, DE    bob: p1, NL    carol: no provider, no country
func switchHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	seedProviders(t, home, map[string][]subscription.Node{
		"p1": {node("p1-de", "DE"), node("p1-nl", "NL")},
		"p2": {node("p2-de", "DE"), node("p2-fi", "FI")},
	})
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{
		{Login: "alice", Password: config.InitPlaceholder, Provider: "p1", ExitCountry: "DE"},
		{Login: "bob", Password: config.InitPlaceholder, Provider: "p1", ExitCountry: "NL"},
		{Login: "carol", Password: config.InitPlaceholder},
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

func accountByLogin(t *testing.T, home, login string) config.Account {
	t.Helper()
	for _, a := range loadAccounts(t, home) {
		if a.Login == login {
			return a
		}
	}
	t.Fatalf("no account %q on disk", login)
	return config.Account{}
}

func wantNoNodes(t *testing.T, ce *cli.Error, login string) {
	t.Helper()
	if ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("error = %+v, want NO_NODES_FOR_COUNTRY (exit 5)", ce)
	}
	if !strings.Contains(ce.Message, "account "+login) || !strings.Contains(ce.Message, "nothing was changed") {
		t.Errorf("message %q must name the account %s and say nothing was changed", ce.Message, login)
	}
	if !strings.Contains(ce.Hint, "sub countries") {
		t.Errorf("hint %q does not point to sub countries", ce.Hint)
	}
	if strings.Contains(ce.Message, ".invalid") {
		t.Errorf("message %q mentions a subscription URL", ce.Message)
	}
}

func TestSetProviderRefusedWithoutNodesChangesNothing(t *testing.T) {
	home := switchHome(t)
	// bob exits through NL and p2 has no NL node; alice would be fine.
	ce := runAccountErr(t, home, "set-provider", "alice,bob", "p2")
	wantNoNodes(t, ce, "bob")
	if a := accountByLogin(t, home, "alice"); a.Provider != "p1" {
		t.Errorf("alice moved to %q although the command was refused", a.Provider)
	}
	if a := accountByLogin(t, home, "bob"); a.Provider != "p1" {
		t.Errorf("bob moved to %q although the command was refused", a.Provider)
	}

	// A single account is refused the same way.
	wantNoNodes(t, runAccountErr(t, home, "set-provider", "bob", "p2"), "bob")
}

func TestSetProviderSeveralAccountsAndAll(t *testing.T) {
	home := switchHome(t)
	runAccountOK(t, home, "set-country", "bob", "DE") // p1 has DE: allowed

	out := runAccountOK(t, home, "set-provider", "alice,bob", "p2")
	if !strings.Contains(string(out), `"accounts":[`) || strings.Contains(string(out), `"account":`) {
		t.Fatalf("several logins must print the accounts form: %s", out)
	}
	for _, login := range []string{"alice", "bob"} {
		if a := accountByLogin(t, home, login); a.Provider != "p2" || a.ExitCountry != "DE" {
			t.Errorf("%s = %+v, want provider p2 and country DE", login, a)
		}
	}
	if a := accountByLogin(t, home, "carol"); a.Provider != "" {
		t.Errorf("carol was not named but changed: %+v", a)
	}

	// --all: carol has no country, so she only needs some node of p1.
	out = runAccountOK(t, home, "set-provider", "--all", "p1")
	if strings.Count(string(out), `"login":`) != 3 {
		t.Fatalf("--all must report every account: %s", out)
	}
	for _, a := range loadAccounts(t, home) {
		if a.Provider != "p1" {
			t.Errorf("%s = %+v, want provider p1", a.Login, a)
		}
	}
}

func TestSetProviderAllRefusedAsAWhole(t *testing.T) {
	home := switchHome(t)
	// bob (NL) cannot move to p2, so nobody moves.
	wantNoNodes(t, runAccountErr(t, home, "set-provider", "--all", "p2"), "bob")
	for _, a := range loadAccounts(t, home) {
		if a.Login != "carol" && a.Provider != "p1" {
			t.Errorf("%s moved to %q", a.Login, a.Provider)
		}
	}
	if a := accountByLogin(t, home, "carol"); a.Provider != "" {
		t.Errorf("carol moved to %q", a.Provider)
	}
}

func TestSetCountryChecksTheAccountsOwnProvider(t *testing.T) {
	home := switchHome(t)
	// FI exists only in p2: alice is bound to p1, carol is bound to nothing.
	wantNoNodes(t, runAccountErr(t, home, "set-country", "alice", "fi"), "alice")
	if a := accountByLogin(t, home, "alice"); a.ExitCountry != "DE" {
		t.Errorf("alice = %+v after a refused change", a)
	}
	out := runAccountOK(t, home, "set-country", "carol", "fi")
	if !strings.Contains(string(out), `"account":{"login":"carol","provider":"","exit_country":"FI"`) {
		t.Fatalf("single login must print the account form: %s", out)
	}
	if a := accountByLogin(t, home, "carol"); a.ExitCountry != "FI" {
		t.Errorf("carol = %+v, want FI", a)
	}

	// Together: the refusal names the account that fails and nothing changes.
	wantNoNodes(t, runAccountErr(t, home, "set-country", "carol,alice", "FI"), "alice")
	if a := accountByLogin(t, home, "carol"); a.ExitCountry != "FI" {
		t.Errorf("carol = %+v", a)
	}
	runAccountOK(t, home, "set-country", "alice,bob", "DE")
	runAccountOK(t, home, "set-country", "--all", "DE")
	for _, a := range loadAccounts(t, home) {
		if a.ExitCountry != "DE" {
			t.Errorf("%s = %+v, want country DE", a.Login, a)
		}
	}
}

func TestSetCountryRefusedWhenTheSnapshotIsMissing(t *testing.T) {
	home := switchHome(t)
	if err := os.Remove(filepath.Join(home, "subscriptions", "p1.json")); err != nil {
		t.Fatal(err)
	}
	wantNoNodes(t, runAccountErr(t, home, "set-country", "alice", "NL"), "alice")
	// Repeating what is already set does not ask for nodes.
	runAccountOK(t, home, "set-country", "alice", "DE")
	runAccountOK(t, home, "set-provider", "alice", "p1")
	runAccountOK(t, home, "set-provider", "alice,bob", "p1")
}

func TestSetNoneClearsSeveralAccounts(t *testing.T) {
	home := switchHome(t)
	runAccountOK(t, home, "set-country", "alice,bob", "--none")
	for _, login := range []string{"alice", "bob"} {
		if a := accountByLogin(t, home, login); a.ExitCountry != "" || a.Provider != "p1" {
			t.Errorf("%s = %+v, want p1 without a country", login, a)
		}
	}
	runAccountOK(t, home, "set-provider", "--all", "--none")
	for _, a := range loadAccounts(t, home) {
		if a.Provider != "" || a.ExitCountry != "" {
			t.Errorf("%s = %+v, want no provider and no country", a.Login, a)
		}
	}
}

func TestSetClearingProviderChecksTheCountryAcrossSubscriptions(t *testing.T) {
	home := switchHome(t)
	// bob keeps NL; with no provider any subscription may supply it, p1 does.
	runAccountOK(t, home, "set-provider", "bob", "--none")
	// Without p1's snapshot only p2 is left: it offers DE but not NL.
	if err := os.Remove(filepath.Join(home, "subscriptions", "p1.json")); err != nil {
		t.Fatal(err)
	}
	runAccountOK(t, home, "set-country", "bob", "DE")
	wantNoNodes(t, runAccountErr(t, home, "set-country", "bob", "NL"), "bob")
	if a := accountByLogin(t, home, "bob"); a.ExitCountry != "DE" {
		t.Errorf("bob = %+v after a refused change", a)
	}
}

func TestSetAccountListErrors(t *testing.T) {
	home := switchHome(t)
	ce := runAccountErr(t, home, "set-country", "alice,ghost", "DE")
	if ce.Code != "ACCOUNT_NOT_FOUND" || ce.Exit != cli.ExitNotFound || !strings.Contains(ce.Message, "ghost") {
		t.Errorf("unknown login in a list: %+v", ce)
	}
	if ce := runAccountErr(t, home, "set-country", "alice,bad name", "DE"); ce.Code != "ACCOUNT_INVALID" || ce.Exit != cli.ExitUsage {
		t.Errorf("malformed login in a list: %+v", ce)
	}
	if ce := runAccountErr(t, home, "set-country", "alice,,bob", "DE"); ce.Code != "ACCOUNT_INVALID" {
		t.Errorf("empty login in a list: %+v", ce)
	}
	if ce := runAccountErr(t, home, "set-country", "alice,bob", "nope"); ce.Code != "COUNTRY_INVALID" || ce.Exit != cli.ExitUsage {
		t.Errorf("bad country: %+v", ce)
	}
	if ce := runAccountErr(t, home, "set-provider", "alice,bob", "ghost"); ce.Code != "SUB_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Errorf("unknown subscription: %+v", ce)
	}
	if a := accountByLogin(t, home, "alice"); a.ExitCountry != "DE" || a.Provider != "p1" {
		t.Errorf("a failed command changed alice: %+v", a)
	}

	// A repeated login counts once and keeps the single form only without a comma.
	out := runAccountOK(t, home, "set-country", "alice,alice", "DE")
	if strings.Count(string(out), `"login":`) != 1 || !strings.Contains(string(out), `"accounts":[`) {
		t.Errorf("repeated login: %s", out)
	}
}

func TestSetUsageErrors(t *testing.T) {
	home := switchHome(t)
	for _, args := range [][]string{
		{"set-country", "alice"},
		{"set-country", "--all"},
		{"set-country", "--all", "alice", "DE"},
		{"set-country", "--all", "--none", "DE"},
		{"set-country", "alice", "--none", "DE"},
		{"set-provider", "alice"},
		{"set-provider", "--all"},
		{"set-provider", "--all", "alice", "p1"},
		{"set-provider", "--bogus", "alice", "p1"},
	} {
		if ce := runAccountErr(t, home, args...); ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
			t.Errorf("%v: %+v, want a USAGE error", args, ce)
		}
	}
}

func TestSetAllWithoutAccounts(t *testing.T) {
	home := t.TempDir()
	seedProviders(t, home, map[string][]subscription.Node{"p1": {node("p1-de", "DE")}})
	if ce := runAccountErr(t, home, "set-country", "--all", "DE"); ce.Code != "ACCOUNT_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Errorf("--all without accounts: %+v", ce)
	}
}
