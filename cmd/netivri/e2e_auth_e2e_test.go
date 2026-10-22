//go:build e2e

package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
)

const (
	literalUser = "eve"
	literalPass = "s3cret-pass"
	initPass    = "first-pass"
	bobUser     = "bob"
	bobPass     = "bob-secret-pass"
	carolUser   = "carol"
	carolPass   = "hunter2"
)

func requireKtav(t *testing.T) {
	t.Helper()
}

// Scenario 1: no config file at all.
func TestE2EUpNoConfig(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "none" {
		t.Fatalf("auth = %q, want none", st.Data.Auth)
	}
	host, portStr, err := net.SplitHostPort(st.Data.Listen)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", st.Data.Listen, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("listen host = %q, want 127.0.0.1", host)
	}
	if port, _ := strconv.Atoi(portStr); port <= 0 {
		t.Fatalf("listen port = %q, want > 0", portStr)
	}
	backend := echoServer(t)
	c := socks5NoAuthConnect(t, st.Data.Listen, backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()
	hc, hbr, line := httpConnect(t, st.Data.Listen, backend, "", "")
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("CONNECT status = %q, want HTTP/1.1 200", strings.TrimRight(line, "\r\n"))
	}
	drainHTTPHeaders(t, hbr)
	echoRoundTrip(t, hc, hbr)
	_ = hc.Close()
	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoConfigFile(t, home)
}

// Scenario 2: literal login plus an argon2id hash.
func TestE2EUpLiteralAuth(t *testing.T) {
	requireKtav(t)
	hash, err := auth.Hash(literalPass)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Auth = config.Auth{Login: literalUser, Password: hash}
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "enabled" {
		t.Fatalf("auth = %q, want enabled", st.Data.Auth)
	}
	backend := echoServer(t)

	t.Run("socks5-correct-credentials", func(t *testing.T) {
		c := socks5AuthConnect(t, st.Data.Listen, literalUser, literalPass, backend)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})

	t.Run("socks5-wrong-password", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, literalUser, "wrong-pass"); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})

	// Mihomo answers a no-auth-only offer with method 0x02 and waits for
	// the RFC 1929 subnegotiation; the client cannot proceed.
	t.Run("socks5-noauth-offer-refused", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02 (server demands auth)", m)
		}
	})

	t.Run("http-without-credentials-407", func(t *testing.T) {
		c, _, line := httpConnect(t, st.Data.Listen, backend, "", "")
		defer c.Close()
		if !strings.HasPrefix(line, "HTTP/1.1 407") {
			t.Fatalf("CONNECT status = %q, want HTTP/1.1 407", strings.TrimRight(line, "\r\n"))
		}
	})

	t.Run("http-with-basic-credentials", func(t *testing.T) {
		c, br, line := httpConnect(t, st.Data.Listen, backend, literalUser, literalPass)
		if !strings.HasPrefix(line, "HTTP/1.1 200") {
			t.Fatalf("CONNECT status = %q, want HTTP/1.1 200", strings.TrimRight(line, "\r\n"))
		}
		drainHTTPHeaders(t, br)
		echoRoundTrip(t, c, br)
		_ = c.Close()
	})

	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoSecrets(t, p.Stdout(), p.Stderr(), literalPass, "wrong-pass", hash)
}

