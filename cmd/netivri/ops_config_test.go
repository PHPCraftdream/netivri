package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// writeTestConfig is the only way tests create fixture files: always
// through config.Save(config.ConfigPath(home), cfg).
func writeTestConfig(t *testing.T, home string, cfg *config.Config) {
	t.Helper()
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save fixture: %v", err)
	}
}

func readTestConfigBytes(t *testing.T, home string) []byte {
	t.Helper()
	b, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	return b
}

// wantCLIError asserts err is a *cli.Error with the given code and
// returns it.
func wantCLIError(t *testing.T, err error, code string) *cli.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s error, got nil", code)
	}
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.Error, got %T: %v", err, err)
	}
	if ce.Code != code {
		t.Fatalf("code = %q, want %q (message %q)", ce.Code, code, ce.Message)
	}
	return ce
}

func TestConfigGetDefaultDisplayValuesOnEmptyHome(t *testing.T) {
	home := t.TempDir()
	res, err := ConfigGet(home, "")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if len(res.Groups) != 4 {
		t.Fatalf("groups = %d, want 4", len(res.Groups))
	}
	listen := res.Groups["listen"]
	if listen["bind"] != "127.0.0.1" {
		t.Errorf("bind = %v", listen["bind"])
	}
	if listen["port"] != 7890 {
		t.Errorf("port = %v, want int 7890", listen["port"])
	}
	if listen["allowed_clients"] != "*" {
		t.Errorf("allowed_clients = %v", listen["allowed_clients"])
	}
	a := res.Groups["auth"]
	if a["login"] != "" {
		t.Errorf("login = %v", a["login"])
	}
	if a["password"] != "not set" {
		t.Errorf("password = %v, want \"not set\"", a["password"])
	}
	if a["allow_init_remote"] != false {
		t.Errorf("allow_init_remote = %v, want false", a["allow_init_remote"])
	}
	for _, key := range []string{"proxy", "pre_socks5", "post_socks5"} {
		group := res.Groups["fetch"]
		if key != "proxy" {
			group = res.Groups["chain"]
		}
		pv, ok := group[key].(configProxyValue)
		if !ok {
			t.Fatalf("%s display = %T, want configProxyValue", key, group[key])
		}
		if pv.Value != "" || pv.Auth {
			t.Errorf("%s = %+v, want empty", key, pv)
		}
	}
	if res.Groups["fetch"]["update_interval"] != "" {
		t.Errorf("update_interval = %v, want empty", res.Groups["fetch"]["update_interval"])
	}
}

func TestConfigGetKeyFilter(t *testing.T) {
	home := t.TempDir()
	res, err := ConfigGet(home, "listen.port")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if len(res.Groups) != 1 {
		t.Fatalf("groups = %d (%v), want only listen", len(res.Groups), res.Groups)
	}
	if _, ok := res.Groups["listen"]["port"]; !ok {
		t.Errorf("missing listen.port in %+v", res.Groups["listen"])
	}

	_, err = ConfigGet(home, "nope.notakey")
	ce := wantCLIError(t, err, "USAGE")
	if ce.Exit != cli.ExitUsage {
		t.Errorf("exit = %d, want %d", ce.Exit, cli.ExitUsage)
	}
	if !strings.Contains(ce.Hint, "listen.bind") || !strings.Contains(ce.Hint, "chain.post_socks5") {
		t.Errorf("hint %q does not list valid keys", ce.Hint)
	}
}

func TestConfigGetRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	hash, err := auth.Hash("pw-for-test")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfg := config.Default()
	cfg.Auth.Password = hash
	cfg.Chain.PreSOCKS5 = "socks5://alice:s3cret@relay.example:1080"
	cfg.Fetch.Proxy = "http://u:p@proxy.example:8443"
	writeTestConfig(t, home, cfg)

	res, err := ConfigGet(home, "")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if got := res.Groups["auth"]["password"]; got != "set" {
		t.Errorf("password display = %v, want \"set\"", got)
	}
	pre, ok := res.Groups["chain"]["pre_socks5"].(configProxyValue)
	if !ok {
		t.Fatalf("pre_socks5 display = %T", res.Groups["chain"]["pre_socks5"])
	}
	if pre.Value != "socks5://relay.example:1080" || !pre.Auth {
		t.Errorf("pre_socks5 = %+v", pre)
	}
	fp, ok := res.Groups["fetch"]["proxy"].(configProxyValue)
	if !ok {
		t.Fatalf("proxy display = %T", res.Groups["fetch"]["proxy"])
	}
	if fp.Value != "http://proxy.example:8443" || !fp.Auth {
		t.Errorf("proxy = %+v", fp)
	}
	blob, jerr := json.Marshal(res)
	if jerr != nil {
		t.Fatalf("marshal: %v", jerr)
	}
	for _, secret := range []string{"$argon2id$", "s3cret", "alice", "u:p", "pw-for-test"} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Errorf("result JSON leaks %q", secret)
		}
	}
}

func TestConfigKeyMetadata(t *testing.T) {
	keys := ConfigKeys()
	if len(keys) != 10 {
		t.Fatalf("len(ConfigKeys) = %d, want 10", len(keys))
	}
	want := []struct {
		key      string
		kind     ConfigKeyKind
		restarts bool
	}{
		{"listen.bind", ConfigKeyText, true},
		{"listen.port", ConfigKeyPort, true},
		{"listen.allowed_clients", ConfigKeyText, true},
		{"auth.login", ConfigKeyText, true},
		{"auth.password", ConfigKeyPassword, true},
		{"auth.allow_init_remote", ConfigKeyBool, false},
		{"fetch.proxy", ConfigKeyProxy, false},
		{"fetch.update_interval", ConfigKeyText, false},
		{"chain.pre_socks5", ConfigKeyProxy, false},
		{"chain.post_socks5", ConfigKeyProxy, true},
	}
	for i, w := range want {
		k := keys[i]
		if k.Key != w.key || k.Kind != w.kind || k.Restarts != w.restarts {
			t.Errorf("key %d = %+v, want %s kind %d restarts %v", i, k, w.key, w.kind, w.restarts)
		}
		if byName, ok := ConfigKeyByName(w.key); !ok || byName != k {
			t.Errorf("ConfigKeyByName(%q) mismatch", w.key)
		}
	}
	if _, ok := ConfigKeyByName("listen.Bind"); ok {
		t.Errorf("ConfigKeyByName matched a non-exact key")
	}
	cfg := config.Default()
	if v := ConfigDisplayValue(cfg, "no.suchkey"); v != nil {
		t.Errorf("unknown display = %v, want nil", v)
	}
	if v, ok := ConfigRawValue(cfg, "auth.password"); ok || v != "" {
		t.Errorf("password raw = (%q, %v), want hidden", v, ok)
	}
	if v, ok := ConfigRawValue(cfg, "chain.pre_socks5"); ok || v != "" {
		t.Errorf("proxy raw = (%q, %v), want hidden", v, ok)
	}
	if v, ok := ConfigRawValue(cfg, "no.suchkey"); ok || v != "" {
		t.Errorf("unknown raw = (%q, %v), want hidden", v, ok)
	}
	if v, ok := ConfigRawValue(cfg, "listen.port"); !ok || v != "7890" {
		t.Errorf("port raw = (%q, %v), want (7890, true)", v, ok)
	}
	if v, ok := ConfigRawValue(cfg, "auth.allow_init_remote"); !ok || v != "false" {
		t.Errorf("bool raw = (%q, %v), want (false, true)", v, ok)
	}
}

