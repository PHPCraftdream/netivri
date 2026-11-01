package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestDoctorInvalidClassifierConfig(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{
		{Name: "one", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Params: map[string]string{"sni": "one.example"}},
		{Name: "two", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "two.example"}},
	}
	doc, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(doc)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("valid shared-port config did not load: %v", err)
	}
	if checks := doctorInvalidClassifierConfig(home); len(checks) != 0 {
		t.Fatalf("valid shared-port config classified invalid: %+v", checks)
	}

	// A single server without SNI remains valid, as does one explicitly
	// marked default when another shared TLS server has no SNI.
	for _, servers := range [][]config.Server{
		{{Name: "solo", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice"}},
		{{Name: "fallback", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Default: true},
			{Name: "named", Protocol: "anytls", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "any.example"}}},
	} {
		validCfg := *cfg
		validCfg.Servers = servers
		valid, err := config.Marshal(&validCfg)
		if err != nil {
			t.Fatal(err)
		}
		write(valid)
		if _, err := config.Load(path); err != nil {
			t.Fatalf("valid config did not load: %v", err)
		}
		if checks := doctorInvalidClassifierConfig(home); len(checks) != 0 {
			t.Fatalf("valid config classified invalid: %+v", checks)
		}
	}

	ambiguousCfg := *cfg
	ambiguousCfg.Servers = []config.Server{
		{Name: "one", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Params: map[string]string{"sni": "one.example"}},
		{Name: "two", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "one.example"}},
	}
	ambiguous, err := config.Marshal(&ambiguousCfg)
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous == doc {
		t.Fatal("ambiguous config unexpectedly rendered identically to valid config")
	}
	write(ambiguous)
	if _, err := config.Load(path); err == nil {
		t.Fatal("ambiguous config unexpectedly loaded")
	} else {
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
			t.Fatalf("ambiguous config load error = %v, want SERVER_PORT_AMBIGUOUS", err)
		}
	}
	checks := doctorInvalidClassifierConfig(home)
	if len(checks) != 1 || checks[0].Name != "servers" || checks[0].Status != doctorFail || checks[0].Detail == "" || !strings.Contains(checks[0].Detail, "ambiguous") || strings.Contains(checks[0].Detail, "12345") || strings.Contains(checks[0].Detail, "one.example") || strings.Contains(checks[0].Detail, "two.example") {
		t.Fatalf("ambiguous config checks = %+v", checks)
	}

	write("servers = this is not a valid config")
	if checks := doctorInvalidClassifierConfig(home); len(checks) != 0 {
		t.Fatalf("unrelated malformed config misclassified: %+v", checks)
	}
}

func TestDoctorMissingSharedMatchKey(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{
		{Name: "no-sni", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Default: true},
		{Name: "named", Protocol: "anytls", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Params: map[string]string{"sni": "any.example"}},
	}
	writeConfig := func(cfg *config.Config) {
		t.Helper()
		doc, err := config.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig(cfg)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("valid default shared-port config did not load: %v", err)
	}

	invalidCfg := *cfg
	invalidCfg.Servers = append([]config.Server(nil), cfg.Servers...)
	invalidCfg.Servers[0].Default = false
	writeConfig(&invalidCfg)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("shared-port config without a match key unexpectedly loaded")
	}
	var ce *cli.Error
	const hint = "set sni on no-sni or mark it as the default"
	if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Hint != hint {
		t.Fatalf("load error = %v, want SERVER_PORT_AMBIGUOUS with hint %q", err, hint)
	}
	checks := doctorInvalidClassifierConfig(home)
	if len(checks) != 1 || checks[0].Name != "servers" || checks[0].Status != doctorFail || !strings.Contains(checks[0].Detail, hint) || strings.Contains(checks[0].Detail, "12345") || strings.Contains(checks[0].Detail, "any.example") {
		t.Fatalf("missing shared match key checks = %+v", checks)
	}
}