// Scenario 3: literal login with password placeholder "init".
func TestE2EUpInitPasswordFlow(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Auth = config.Auth{Login: "alice", Password: config.InitPlaceholder}
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "init-pending" {
		t.Fatalf("auth = %q, want init-pending", st.Data.Auth)
	}
	backend := echoServer(t)
	c := socks5AuthConnect(t, st.Data.Listen, "alice", initPass, backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()

	// Live first-use transition in the SAME process: a different password
	// is rejected and the claimed credential still works (cache path).
	t.Run("live-wrong-password-rejected", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, "alice", "other-pass"); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})
	t.Run("live-claimed-credential-works", func(t *testing.T) {
		c := socks5AuthConnect(t, st.Data.Listen, "alice", initPass, backend)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})

	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoSecrets(t, p.Stdout(), p.Stderr(), initPass, "other-pass")

	raw, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), initPass) {
		t.Fatal("config.ktav contains the plaintext first password")
	}
	cfg := loadConfigFile(t, home)
	if cfg.Auth.Login != "alice" {
		t.Fatalf("persisted login = %q, want alice", cfg.Auth.Login)
	}
	if !strings.HasPrefix(cfg.Auth.Password, "$argon2id$") {
		t.Fatalf("persisted password (length %d) is not an argon2id PHC hash", len(cfg.Auth.Password))
	}
	storedHash := cfg.Auth.Password
	// The claiming process output must not leak the persisted hash either.
	assertNoSecrets(t, p.Stdout(), p.Stderr(), storedHash)

	// A new process on the same home: enabled, credential persisted.
	p2 := startUp(t, home, "--port", "0")
	st2 := p2.Status()
	assertStatusSuccess(t, st2)
	if st2.Data.Auth != "enabled" {
		t.Fatalf("auth after restart = %q, want enabled", st2.Data.Auth)
	}
	backend2 := echoServer(t)

	t.Run("restart-first-password-still-works", func(t *testing.T) {
		c := socks5AuthConnect(t, st2.Data.Listen, "alice", initPass, backend2)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})

	p2.Stop()
	assertOneLine(t, p2.Stdout())
	assertNoSecrets(t, p2.Stdout(), p2.Stderr(), initPass, "other-pass", storedHash)
}