func TestConfigSetEachKey(t *testing.T) {
	t.Run("port", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "listen.port", "8080")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded || res.Key != "listen.port" {
			t.Errorf("result = %+v", res)
		}
		cfg, err := loadConfig(home)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if cfg.Listen.Port != 8080 {
			t.Errorf("port = %d, want 8080", cfg.Listen.Port)
		}
	})
	t.Run("bind", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "listen.bind", "localhost")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		cfg, _ := loadConfig(home)
		if cfg.Listen.Bind != "localhost" {
			t.Errorf("bind = %q", cfg.Listen.Bind)
		}
	})
	t.Run("allowed_clients", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "listen.allowed_clients", "127.0.0.1")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		cfg, _ := loadConfig(home)
		if cfg.Listen.AllowedClients != "127.0.0.1" {
			t.Errorf("allowed_clients = %q", cfg.Listen.AllowedClients)
		}
	})
	t.Run("login", func(t *testing.T) {
		home := t.TempDir()
		cfg := config.Default()
		cfg.Auth.Login, cfg.Auth.Password = "old", config.InitPlaceholder
		writeTestConfig(t, home, cfg)
		res, err := ConfigSet(home, "auth.login", "bob")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Auth.Login != "bob" || lc.Auth.Password != config.InitPlaceholder {
			t.Errorf("auth = %+v", lc.Auth)
		}
	})
	t.Run("password plain", func(t *testing.T) {
		home := t.TempDir()
		cfg := config.Default()
		cfg.Auth.Login, cfg.Auth.Password = "alice", config.InitPlaceholder
		writeTestConfig(t, home, cfg)
		res, err := ConfigSet(home, "auth.password", "sup3rsecret-pw")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if !strings.HasPrefix(lc.Auth.Password, "$argon2id$") {
			t.Errorf("stored password is not an argon2id hash")
		}
		if ok, verr := auth.VerifyHash(lc.Auth.Password, "sup3rsecret-pw"); verr != nil || !ok {
			t.Errorf("VerifyHash = %v, %v", ok, verr)
		}
		if blob := readTestConfigBytes(t, home); bytes.Contains(blob, []byte("sup3rsecret-pw")) {
			t.Errorf("saved file contains the plain password")
		}
	})
	t.Run("password init", func(t *testing.T) {
		home := t.TempDir()
		cfg := config.Default()
		cfg.Auth.Login = "alice"
		writeTestConfig(t, home, cfg)
		res, err := ConfigSet(home, "auth.password", "init")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Auth.Password != "init" {
			t.Errorf("password = %q, want literal init", lc.Auth.Password)
		}
	})
	t.Run("allow_init_remote", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "auth.allow_init_remote", "true")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if !lc.Auth.AllowInitRemote {
			t.Errorf("allow_init_remote = false")
		}
	})
	t.Run("fetch.proxy", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "fetch.proxy", "socks5://p.example:1080")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Fetch.Proxy != "socks5://p.example:1080" {
			t.Errorf("proxy = %q", lc.Fetch.Proxy)
		}
	})
	t.Run("update_interval", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "fetch.update_interval", "30m")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Fetch.UpdateInterval != "30m" {
			t.Errorf("update_interval = %q", lc.Fetch.UpdateInterval)
		}
		res, err = ConfigSet(home, "fetch.update_interval", "off")
		if err != nil {
			t.Fatalf("set off: %v", err)
		}
		if !res.Changed || res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ = loadConfig(home)
		if lc.Fetch.UpdateInterval != "off" {
			t.Errorf("update_interval = %q, want off", lc.Fetch.UpdateInterval)
		}
	})
	t.Run("chain.pre_socks5", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "chain.pre_socks5", "http://pre.example:8080")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Chain.PreSOCKS5 != "http://pre.example:8080" {
			t.Errorf("pre = %q", lc.Chain.PreSOCKS5)
		}
	})
	t.Run("chain.post_socks5", func(t *testing.T) {
		home := t.TempDir()
		res, err := ConfigSet(home, "chain.post_socks5", "socks5://post.example:1080")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !res.Changed || !res.RestartNeeded {
			t.Errorf("result = %+v", res)
		}
		lc, _ := loadConfig(home)
		if lc.Chain.PostSOCKS5 != "socks5://post.example:1080" {
			t.Errorf("post = %q", lc.Chain.PostSOCKS5)
		}
	})
}

