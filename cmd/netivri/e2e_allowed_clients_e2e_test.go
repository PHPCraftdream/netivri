//go:build e2e

package main

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// allowedClientsDial dials the proxy and bounds every later IO on the
// connection to ~5s, so a daemon that never answers fails fast instead of
// hanging for the full e2eIOTimeout.
func allowedClientsDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c := dialProxy(t, addr)
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	return c
}

// Scenario 1: a global rule that excludes loopback refuses the client in the
// accept loop, before any handshake: the greeting is answered with EOF and
// zero bytes.
func TestE2EAllowedClientsGlobalRefusesLoopback(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Listen.AllowedClients = "10.0.0.0/8"
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)

	c := allowedClientsDial(t, st.Data.Listen)
	buf := []byte{5, 1, 0} // SOCKS5 greeting, one method: no-auth
	if _, err := c.Write(buf); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	got := make([]byte, 2)
	n, err := io.ReadFull(c, got)
	if n != 0 {
		t.Fatalf("refused client read %d reply byte(s) before the error, want 0", n)
	}
	if err == nil {
		t.Fatal("refused client got a method-selection reply, want EOF with zero bytes")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("the daemon did not close the connection promptly: %v", err)
	}
	if lower := strings.ToLower(err.Error()); !errors.Is(err, io.EOF) && !strings.Contains(lower, "reset") && !strings.Contains(lower, "forcibly closed") {
		t.Fatalf("refused client error = %v, want EOF or a connection reset", err)
	}
	_ = c.Close()
	p.Stop()
	assertOneLine(t, p.Stdout())
}

// Scenario 2: the global rule `loopback` allows a loopback SOCKS5 client, and
// `netivri status` reports the rule as allowed_clients.
func TestE2EAllowedClientsGlobalAllowsLoopback(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Listen.AllowedClients = "loopback"
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)

	backend := echoServer(t)
	c := socks5NoAuthConnect(t, st.Data.Listen, backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()

	code, stdout, stderr := daemonRun(t, home, "status", "--json")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	data := daemonData(t, stdout)
	if data["allowed_clients"] != "loopback" {
		t.Fatalf("status allowed_clients = %v, want loopback", data["allowed_clients"])
	}

	p.Stop()
	assertOneLine(t, p.Stdout())
}

// Scenario 3: the per-account rule is enforced before the password is
// verified, so an `init` account cannot be claimed from a disallowed address.
func TestE2EAllowedClientsAccountInitClaimGuarantee(t *testing.T) {
	requireKtav(t)
	const chosenPass = "alice-chose-this-pass"
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{
			Login:          "alice",
			Password:       config.InitPlaceholder,
			AllowedClients: "10.0.0.0/8",
		}}
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)

	// A loopback client with ANY password must fail like a wrong password.
	c := allowedClientsDial(t, st.Data.Listen)
	if m := socks5Greet(t, c, 0, 2); m != 0x02 {
		t.Fatalf("greeting method = %#x, want 0x02 (an account requires auth)", m)
	}
	if code := socks5AuthReply(t, c, "alice", chosenPass); code != 1 {
		t.Fatalf("auth status = %#x, want 1 (rejected before password verification)", code)
	}
	_ = c.Close()

	cfg := loadConfigFile(t, home)
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Login != "alice" {
		t.Fatalf("accounts = %+v, want only alice", cfg.Accounts)
	}
	if cfg.Accounts[0].Password != config.InitPlaceholder {
		t.Fatal("the init placeholder was claimed from a disallowed address")
	}

	p.Stop()
	assertOneLine(t, p.Stdout())

	// Allow loopback: the same login claims the placeholder with a chosen
	// password and the proxy works.
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{
			Login:          "alice",
			Password:       config.InitPlaceholder,
			AllowedClients: "loopback",
		}}
	})
	p2 := startUp(t, home, "--port", "0")
	st2 := p2.Status()
	assertStatusSuccess(t, st2)
	backend := echoServer(t)
	c2 := socks5AuthConnect(t, st2.Data.Listen, "alice", chosenPass, backend)
	echoRoundTrip(t, c2, c2)
	_ = c2.Close()

	p2.Stop()
	assertOneLine(t, p2.Stdout())
	assertNoSecrets(t, p2.Stdout(), p2.Stderr(), chosenPass)

	cfg2 := loadConfigFile(t, home)
	if !strings.HasPrefix(cfg2.Accounts[0].Password, "$argon2id$") {
		t.Fatalf("persisted password (length %d) is not an argon2id PHC hash", len(cfg2.Accounts[0].Password))
	}
	assertNoSecrets(t, p2.Stdout(), p2.Stderr(), cfg2.Accounts[0].Password)
}