// Scenario 4: both placeholders (login "init", password "init").
func TestE2EUpInitBothPlaceholders(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "init-pending" {
		t.Fatalf("auth = %q, want init-pending", st.Data.Auth)
	}
	backend := echoServer(t)
	c := socks5AuthConnect(t, st.Data.Listen, bobUser, bobPass, backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()

	// Live first-use transition in the SAME process: another user is
	// rejected and the winner still works (cache path).
	t.Run("live-different-user-rejected", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, "mallory", bobPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})
	t.Run("live-winner-still-accepted", func(t *testing.T) {
		c := socks5AuthConnect(t, st.Data.Listen, bobUser, bobPass, backend)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})
	t.Run("live-claimed-login-wrong-password-rejected", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, bobUser, carolPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})

	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoSecrets(t, p.Stdout(), p.Stderr(), bobPass)

	cfg := loadConfigFile(t, home)
	if cfg.Auth.Login != bobUser {
		t.Fatalf("persisted login = %q, want %q", cfg.Auth.Login, bobUser)
	}
	if !strings.HasPrefix(cfg.Auth.Password, "$argon2id$") {
		t.Fatal("persisted password is not an argon2id PHC hash")
	}
	storedHash := cfg.Auth.Password
	// The claiming process output must not leak the persisted hash either.
	assertNoSecrets(t, p.Stdout(), p.Stderr(), storedHash)

	p2 := startUp(t, home, "--port", "0")
	st2 := p2.Status()
	assertStatusSuccess(t, st2)
	if st2.Data.Auth != "enabled" {
		t.Fatalf("auth after restart = %q, want enabled", st2.Data.Auth)
	}
	backend2 := echoServer(t)

	t.Run("restart-winner-still-accepted", func(t *testing.T) {
		c := socks5AuthConnect(t, st2.Data.Listen, bobUser, bobPass, backend2)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})
	t.Run("restart-different-user-rejected", func(t *testing.T) {
		c := dialProxy(t, st2.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, "mallory", bobPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})
	t.Run("restart-different-password-rejected", func(t *testing.T) {
		c := dialProxy(t, st2.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, bobUser, carolPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})

	p2.Stop()
	assertOneLine(t, p2.Stdout())
	assertNoSecrets(t, p2.Stdout(), p2.Stderr(), bobPass, carolPass, storedHash)
}

// Scenario 5: login placeholder with a real hash.
func TestE2EUpInitLoginPlaceholder(t *testing.T) {
	requireKtav(t)
	hash, err := auth.Hash(carolPass)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Auth = config.Auth{Login: config.InitPlaceholder, Password: hash}
	})
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "init-pending" {
		t.Fatalf("auth = %q, want init-pending", st.Data.Auth)
	}
	backend := echoServer(t)

	t.Run("wrong-password-does-not-claim", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, carolUser, "nope"); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
		cfg := loadConfigFile(t, home)
		if cfg.Auth.Login != config.InitPlaceholder {
			t.Fatalf("login claimed by a wrong password: %q", cfg.Auth.Login)
		}
	})

	t.Run("correct-password-claims-login", func(t *testing.T) {
		c := socks5AuthConnect(t, st.Data.Listen, carolUser, carolPass, backend)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})

	// Live claim in the SAME process: the wrong password stays rejected
	// and the claimed credential still works (cache path).
	t.Run("live-wrong-password-still-rejected", func(t *testing.T) {
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, carolUser, "nope"); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})
	t.Run("live-claimed-credential-works", func(t *testing.T) {
		c := socks5AuthConnect(t, st.Data.Listen, carolUser, carolPass, backend)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})
	t.Run("live-different-user-rejected", func(t *testing.T) {
		// The correct password under a different user must not pass once
		// carol claimed the login: the login is compared, not only the hash.
		c := dialProxy(t, st.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, "mallory", carolPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})

	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoSecrets(t, p.Stdout(), p.Stderr(), carolPass, "nope", hash)

	cfg := loadConfigFile(t, home)
	if cfg.Auth.Login != carolUser {
		t.Fatalf("persisted login = %q, want %q", cfg.Auth.Login, carolUser)
	}
	if cfg.Auth.Password != hash {
		t.Fatal("persisted password hash differs from the configured hash")
	}
	assertNoSecrets(t, p.Stdout(), p.Stderr(), cfg.Auth.Password)

	// Restart from the same home: the persisted login keeps working and
	// a different user is still rejected.
	p2 := startUp(t, home, "--port", "0")
	st2 := p2.Status()
	assertStatusSuccess(t, st2)
	if st2.Data.Auth != "enabled" {
		t.Fatalf("auth after restart = %q, want enabled", st2.Data.Auth)
	}
	backend2 := echoServer(t)

	t.Run("restart-claimed-credential-works", func(t *testing.T) {
		c := socks5AuthConnect(t, st2.Data.Listen, carolUser, carolPass, backend2)
		echoRoundTrip(t, c, c)
		_ = c.Close()
	})
	t.Run("restart-different-user-rejected", func(t *testing.T) {
		c := dialProxy(t, st2.Data.Listen)
		defer c.Close()
		if m := socks5Greet(t, c, 0, 2); m != 0x02 {
			t.Fatalf("greeting method = %#x, want 0x02", m)
		}
		if code := socks5AuthReply(t, c, "mallory", carolPass); code != 1 {
			t.Fatalf("auth status = %#x, want 1 (rejected)", code)
		}
	})

	p2.Stop()
	assertOneLine(t, p2.Stdout())
	assertNoSecrets(t, p2.Stdout(), p2.Stderr(), carolPass, "nope", cfg.Auth.Password)
}

