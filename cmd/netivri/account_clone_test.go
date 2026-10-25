package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

const cloneSourcePassword = "source-password"

// cloneHome has alice (provider p1, country DE) with a real password.
func cloneHome(t *testing.T) string {
	t.Helper()
	home := accountProviderHome(t)
	runAccountOK(t, home, "add", "alice", "--provider", "p1", "--country", "DE")
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.Hash(cloneSourcePassword)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Account("alice")
	a.Password = hash
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

type cloneResult struct {
	Account struct {
		Login       string `json:"login"`
		Provider    string `json:"provider"`
		ExitCountry string `json:"exit_country"`
		PasswordSet bool   `json:"password_set"`
	} `json:"account"`
	ClonedFrom string `json:"cloned_from"`
	Password   string `json:"password"`
}

func runClone(t *testing.T, home string, args ...string) (cloneResult, []byte) {
	t.Helper()
	out := runAccountOK(t, home, append([]string{"clone"}, args...)...)
	var env struct {
		Data cloneResult `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return env.Data, out
}

func assertNoPasswordData(t *testing.T, out []byte, secrets ...string) {
	t.Helper()
	for _, s := range append(secrets, "$argon2id$") {
		if bytes.Contains(out, []byte(s)) {
			t.Fatalf("output discloses %q: %s", s, out)
		}
	}
}

func TestAccountCloneCopiesRouteAndPassword(t *testing.T) {
	home := cloneHome(t)
	res, out := runClone(t, home, "alice", "bob")
	if res.Account.Login != "bob" || res.Account.Provider != "p1" || res.Account.ExitCountry != "DE" || !res.Account.PasswordSet {
		t.Fatalf("account = %+v", res.Account)
	}
	if res.ClonedFrom != "alice" || res.Password != "copied" {
		t.Fatalf("result = %+v", res)
	}
	assertNoPasswordData(t, out, cloneSourcePassword)

	alice, bob := loadedAccount(t, home, "alice"), loadedAccount(t, home, "bob")
	if bob.Password != alice.Password || bob.Provider != "p1" || bob.ExitCountry != "DE" {
		t.Fatalf("bob = %+v, alice = %+v", bob, alice)
	}
	if ok, err := auth.VerifyHash(bob.Password, cloneSourcePassword); err != nil || !ok {
		t.Fatalf("the copied password does not verify: %v %v", ok, err)
	}
}

func TestAccountCloneOfAnUnclaimedAccountStaysUnclaimed(t *testing.T) {
	home := accountProviderHome(t)
	runAccountOK(t, home, "add", "alice", "--country", "DE")
	res, _ := runClone(t, home, "alice", "bob")
	if res.Password != "init" || res.Account.PasswordSet {
		t.Fatalf("result = %+v", res)
	}
	if bob := loadedAccount(t, home, "bob"); bob.Password != config.InitPlaceholder || bob.ExitCountry != "DE" {
		t.Fatalf("bob = %+v", bob)
	}
}

func TestAccountCloneWithANewPassword(t *testing.T) {
	home := cloneHome(t)
	res, out := runClone(t, home, "alice", "bob", "--password", "brand-new-password")
	if res.Password != "new" || !res.Account.PasswordSet {
		t.Fatalf("result = %+v", res)
	}
	assertNoPasswordData(t, out, "brand-new-password", cloneSourcePassword)

	alice, bob := loadedAccount(t, home, "alice"), loadedAccount(t, home, "bob")
	if bob.Password == alice.Password {
		t.Fatal("the new password is the source's hash")
	}
	if ok, _ := auth.VerifyHash(bob.Password, "brand-new-password"); !ok {
		t.Fatal("bob's password is not the one given")
	}
	if ok, _ := auth.VerifyHash(bob.Password, cloneSourcePassword); ok {
		t.Fatal("bob still accepts the source's password")
	}
	if ok, _ := auth.VerifyHash(alice.Password, cloneSourcePassword); !ok {
		t.Fatal("the source's password changed")
	}
}

func TestAccountCloneWithInitPassword(t *testing.T) {
	home := cloneHome(t)
	res, _ := runClone(t, home, "alice", "bob", "--password", "init")
	if res.Password != "init" || res.Account.PasswordSet {
		t.Fatalf("result = %+v", res)
	}
	if bob := loadedAccount(t, home, "bob"); bob.Password != config.InitPlaceholder || bob.Provider != "p1" {
		t.Fatalf("bob = %+v", bob)
	}
	if alice := loadedAccount(t, home, "alice"); alice.Password == config.InitPlaceholder {
		t.Fatal("the source lost its password")
	}
}

func TestAccountCloneFlagsAfterTheLogins(t *testing.T) {
	home := cloneHome(t)
	res, _ := runClone(t, home, "alice", "bob", "--password=init", "--country", "nl,de")
	if res.Password != "init" || res.Account.ExitCountry != "NL,DE" {
		t.Fatalf("result = %+v", res)
	}
}

func TestAccountCloneOverridesTheRoute(t *testing.T) {
	home := cloneHome(t)
	res, _ := runClone(t, home, "alice", "bob", "--provider", "p2,p1", "--country", "de")
	if res.Account.Provider != "p2,p1" || res.Account.ExitCountry != "DE" {
		t.Fatalf("account = %+v", res.Account)
	}
	// The password is still the copied one.
	if bob := loadedAccount(t, home, "bob"); bob.Password != loadedAccount(t, home, "alice").Password {
		t.Fatal("overriding the route changed the password")
	}
	// Only the country is replaced: the provider stays.
	res, _ = runClone(t, home, "alice", "carol", "--country", "NL")
	if res.Account.Provider != "p1" || res.Account.ExitCountry != "NL" {
		t.Fatalf("account = %+v", res.Account)
	}
}

func TestAccountCloneRefusesARouteWithoutNodes(t *testing.T) {
	home := cloneHome(t)
	ce := runAccountErr(t, home, "clone", "alice", "bob", "--country", "JP")
	if ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("error = %#v", ce)
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("bob"); a != nil {
		t.Fatalf("bob was created despite the refusal: %+v", a)
	}
}

func TestAccountCloneErrors(t *testing.T) {
	home := cloneHome(t)
	runAccountOK(t, home, "add", "bob", "--country", "NL")
	for _, tc := range []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"unknown source", []string{"ghost", "carol"}, "ACCOUNT_NOT_FOUND", cli.ExitNotFound},
		{"new login taken", []string{"alice", "bob"}, "ACCOUNT_EXISTS", cli.ExitConflict},
		{"clone onto itself", []string{"alice", "alice"}, "ACCOUNT_EXISTS", cli.ExitConflict},
		{"bad new login", []string{"alice", "no spaces"}, "ACCOUNT_INVALID", cli.ExitUsage},
		{"bad source login", []string{"a/b", "carol"}, "ACCOUNT_INVALID", cli.ExitUsage},
		{"the init placeholder as a login", []string{"alice", "init"}, "ACCOUNT_INVALID", cli.ExitUsage},
		{"no logins", nil, "USAGE", cli.ExitUsage},
		{"one login", []string{"alice"}, "USAGE", cli.ExitUsage},
		{"three logins", []string{"alice", "carol", "dave"}, "USAGE", cli.ExitUsage},
		{"unknown flag", []string{"alice", "carol", "--nope"}, "USAGE", cli.ExitUsage},
		{"empty password", []string{"alice", "carol", "--password", ""}, "USAGE", cli.ExitUsage},
		{"password too long", []string{"alice", "carol", "--password", strings.Repeat("x", auth.MaxPasswordLen+1)}, "USAGE", cli.ExitUsage},
		{"bad country", []string{"alice", "carol", "--country", "DE,"}, "COUNTRY_INVALID", cli.ExitUsage},
		{"unknown provider", []string{"alice", "carol", "--provider", "ghost"}, "SUB_NOT_FOUND", cli.ExitNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ce := runAccountErr(t, home, append([]string{"clone"}, tc.args...)...)
			if ce.Code != tc.code || ce.Exit != tc.exit {
				t.Fatalf("error = %#v, want %s exit %d", ce, tc.code, tc.exit)
			}
		})
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("a failed clone left accounts behind: %+v", cfg.Accounts)
	}
}

func TestAccountCloneMaxLengthPasswordWorks(t *testing.T) {
	home := cloneHome(t)
	pw := strings.Repeat("p", auth.MaxPasswordLen)
	runClone(t, home, "alice", "bob", "--password", pw)
	if ok, _ := auth.VerifyHash(loadedAccount(t, home, "bob").Password, pw); !ok {
		t.Fatal("a password of the maximum length does not verify")
	}
}

func TestAccountCloneIsListed(t *testing.T) {
	home := cloneHome(t)
	runClone(t, home, "alice", "bob")
	out := runAccountOK(t, home, "list")
	if !strings.Contains(string(out), `"login":"bob","provider":"p1","exit_country":"DE","password_set":true`) {
		t.Fatalf("list = %s", out)
	}
}
