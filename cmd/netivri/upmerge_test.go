package main

import (
	"os"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func TestMergeUpFlagsVsConfig(t *testing.T) {
	cfg := config.Default() // 127.0.0.1:7890
	cfg.Listen = config.Listen{Bind: "127.0.0.2", Port: 8080}
	cases := []struct {
		name     string
		opts     upOptions
		cfg      *config.Config
		wantBind string
		wantPort int
	}{
		{"no flags, no config values", upOptions{}, config.Default(), "127.0.0.1", 7890},
		{"config only", upOptions{}, cfg, "127.0.0.2", 8080},
		{"flag port only", upOptions{Port: 9999, PortSet: true}, cfg, "127.0.0.2", 9999},
		{"flag bind only", upOptions{Bind: "::1", BindSet: true}, cfg, "::1", 8080},
		{"both flags", upOptions{Port: 1, PortSet: true, Bind: "localhost", BindSet: true}, cfg, "localhost", 1},
		{"flag port zero is explicit random", upOptions{Port: 0, PortSet: true}, cfg, "127.0.0.2", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bind, port, err := mergeUp(tc.opts, tc.cfg)
			if err != nil {
				t.Fatalf("mergeUp(%+v): %v", tc.opts, err)
			}
			if bind != tc.wantBind || port != tc.wantPort {
				t.Errorf("mergeUp = %s:%d, want %s:%d", bind, port, tc.wantBind, tc.wantPort)
			}
		})
	}
}

func TestMergeUpBindValidation(t *testing.T) {
	withAuth := config.Default()
	withAuth.Listen = config.Listen{Bind: "127.0.0.1", Port: 7890}
	withAuth.Auth = config.Auth{Login: "alice", Password: "$argon2id$hash"}
	cases := []struct {
		name     string
		opts     upOptions
		cfg      *config.Config
		wantErr  string // "" = no error
		wantCode string
	}{
		{"bad bind without flag", upOptions{}, func() *config.Config { c := config.Default(); c.Listen.Bind = "example.com"; return c }(), "yes", "USAGE"},
		{"bad bind with flag", upOptions{Bind: "example.com", BindSet: true}, config.Default(), "yes", "USAGE"},
		{"non-loopback without auth", upOptions{Bind: "0.0.0.0", BindSet: true}, config.Default(), "yes", "AUTH_REQUIRED"},
		{"non-loopback config without auth", upOptions{}, func() *config.Config { c := config.Default(); c.Listen.Bind = "192.168.1.5"; return c }(), "yes", "AUTH_REQUIRED"},
		{"non-loopback with auth enabled", upOptions{Bind: "0.0.0.0", BindSet: true}, withAuth, "", ""},
		{"non-loopback with init-pending", upOptions{Bind: "192.168.1.5", BindSet: true}, func() *config.Config {
			c := config.Default()
			c.Listen.Bind = "192.168.1.5"
			c.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
			return c
		}(), "yes", "AUTH_REQUIRED"},
		{"non-loopback init-pending with allow_init_remote", upOptions{Bind: "192.168.1.5", BindSet: true}, func() *config.Config {
			c := config.Default()
			c.Listen.Bind = "192.168.1.5"
			c.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder, AllowInitRemote: true}
			return c
		}(), "", ""},
		{"loopback always fine", upOptions{}, config.Default(), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := mergeUp(tc.opts, tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("mergeUp(%+v): unexpected error %v", tc.opts, err)
				}
				return
			}
			ce, ok := err.(*cli.Error)
			if !ok {
				t.Fatalf("want *cli.Error, got %T (%v)", err, err)
			}
			if ce.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ce.Code, tc.wantCode)
			}
			if tc.wantCode == "USAGE" && ce.Exit != cli.ExitUsage {
				t.Errorf("exit = %d, want %d", ce.Exit, cli.ExitUsage)
			}
			if tc.wantCode == "AUTH_REQUIRED" && !strings.Contains(ce.Hint, "auth.login") {
				t.Errorf("hint = %q, want mention of auth.login", ce.Hint)
			}
		})
	}
}

func TestAuthStatus(t *testing.T) {
	const hash = "$argon2id$v=19$m=8,t=2,p=1" // shape only; not a real hash
	cases := []struct {
		login, password, want string
	}{
		{"", "", "none"},
		{"alice", hash, "enabled"},
		{config.InitPlaceholder, hash, "init-pending"},
		{"alice", config.InitPlaceholder, "init-pending"},
		{config.InitPlaceholder, config.InitPlaceholder, "init-pending"},
	}
	for _, tc := range cases {
		if got := authStatus(tc.login, tc.password); got != tc.want {
			t.Errorf("authStatus(%q, %q) = %q, want %q", tc.login, tc.password, got, tc.want)
		}
	}
}

func TestPersistClosure(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := config.Default()
	cfg.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}

	hash, err := auth.Hash("correct horse")
	if err != nil {
		t.Fatalf("auth.Hash: %v", err)
	}
	persist := newPersist(path, cfg)
	if err := persist("alice", hash); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if cfg.Auth.Login != "alice" || cfg.Auth.Password != hash {
		t.Errorf("in-memory config = %q/%q..., want updated login and hash", cfg.Auth.Login, cfg.Auth.Password[:16])
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load after persist: %v", err)
	}
	if reloaded.Auth.Login != "alice" {
		t.Errorf("reloaded login = %q, want %q", reloaded.Auth.Login, "alice")
	}
	if !strings.HasPrefix(reloaded.Auth.Password, "$argon2id$") {
		t.Errorf("reloaded password does not look like an argon2id hash: %q", reloaded.Auth.Password[:16])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(data), "correct horse") {
		t.Error("saved config file contains the plaintext password")
	}
	if !strings.Contains(string(data), "$argon2id$") {
		t.Error("saved config file has no argon2id hash")
	}
}