// Scenario 6: non-loopback bind without auth is refused; with auth it starts.
func TestE2EUpNonLoopbackBind(t *testing.T) {
	requireKtav(t)
	home := t.TempDir()
	code, stdout, stderr := runNetivri(t, home, "--port", "0", "--bind", "0.0.0.0")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	assertNoSecrets(t, stdout, stderr, literalPass)
	env := decodeErrEnvelope(t, stderr)
	if env.Error.Code != "AUTH_REQUIRED" {
		t.Fatalf("error code = %q, want AUTH_REQUIRED", env.Error.Code)
	}

	// With authentication the same bind starts.
	hash := mustHash(t, literalPass)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Auth = config.Auth{Login: literalUser, Password: hash}
	})
	p := startUp(t, home, "--port", "0", "--bind", "0.0.0.0")
	st := p.Status()
	assertStatusSuccess(t, st)
	if st.Data.Auth != "enabled" {
		t.Fatalf("auth = %q, want enabled", st.Data.Auth)
	}
	_, portStr, err := net.SplitHostPort(st.Data.Listen)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", st.Data.Listen, err)
	}
	if port, _ := strconv.Atoi(portStr); port <= 0 {
		t.Fatalf("listen port = %q, want > 0", portStr)
	}
	loopback := net.JoinHostPort("127.0.0.1", portStr)
	backend := echoServer(t)
	c := socks5AuthConnect(t, loopback, literalUser, literalPass, backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()
	p.Stop()
	assertOneLine(t, p.Stdout())
	assertNoSecrets(t, p.Stdout(), p.Stderr(), literalPass, hash)
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := auth.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

// Scenario 7: port already in use.
func TestE2EUpPortInUse(t *testing.T) {
	requireKtav(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := held.Addr().(*net.TCPAddr).Port
	code, stdout, stderr := runNetivri(t, t.TempDir(), "--port", strconv.Itoa(port))
	if code != 6 {
		t.Fatalf("exit code = %d, want 6 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	env := decodeErrEnvelope(t, stderr)
	if env.Error.Code != "PORT_IN_USE" {
		t.Fatalf("error code = %q, want PORT_IN_USE", env.Error.Code)
	}
}

// Scenario 8: invalid configuration files.
func TestE2EUpInvalidConfig(t *testing.T) {
	requireKtav(t)

	t.Run("garbage-file", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(config.ConfigPath(home), []byte("not ktav :::"), 0o600); err != nil {
			t.Fatalf("write garbage config: %v", err)
		}
		code, stdout, stderr := runNetivri(t, home, "--port", "0")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stdout %q, stderr %q)", code, stdout, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		env := decodeErrEnvelope(t, stderr)
		if env.Error.Code != "CONFIG_INVALID" {
			t.Fatalf("error code = %q, want CONFIG_INVALID", env.Error.Code)
		}
	})

	t.Run("plaintext-password", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, func(cfg *config.Config) {
			cfg.Auth = config.Auth{Login: "eve", Password: "plaintext-pass"}
		})
		code, stdout, stderr := runNetivri(t, home, "--port", "0")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stdout %q, stderr %q)", code, stdout, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		env := decodeErrEnvelope(t, stderr)
		if env.Error.Code != "CONFIG_INVALID" {
			t.Fatalf("error code = %q, want CONFIG_INVALID", env.Error.Code)
		}
		if strings.Contains(stderr, "plaintext-pass") {
			t.Fatal("error envelope leaks the password")
		}
	})
}

// Scenario 10: flags win over the config file port.
func TestE2EUpFlagsVsConfigPort(t *testing.T) {
	requireKtav(t)
	x := freePort(t)
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Listen = config.Listen{Bind: "127.0.0.1", Port: x}
	})

	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	_, portStr, err := net.SplitHostPort(st.Data.Listen)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", st.Data.Listen, err)
	}
	if port, _ := strconv.Atoi(portStr); port == x {
		t.Fatalf("listen port = %d, want a random port different from config port %d", port, x)
	}
	p.Stop()
	assertOneLine(t, p.Stdout())

	p2 := startUp(t, home)
	st2 := p2.Status()
	assertStatusSuccess(t, st2)
	if got := st2.Data.Listen; !strings.HasSuffix(got, ":"+strconv.Itoa(x)) {
		t.Fatalf("listen = %q, want the config port %d", got, x)
	}
	p2.Stop()
	assertOneLine(t, p2.Stdout())
}