func TestConfigSetClear(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Fetch.Proxy = "socks5://p.example:1080"
	cfg.Auth.Login, cfg.Auth.Password = "alice", config.InitPlaceholder
	cfg.Chain.PostSOCKS5 = "socks5://post.example:1080"
	writeTestConfig(t, home, cfg)

	if res, err := ConfigSet(home, "fetch.proxy", "none"); err != nil || !res.Changed {
		t.Fatalf("clear fetch.proxy: %+v, %v", res, err)
	}
	lc, err := loadConfig(home)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if lc.Fetch.Proxy != "" {
		t.Errorf("cleared Fetch.Proxy = %q", lc.Fetch.Proxy)
	}

	// The config file's validation (config.Validate) enforces the auth
	// pair rule: auth.login and auth.password must be set together or
	// not at all ("auth.password requires auth.login" and vice versa).
	// Because `config set` changes one key at a time, clearing either
	// half alone would leave the pair inconsistent, so a single-key
	// clear of auth.login or auth.password is CONFIG_INVALID by design;
	// both must be cleared together by editing the file.
	before := readTestConfigBytes(t, home)
	_, err = ConfigSet(home, "auth.login", "none")
	if ce := wantCLIError(t, err, config.CodeConfigInvalid); !strings.Contains(ce.Message, "auth.password") {
		t.Errorf("auth.login clear message = %q, want contains \"auth.password\"", ce.Message)
	}
	blob := readTestConfigBytes(t, home)
	if !bytes.Equal(blob, before) {
		t.Error("rejected auth.login clear modified the config file")
	}
	_, err = ConfigSet(home, "auth.password", "none")
	if ce := wantCLIError(t, err, config.CodeConfigInvalid); !strings.Contains(ce.Message, "auth.login") {
		t.Errorf("auth.password clear message = %q, want contains \"auth.login\"", ce.Message)
	}
	blob = readTestConfigBytes(t, home)
	if !bytes.Equal(blob, before) {
		t.Error("rejected auth.password clear modified the config file")
	}

	// The auth pair REMAINS in the file after the rejected clears; only
	// the cleared "proxy" must be gone (omitempty).
	for _, marker := range []string{"proxy"} {
		if bytes.Contains(blob, []byte(marker)) {
			t.Errorf("saved file still contains %q after omitempty clear", marker)
		}
	}
	for _, marker := range []string{"login", "password"} {
		if !bytes.Contains(blob, []byte(marker)) {
			t.Errorf("saved file lost %q after rejected auth clear", marker)
		}
	}
}

func TestConfigSetUnchanged(t *testing.T) {
	home := t.TempDir()
	res, err := ConfigSet(home, "listen.port", "7890")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Changed || res.RestartNeeded {
		t.Errorf("result = %+v, want no change", res)
	}

	// Same plain password twice: the second attempt must detect the
	// stored hash already matches and change nothing.
	pair := t.TempDir()
	cfg := config.Default()
	cfg.Auth.Login, cfg.Auth.Password = "alice", config.InitPlaceholder
	writeTestConfig(t, pair, cfg)
	if res, err := ConfigSet(pair, "auth.password", "dup-secret"); err != nil || !res.Changed {
		t.Fatalf("first password set: %+v, %v", res, err)
	}
	before := readTestConfigBytes(t, pair)
	res, err = ConfigSet(pair, "auth.password", "dup-secret")
	if err != nil {
		t.Fatalf("second password set: %v", err)
	}
	if res.Changed || res.RestartNeeded {
		t.Errorf("result = %+v, want no change", res)
	}
	if after := readTestConfigBytes(t, pair); !bytes.Equal(before, after) {
		t.Errorf("file changed on a no-op set")
	}
}

