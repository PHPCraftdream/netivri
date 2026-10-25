package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// clientsHome is a home with the two providers of switchHome and no accounts.
func clientsHome(t *testing.T) string {
	t.Helper()
	home := switchHome(t)
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = nil
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

func listAccountsJSON(t *testing.T, home string) []map[string]any {
	t.Helper()
	var doc struct {
		Data struct {
			Accounts []map[string]any `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(runAccountOK(t, home, "list"), &doc); err != nil {
		t.Fatalf("decode account list: %v", err)
	}
	return doc.Data.Accounts
}

func accountClients(t *testing.T, home, login string) config.Account {
	t.Helper()
	return accountByLogin(t, home, login)
}

func TestAccountAddStoresClients(t *testing.T) {
	home := switchHome(t)
	runAccountOK(t, home, "add", "dave", "--provider", "p1", "--clients", "10.0.0.0/8")
	if a := accountClients(t, home, "dave"); a.AllowedClients != "10.0.0.0/8" {
		t.Fatalf("dave = %+v, want clients 10.0.0.0/8", a)
	}
	list := listAccountsJSON(t, home)
	found := false
	for _, a := range list {
		if a["login"] != "dave" {
			continue
		}
		found = true
		if a["allowed_clients"] != "10.0.0.0/8" {
			t.Fatalf("list shows dave with allowed_clients %v", a["allowed_clients"])
		}
	}
	if !found {
		t.Fatalf("list does not contain dave: %v", list)
	}
}

func TestAccountAddRejectsBadClients(t *testing.T) {
	home := switchHome(t)
	ce := runAccountErr(t, home, "add", "dave", "--clients", "19*")
	if ce.Code != "CONFIG_INVALID" || ce.Exit != cli.ExitUsage {
		t.Fatalf("add with a bad rule = %+v, want CONFIG_INVALID (exit 2)", ce)
	}
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cfg.Accounts {
		if a.Login == "dave" {
			t.Fatalf("a refused add still wrote the account: %+v", cfg.Accounts)
		}
	}
}

func TestSetClientsSetsAndClears(t *testing.T) {
	home := switchHome(t)

	runAccountOK(t, home, "set-clients", "alice", "loopback")
	if a := accountClients(t, home, "alice"); a.AllowedClients != "loopback" {
		t.Fatalf("alice = %+v, want loopback", a)
	}

	runAccountOK(t, home, "set-clients", "alice,bob", "10.0.0.0/8")
	for _, login := range []string{"alice", "bob"} {
		if a := accountClients(t, home, login); a.AllowedClients != "10.0.0.0/8" {
			t.Fatalf("%s = %+v, want 10.0.0.0/8", login, a)
		}
	}

	runAccountOK(t, home, "set-clients", "--all", "192.168.1.*")
	for _, a := range loadAccounts(t, home) {
		if a.AllowedClients != "192.168.1.*" {
			t.Fatalf("%s = %+v, want 192.168.1.*", a.Login, a)
		}
	}

	runAccountOK(t, home, "set-clients", "alice", "--none")
	if a := accountClients(t, home, "alice"); a.AllowedClients != "" {
		t.Fatalf("alice = %+v, want the rule cleared", a)
	}
}

func TestSetClientsErrorsChangeNothing(t *testing.T) {
	home := switchHome(t)
	runAccountOK(t, home, "set-clients", "alice", "10.0.0.0/8")

	ce := runAccountErr(t, home, "set-clients", "nosuch", "loopback")
	if ce.Code != "ACCOUNT_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unknown login = %+v, want ACCOUNT_NOT_FOUND (exit 5)", ce)
	}

	before, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if ce := runAccountErr(t, home, "set-clients", "alice", "127.0.0.1,,10.0.0.0/8"); ce.Code != "CONFIG_INVALID" {
		t.Fatalf("bad rule = %+v, want CONFIG_INVALID (exit 2)", ce)
	}
	after, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Accounts) != len(after.Accounts) {
		t.Fatal("a refused set-clients rewrote the accounts")
	}
	for i := range before.Accounts {
		if before.Accounts[i] != after.Accounts[i] {
			t.Fatalf("a refused set-clients changed %s", after.Accounts[i].Login)
		}
	}
}

func TestCloneCopiesAndReplacesClients(t *testing.T) {
	home := switchHome(t)
	runAccountOK(t, home, "set-clients", "alice", "10.0.0.0/8")

	// Clone into new logins: an existing login is refused (ACCOUNT_EXISTS).
	out := runAccountOK(t, home, "clone", "alice", "dave")
	if !strings.Contains(string(out), `"allowed_clients":"10.0.0.0/8"`) {
		t.Fatalf("clone output %s", out)
	}
	if a := accountClients(t, home, "dave"); a.AllowedClients != "10.0.0.0/8" {
		t.Fatalf("dave = %+v, want the copied rule", a)
	}

	runAccountOK(t, home, "clone", "alice", "erin", "--clients", "loopback")
	if a := accountClients(t, home, "erin"); a.AllowedClients != "loopback" {
		t.Fatalf("erin = %+v, want loopback", a)
	}
	if a := accountClients(t, home, "alice"); a.AllowedClients != "10.0.0.0/8" {
		t.Fatalf("alice changed: %+v", a)
	}

	// Cloning onto an existing login is refused and leaves it untouched.
	ce := runAccountErr(t, home, "clone", "alice", "bob")
	if ce.Code != "ACCOUNT_EXISTS" {
		t.Fatalf("clone onto bob = %+v, want ACCOUNT_EXISTS", ce)
	}
	if a := accountClients(t, home, "bob"); a.AllowedClients != "" {
		t.Fatalf("bob = %+v, want his rule untouched", a)
	}
}

func TestFingerprintIncludesAllowedClients(t *testing.T) {
	base := fingerprint(testDesired())

	d := testDesired()
	d.cfg.Accounts[0].AllowedClients = "10.0.0.0/8"
	if got := fingerprint(d); got == base {
		t.Error("a changed account rule did not change the fingerprint")
	}

	d = testDesired()
	d.cfg.Listen.AllowedClients = "10.0.0.0/8"
	if got := fingerprint(d); got == base {
		t.Error("a changed listen rule did not change the fingerprint")
	}
}

// accountStatus passes the stored rule through: "" when the account has none.
func TestAccountStatusAllowedClientsPassthrough(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Login: "alice", AllowedClients: "10.0.0.0/8"},
		{Login: "bob"},
	}
	status := accountStatus(cfg, nil)
	if len(status) != 2 {
		t.Fatalf("len(status) = %d, want 2", len(status))
	}
	if status[0].Login != "alice" || status[1].Login != "bob" {
		t.Fatalf("status logins = %q, %q; want alice first, bob second (sorted)", status[0].Login, status[1].Login)
	}
	for _, a := range status {
		want := ""
		if a.Login == "alice" {
			want = "10.0.0.0/8"
		}
		if a.AllowedClients != want {
			t.Errorf("status %s allowed_clients = %q, want %q", a.Login, a.AllowedClients, want)
		}
	}
}

func TestAllowedClientsLabel(t *testing.T) {
	if got := allowedClientsLabel(""); got != "*" {
		t.Errorf("allowedClientsLabel(\"\") = %q, want *", got)
	}
	if got := allowedClientsLabel("10.0.0.0/8"); got != "10.0.0.0/8" {
		t.Errorf("allowedClientsLabel passthrough = %q", got)
	}
}
