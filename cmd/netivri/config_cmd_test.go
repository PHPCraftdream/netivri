package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// configDispatch runs dispatch and returns the raw stdout. Dispatch prints
// JSON here because cli.UseKtav is only wired up in main().
func configDispatch(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := dispatch(args, &out)
	return out.String(), err
}

// configData dispatches args, requires success and returns data as a decoded
// JSON object (numbers as float64, booleans as bool).
func configData(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, err := configDispatch(t, args...)
	if err != nil {
		t.Fatalf("dispatch(%q) = %v, want nil", args, err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("dispatch(%q) not one JSON object: %v\n%s", args, err, out)
	}
	if env["ok"] != true {
		t.Fatalf("dispatch(%q) ok = %v", args, env["ok"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch(%q) has no data object: %v", args, env)
	}
	return data
}

// configGroups decodes the data.groups payload of a `config get` run.
func configGroups(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	data := configData(t, args...)
	raw, ok := data["groups"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch(%q) data.groups = %v", args, data["groups"])
	}
	groups := map[string]map[string]any{}
	for name, g := range raw {
		m, ok := g.(map[string]any)
		if !ok {
			t.Fatalf("dispatch(%q) group %q = %v", args, name, g)
		}
		groups[name] = m
	}
	return groups
}

// wantConfigUsage asserts a *cli.Error usage failure with empty stdout.
func wantConfigUsage(t *testing.T, args []string) *cli.Error {
	t.Helper()
	out, err := configDispatch(t, args...)
	if err == nil {
		t.Fatalf("dispatch(%q) succeeded, want usage error", args)
	}
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("dispatch(%q) = %T (%v), want *cli.Error", args, err, err)
	}
	if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
		t.Errorf("dispatch(%q) code/exit = %q/%d, want USAGE/%d", args, ce.Code, ce.Exit, cli.ExitUsage)
	}
	if out != "" {
		t.Errorf("dispatch(%q) wrote to stdout on failure: %q", args, out)
	}
	return ce
}

func readHomeConfig(t *testing.T, home string) ([]byte, *config.Config) {
	t.Helper()
	raw, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("read %s: %v", config.ConfigPath(home), err)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatalf("loadConfig(%s): %v", home, err)
	}
	return raw, cfg
}

func TestConfigGetDefaultsOnEmptyHome(t *testing.T) {
	home := t.TempDir()
	groups := configGroups(t, "config", "get", "--home", home)
	listen := groups["listen"]
	if listen["bind"] != "127.0.0.1" {
		t.Errorf("listen.bind = %v, want 127.0.0.1", listen["bind"])
	}
	if listen["port"] != float64(7890) {
		t.Errorf("listen.port = %v, want 7890", listen["port"])
	}
	if groups["auth"]["password"] != "not set" {
		t.Errorf("auth.password = %v, want \"not set\"", groups["auth"]["password"])
	}
	proxy, ok := groups["fetch"]["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("fetch.proxy = %v, want an object", groups["fetch"]["proxy"])
	}
	if proxy["value"] != "" || proxy["auth"] != false {
		t.Errorf("fetch.proxy = %v, want {value:\"\", auth:false}", proxy)
	}
}

func TestConfigGetSingleKeyFiltersToItsGroup(t *testing.T) {
	home := t.TempDir()
	groups := configGroups(t, "config", "get", "listen.port", "--home", home)
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want only the listen group", groups)
	}
	if _, ok := groups["listen"]["port"]; !ok {
		t.Errorf("listen group lacks port: %v", groups["listen"])
	}
}

func TestConfigUsageErrors(t *testing.T) {
	home := t.TempDir()
	cases := [][]string{
		{"config", "get", "nope", "--home", home},
		{"config", "get", "a", "b"},
		{"config"},
		{"config", "frobnicate"},
		{"config", "set", "x"},
		{"config", "set", "x", "y", "z"},
	}
	for _, args := range cases {
		ce := wantConfigUsage(t, args)
		if len(args) > 2 && args[2] == "nope" {
			if ce.Hint == "" {
				t.Errorf("dispatch(%q) hint is empty, want the key list", args)
			}
			if !strings.Contains(ce.Hint, "listen.port") {
				t.Errorf("dispatch(%q) hint %q does not list valid keys", args, ce.Hint)
			}
		}
	}
}

func TestConfigSetPortPersistsAndNeedsRestart(t *testing.T) {
	home := t.TempDir()
	data := configData(t, "config", "set", "listen.port", "8080", "--home", home)
	if data["key"] != "listen.port" {
		t.Errorf("data.key = %v, want listen.port", data["key"])
	}
	if data["changed"] != true {
		t.Errorf("data.changed = %v, want true", data["changed"])
	}
	if data["restart_needed"] != true {
		t.Errorf("data.restart_needed = %v, want true", data["restart_needed"])
	}
	_, cfg := readHomeConfig(t, home)
	if cfg.Listen.Port != 8080 {
		t.Errorf("reloaded listen.port = %d, want 8080", cfg.Listen.Port)
	}
}