func TestConfigSetInvalidNothingWritten(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		value  string
		code   string
		banned string // value fragment that must not appear in the message
	}{
		{"port out of range", "listen.port", "70000", config.CodeConfigInvalid, "70000"},
		{"port not a number", "listen.port", "abc", "USAGE", "abc"},
		{"bad bind", "listen.bind", "999.1.1.1", config.CodeConfigInvalid, "999.1.1.1"},
		{"empty allowed_clients", "listen.allowed_clients", "", config.CodeConfigInvalid, ""},
		{"short interval", "fetch.update_interval", "1m", config.CodeConfigInvalid, "1m"},
		{"bad scheme", "chain.pre_socks5", "ftp://x:1", config.CodeConfigInvalid, "ftp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeTestConfig(t, home, config.Default())
			before := readTestConfigBytes(t, home)
			_, err := ConfigSet(home, tc.key, tc.value)
			ce := wantCLIError(t, err, tc.code)
			if tc.banned != "" && strings.Contains(ce.Message, tc.banned) {
				t.Errorf("message %q contains the rejected value", ce.Message)
			}
			if after := readTestConfigBytes(t, home); !bytes.Equal(before, after) {
				t.Errorf("file bytes changed on a rejected set")
			}
		})
	}
	t.Run("password without login", func(t *testing.T) {
		home := t.TempDir()
		writeTestConfig(t, home, config.Default())
		before := readTestConfigBytes(t, home)
		_, err := ConfigSet(home, "auth.password", "n0-login-secret")
		ce := wantCLIError(t, err, config.CodeConfigInvalid)
		if !strings.Contains(ce.Message, "auth.login") {
			t.Errorf("message %q does not name auth.login", ce.Message)
		}
		if strings.Contains(ce.Message, "n0-login-secret") {
			t.Errorf("message %q echoes the password", ce.Message)
		}
		if after := readTestConfigBytes(t, home); !bytes.Equal(before, after) {
			t.Errorf("file bytes changed on a rejected set")
		}
	})
}

func TestConfigSetBoolVariantsAndUnknownKey(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"yes", "YES", "y", "1", "t", "true"} {
		if _, err := ConfigSet(home, "auth.allow_init_remote", v); err != nil {
			t.Fatalf("set %q: %v", v, err)
		}
		lc, _ := loadConfig(home)
		if !lc.Auth.AllowInitRemote {
			t.Errorf("value %q did not store true", v)
		}
		if _, err := ConfigSet(home, "auth.allow_init_remote", "no"); err != nil {
			t.Fatalf("set no: %v", err)
		}
		lc, _ = loadConfig(home)
		if lc.Auth.AllowInitRemote {
			t.Errorf("value %q pass left allow_init_remote true", v)
		}
	}
	if _, err := ConfigSet(home, "auth.allow_init_remote", "maybe"); err == nil {
		t.Fatalf("\"maybe\" accepted")
	} else {
		wantCLIError(t, err, "USAGE")
	}

	tooLong := strings.Repeat("x", auth.MaxPasswordLen+1)
	_, err := ConfigSet(home, "auth.password", tooLong)
	ce := wantCLIError(t, err, "USAGE")
	if strings.Contains(ce.Message, "xxxx") {
		t.Errorf("message echoes the rejected password")
	}

	for _, op := range []func() error{
		func() error { _, err := ConfigGet(home, "bogus.key"); return err },
		func() error { _, err := ConfigSet(home, "bogus.key", "1"); return err },
	} {
		ce := wantCLIError(t, op(), "USAGE")
		if !strings.Contains(ce.Hint, "valid keys") {
			t.Errorf("hint %q missing key list", ce.Hint)
		}
	}
}

func TestConfigSetWholeConfigValidation(t *testing.T) {
	home := t.TempDir()
	hash, err := auth.Hash("pw-account")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfg := config.Default()
	cfg.Accounts = append(cfg.Accounts, config.Account{Login: "alice", Password: hash})
	writeTestConfig(t, home, cfg)
	before := readTestConfigBytes(t, home)
	_, err = ConfigSet(home, "auth.login", "alice")
	wantCLIError(t, err, config.CodeConfigInvalid)
	if after := readTestConfigBytes(t, home); !bytes.Equal(before, after) {
		t.Errorf("file bytes changed on a rejected set")
	}
}
