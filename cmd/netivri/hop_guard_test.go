package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

const globalPostFallbackMessage = "the global chain with several post hops is not supported for connections without an account yet; set the list on the accounts or provider instead"

func globalPostPoolConfig() *config.Config {
	cfg := config.Default()
	cfg.Chain.PostSOCKS5 = config.HopList{"socks5://first.example:1080", "http://second.example:8080"}
	return cfg
}

func TestGlobalPostFallbackGuardCases(t *testing.T) {
	fixedAuth := globalPostPoolConfig()
	fixedAuth.Auth = config.Auth{Login: "legacy", Password: "$argon2id$fixture"}
	fixedAuth.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	initAuth := globalPostPoolConfig()
	initAuth.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	initAuth.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	accountInherits := globalPostPoolConfig()
	accountInherits.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"}}
	accountNone := globalPostPoolConfig()
	accountNone.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE", PostSOCKS5: config.HopList{"none"}}}
	directNone := globalPostPoolConfig()
	directNone.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct", PostSOCKS5: config.HopList{"none"}}}
	directInherits := globalPostPoolConfig()
	directInherits.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct"}}
	globalPreOnly := config.Default()
	globalPreOnly.Chain.PreSOCKS5 = config.HopList{"socks5://pre-one.example:1080", "http://pre-two.example:8080"}

	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{name: "no accounts", cfg: globalPostPoolConfig(), wantErr: true},
		{name: "active fixed legacy auth", cfg: fixedAuth, wantErr: true},
		{name: "active init legacy auth", cfg: initAuth, wantErr: true},
		{name: "named account inherits pool", cfg: accountInherits},
		{name: "named account none", cfg: accountNone},
		{name: "direct account inherits pool", cfg: directInherits},
		{name: "direct account none", cfg: directNone},
		{name: "multi pre alone", cfg: globalPreOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateGlobalPostFallback(tc.cfg)
			if tc.wantErr {
				assertGlobalPostGuardMessage(t, err)
				if validationErr := tc.cfg.Validate(); validationErr == nil {
					t.Fatal("whole config accepted global post pool with reachable MATCH route")
				} else {
					assertGlobalPostGuardMessage(t, validationErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("global fallback guard rejected case: %v", err)
			}
			if err := tc.cfg.Validate(); err != nil {
				t.Fatalf("whole config rejected case: %v", err)
			}
		})
	}
	if err := config.ValidateGlobalPostFallback(nil); err != nil {
		t.Fatalf("nil config: %v", err)
	}
}

func TestGlobalPostFallbackGuardConfigLoadAndDoctorUseSameMessage(t *testing.T) {
	cfg := globalPostPoolConfig()
	path := filepath.Join(t.TempDir(), "config.ktav")
	// Save serializes the candidate but does not validate the full config;
	// Load and the doctor config check must both apply the shared guard.
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("write invalid guard fixture: %v", err)
	}
	_, loadErr := config.Load(path)
	assertGlobalPostGuardMessage(t, loadErr)

	doctor := &doctorContext{home: filepath.Dir(path)}
	check := doctor.checkConfig()
	if check.Status != doctorFail || check.Detail != globalPostFallbackMessage {
		t.Fatalf("doctor config check = %+v, want failure with exact guard message", check)
	}
}

func TestGlobalPostFallbackGuardConfigSetDoesNotRewriteInvalidConfig(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	if err := config.Save(path, globalPostPoolConfig()); err != nil {
		t.Fatalf("write invalid guard fixture: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, setErr := ConfigSet(home, "listen.port", "7891")
	assertGlobalPostGuardMessage(t, setErr)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("config set rewrote the invalid configuration")
	}
}

func TestGlobalPostFallbackLegacyAuthClaimIsAtomic(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := globalPostPoolConfig()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid account-only pool fixture rejected: %v", err)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save account-only pool fixture: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	claimErr := newPersist(path, cfg)("legacy", "$argon2id$claimed")
	assertGlobalPostGuardMessage(t, claimErr)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("legacy auth claim saved a configuration rejected by the global post guard")
	}
	if cfg.Auth.Login != "" || cfg.Auth.Password != "" {
		t.Fatal("failed legacy auth claim mutated the in-memory config")
	}
}

func TestGlobalPostFallbackAccountRemovalIsAtomic(t *testing.T) {
	home := t.TempDir()
	cfg := globalPostPoolConfig()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid account-only pool fixture rejected: %v", err)
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save account-only pool fixture: %v", err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	removeErr := AccountRemove(home, "alice")
	assertGlobalPostGuardMessage(t, removeErr)
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("account removal saved an invalid no-account candidate")
	}
	loaded, err := config.Load(config.ConfigPath(home))
	if err != nil || len(loaded.Accounts) != 1 || loaded.Accounts[0].Login != "alice" {
		t.Fatalf("account removal changed the stored account candidate: accounts=%v err=%v", loaded, err)
	}
}

func assertGlobalPostGuardMessage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected global post fallback guard error")
	}
	var cliErr *cli.Error
	if errors.As(err, &cliErr) {
		if cliErr.Code != config.CodeConfigInvalid || cliErr.Exit != cli.ExitUsage {
			t.Fatalf("error code/exit = %s/%d, want %s/%d", cliErr.Code, cliErr.Exit, config.CodeConfigInvalid, cli.ExitUsage)
		}
		if cliErr.Message != globalPostFallbackMessage {
			t.Fatalf("CLI message = %q, want exact guard message", cliErr.Message)
		}
		return
	}
	if err.Error() != globalPostFallbackMessage {
		t.Fatalf("error = %q, want exact guard message %q", err.Error(), globalPostFallbackMessage)
	}
}