func TestConfigSetInvalidPortWritesNothing(t *testing.T) {
	home := t.TempDir()
	if err := config.Save(config.ConfigPath(home), config.Default()); err != nil {
		t.Fatalf("save defaults: %v", err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	out, err := configDispatch(t, "config", "set", "listen.port", "70000", "--home", home)
	if err == nil {
		t.Fatalf("set listen.port 70000 succeeded, want CONFIG_INVALID")
	}
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("error = %T (%v), want *cli.Error", err, err)
	}
	if ce.Code != "CONFIG_INVALID" || ce.Exit != cli.ExitUsage {
		t.Errorf("code/exit = %q/%d, want CONFIG_INVALID/%d", ce.Code, ce.Exit, cli.ExitUsage)
	}
	if out != "" {
		t.Errorf("wrote to stdout on failure: %q", out)
	}
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("an invalid set rewrote config.ktav")
	}
}

func TestConfigSecretsAreNeverDisplayedOrStoredPlaintext(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	hash, err := auth.Hash("pw-for-test")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfg.Auth.Login = "ops"
	cfg.Auth.Password = hash
	cfg.Chain.PostSOCKS5 = config.HopList{"socks5://alice:s3cret@relay.example:1080"}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save fixture: %v", err)
	}

	chainGroup := configGroups(t, "config", "get", "--home", home)["chain"]
	post, ok := chainGroup["post_socks5"].(map[string]any)
	if !ok {
		t.Fatal("chain.post_socks5 display is not an object")
	}
	postHops, ok := post["value"].([]any)
	if !ok || len(postHops) != 1 || postHops[0] != "socks5://relay.example:1080" {
		t.Fatal("chain.post_socks5 display does not contain the redacted one-hop list")
	}
	out := func() string {
		t.Helper()
		var buf bytes.Buffer
		if err := dispatch([]string{"config", "get", "--home", home}, &buf); err != nil {
			t.Fatalf("config get: %v", err)
		}
		return buf.String()
	}()
	for _, want := range []string{"set", "socks5://relay.example:1080", "\"auth\":true"} {
		if !strings.Contains(out, want) {
			t.Errorf("config get output lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{hash[:20], "s3cret", "alice"} {
		if strings.Contains(out, secret) {
			t.Errorf("config get output leaks %q", secret)
		}
	}

	data := configData(t, "config", "set", "auth.password", "fresh-secret", "--home", home)
	if data["key"] != "auth.password" {
		t.Errorf("data.key = %v, want auth.password", data["key"])
	}
	setOut, err := configDispatch(t, "config", "set", "auth.password", "fresh-secret", "--home", home)
	if err != nil {
		t.Fatalf("second set: %v", err)
	}
	if strings.Contains(setOut, "fresh-secret") {
		t.Error("config set output echoes the password")
	}
	file, cfg2 := readHomeConfig(t, home)
	if !strings.HasPrefix(cfg2.Auth.Password, "$argon2id$") {
		t.Errorf("stored password = %q, want an argon2id hash", cfg2.Auth.Password)
	}
	verified, err := auth.VerifyHash(cfg2.Auth.Password, "fresh-secret")
	if err != nil || !verified {
		t.Errorf("VerifyHash(stored, fresh-secret) = %v, %v, want true, nil", verified, err)
	}
	if strings.Contains(string(file), "fresh-secret") {
		t.Error("config.ktav stores the password in plaintext")
	}
}

func TestConfigSetClearBoolAndRestartFlags(t *testing.T) {
	home := t.TempDir()
	configData(t, "config", "set", "fetch.proxy", "socks5://127.0.0.1:1080", "--home", home)
	configData(t, "config", "set", "fetch.proxy", "none", "--home", home)
	if _, cfg := readHomeConfig(t, home); cfg.Fetch.Proxy != "" {
		t.Errorf("fetch.proxy = %q, want cleared", cfg.Fetch.Proxy)
	}

	configData(t, "config", "set", "auth.allow_init_remote", "yes", "--home", home)
	if _, cfg := readHomeConfig(t, home); !cfg.Auth.AllowInitRemote {
		t.Error("auth.allow_init_remote = false, want true")
	}

	_ = wantConfigUsage(t, []string{"config", "set", "auth.allow_init_remote", "maybe", "--home", home})

	data := configData(t, "config", "set", "chain.post_socks5", "socks5://127.0.0.1:1080", "--home", home)
	if data["restart_needed"] != true {
		t.Errorf("chain.post_socks5 restart_needed = %v, want true", data["restart_needed"])
	}

	data = configData(t, "config", "set", "fetch.update_interval", "30m", "--home", home)
	if data["restart_needed"] != false {
		t.Errorf("fetch.update_interval restart_needed = %v, want false", data["restart_needed"])
	}
}
