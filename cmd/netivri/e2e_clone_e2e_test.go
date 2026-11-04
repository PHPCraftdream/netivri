//go:build e2e

package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// e2eAuthStatus returns the SOCKS5 username/password status of a login (0 =
// accepted) without going on to CONNECT.
func e2eAuthStatus(t *testing.T, proxy, user, pass string) byte {
	t.Helper()
	c := dialProxy(t, proxy)
	defer c.Close()
	if m := socks5Greet(t, c, 0, 2); m != 0x02 {
		t.Fatalf("greeting method = %#x, want 0x02", m)
	}
	return socks5AuthReply(t, c, user, pass)
}

// TestE2EAccountClone clones an account three ways and signs in to a real
// angel with each: the copied password, a new one, and the init placeholder
// whose first password sticks.
func TestE2EAccountClone(t *testing.T) {
	home := t.TempDir()
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash}}
	})

	clone := func(args ...string) string {
		t.Helper()
		code, out, stderr := accountsCommand(t, home, append([]string{"clone", "alice"}, args...)...)
		if code != 0 {
			t.Fatalf("account clone %v: exit %d (%s%s)", args, code, out, stderr)
		}
		assertNoSecrets(t, out, stderr, accountsDEPass, "carol-own-password", hash)
		return out
	}
	if out := clone("bob"); !strings.Contains(out, `"password":"copied"`) {
		t.Fatalf("bob: %s", out)
	}
	if out := clone("carol", "--password", "carol-own-password"); !strings.Contains(out, `"password":"new"`) {
		t.Fatalf("carol: %s", out)
	}
	if out := clone("dave", "--password", "init"); !strings.Contains(out, `"password":"init"`) {
		t.Fatalf("dave: %s", out)
	}

	code, out, stderr := accountsCommand(t, home, "clone", "alice", "bob")
	if code == 0 || !strings.Contains(stderr, "ACCOUNT_EXISTS") {
		t.Fatalf("cloning onto an existing login: exit %d (%s%s)", code, out, stderr)
	}

	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	listen := p.Status().Data.Listen

	// The copy opens with the source's password.
	bob := socks5AuthConnect(t, listen, "bob", accountsDEPass, backend)
	echoRoundTrip(t, bob, bob)
	_ = bob.Close()
	if st := e2eAuthStatus(t, listen, "bob", "carol-own-password"); st == 0 {
		t.Error("bob accepts a password that is not his")
	}

	// The account with its own password refuses the source's.
	if st := e2eAuthStatus(t, listen, "carol", accountsDEPass); st == 0 {
		t.Error("carol accepts the source's password")
	}
	carol := socks5AuthConnect(t, listen, "carol", "carol-own-password", backend)
	echoRoundTrip(t, carol, carol)
	_ = carol.Close()

	// The init account takes the first password and keeps it.
	dave := socks5AuthConnect(t, listen, "dave", "dave-first-password", backend)
	echoRoundTrip(t, dave, dave)
	_ = dave.Close()
	if st := e2eAuthStatus(t, listen, "dave", "another-password"); st == 0 {
		t.Error("dave accepts a second, different password")
	}
	if st := e2eAuthStatus(t, listen, "dave", "dave-first-password"); st != 0 {
		t.Errorf("dave refuses the password that claimed the account: status %#x", st)
	}

	// The source is untouched.
	alice := socks5AuthConnect(t, listen, "alice", accountsDEPass, backend)
	echoRoundTrip(t, alice, alice)
	_ = alice.Close()
}
