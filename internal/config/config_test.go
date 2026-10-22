package config

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// validBaseline is a fully populated configuration that passes
// Validate; invalid cases below each change exactly one field, so a
// removed rule makes its test fail.
func validBaseline() *Config {
	cfg := Default()
	cfg.Auth = Auth{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$hashhashhash"}
	cfg.Fetch = Fetch{Proxy: "socks5://alice:pw@proxy.example:1080"}
	cfg.Subscriptions = []Subscription{
		{Name: "one", URL: "https://one.example/sub"},
		{Name: "two", URL: "https://two.example/sub", Via: "http://proxy.example:8080"},
	}
	cfg.Selection.Country = "DE"
	return cfg
}

// remoteInitBaseline is the valid baseline for the remote-init rule:
// non-loopback bind, init credentials, allow_init_remote true. The
// invalid case below flips exactly that one field to false.
func remoteInitBaseline() *Config {
	cfg := Default()
	cfg.Listen.Bind = "0.0.0.0"
	cfg.Auth = Auth{Login: InitPlaceholder, Password: InitPlaceholder, AllowInitRemote: true}
	return cfg
}

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	if cfg.Listen.Bind != "127.0.0.1" || cfg.Listen.Port != 7890 {
		t.Fatalf("unexpected defaults: %+v", cfg.Listen)
	}
	if cfg.Selection.Mode != ModeManual {
		t.Fatalf("default mode = %q, want %q", cfg.Selection.Mode, ModeManual)
	}
}

func TestFetchInterval(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		enabled bool
	}{
		{"", time.Hour, true},
		{"1h", time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"90m", 90 * time.Minute, true},
		{UpdateOff, 0, false},
	}
	for _, tc := range cases {
		got, enabled := Fetch{UpdateInterval: tc.raw}.Interval()
		if got != tc.want || enabled != tc.enabled {
			t.Errorf("Interval(%q) = %v, %v; want %v, %v", tc.raw, got, enabled, tc.want, tc.enabled)
		}
	}
}

func TestValidateValid(t *testing.T) {
	cases := map[string]*Config{
		"baseline":               validBaseline(),
		"bind localhost":         func() *Config { c := validBaseline(); c.Listen.Bind = "localhost"; return c }(),
		"bind ipv6":              func() *Config { c := validBaseline(); c.Listen.Bind = "::1"; return c }(),
		"port 1":                 func() *Config { c := validBaseline(); c.Listen.Port = 1; return c }(),
		"port 65535":             func() *Config { c := validBaseline(); c.Listen.Port = 65535; return c }(),
		"no auth":                func() *Config { c := validBaseline(); c.Auth = Auth{}; return c }(),
		"legacy arbitrary login": func() *Config { c := validBaseline(); c.Auth.Login = "legacy user"; return c }(),
		"init placeholder": func() *Config {
			c := validBaseline()
			c.Listen.Bind = "127.0.0.1"
			c.Auth = Auth{Login: InitPlaceholder, Password: InitPlaceholder}
			return c
		}(),
		"no subscriptions":  func() *Config { c := validBaseline(); c.Subscriptions = nil; return c }(),
		"remote init valid": remoteInitBaseline(),
		"interval 1h":       func() *Config { c := validBaseline(); c.Fetch.UpdateInterval = "1h"; return c }(),
		"interval 5m":       func() *Config { c := validBaseline(); c.Fetch.UpdateInterval = "5m"; return c }(),
		"interval 168h":     func() *Config { c := validBaseline(); c.Fetch.UpdateInterval = "168h"; return c }(),
		"interval off":      func() *Config { c := validBaseline(); c.Fetch.UpdateInterval = UpdateOff; return c }(),
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); err != nil {
			t.Errorf("case %s: unexpected error: %v", name, err)
		}
	}
}

func TestValidateInvalid(t *testing.T) {
	cases := []struct {
		name     string
		baseline func() *Config
		change   func(*Config)
		contains string
	}{
		{"zero value", func() *Config { return &Config{} }, func(c *Config) {}, "listen.bind"},
		{"port zero", validBaseline, func(c *Config) { c.Listen.Port = 0 }, "listen.port"},
		{"port too big", validBaseline, func(c *Config) { c.Listen.Port = 65536 }, "listen.port"},
		{"bad bind", validBaseline, func(c *Config) { c.Listen.Bind = "example.com" }, "listen.bind"},
		{"bad proxy scheme", validBaseline, func(c *Config) { c.Fetch.Proxy = "ftp://proxy.example:21" }, "fetch.proxy"},
		{"proxy without host", validBaseline, func(c *Config) { c.Fetch.Proxy = "socks5://" }, "fetch.proxy"},
		{"bad via", validBaseline, func(c *Config) { c.Subscriptions[1].Via = "gopher://x" }, "via"},
		{"empty name", validBaseline, func(c *Config) { c.Subscriptions[0].Name = " " }, "subscriptions[0].name"},
		{"empty url", validBaseline, func(c *Config) { c.Subscriptions[0].URL = "" }, "subscriptions[0].url"},
		{"duplicate names", validBaseline, func(c *Config) { c.Subscriptions[1].Name = "one" }, "duplicates subscriptions[0]"},
		{"bad mode", validBaseline, func(c *Config) { c.Selection.Mode = "turbo" }, "selection.mode"},
		{"plaintext password", validBaseline, func(c *Config) { c.Auth.Password = "hunter2" }, "auth.password"},
		{"password without login", validBaseline, func(c *Config) { c.Auth.Login = "" }, "auth.login"},
		{"login without password", validBaseline, func(c *Config) { c.Auth = Auth{Login: "alice"} }, "auth.login"},
		{"login leading space", validBaseline, func(c *Config) { c.Auth.Login = " alice" }, "auth.login"},
		{"login line break", validBaseline, func(c *Config) { c.Auth.Login = "a\nlice" }, "auth.login"},
		{"name line break", validBaseline, func(c *Config) { c.Subscriptions[0].Name = "o\nne" }, "subscriptions[0].name"},
		{"proxy leading space", validBaseline, func(c *Config) { c.Fetch.Proxy = " socks5://proxy.example:1080" }, "fetch.proxy"},
		{"sub name trailing space", validBaseline, func(c *Config) { c.Subscriptions[0].Name = "one " }, "subscriptions[0].name"},
		{"sub url control char", validBaseline, func(c *Config) { c.Subscriptions[0].URL = "https://x.example/\x01" }, "subscriptions[0].url"},
		{"via tab", validBaseline, func(c *Config) { c.Subscriptions[1].Via = "http://via	.example:3128" }, "subscriptions[1].via"},
		{"country line break", validBaseline, func(c *Config) { c.Selection.Country = "D\ne" }, "selection.country"},
		{"node trailing space", validBaseline, func(c *Config) { c.Selection.Node = "node-1 " }, "selection.node"},
		{"remote init not allowed", remoteInitBaseline, func(c *Config) { c.Auth.AllowInitRemote = false }, "allow_init_remote"},
		{"interval not a duration", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "hourly" }, "fetch.update_interval"},
		{"interval without unit", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "60" }, "fetch.update_interval"},
		{"interval too short", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "4m59s" }, "fetch.update_interval"},
		{"interval too long", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "169h" }, "fetch.update_interval"},
		{"interval negative", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "-1h" }, "fetch.update_interval"},
		{"interval upper-case off", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "OFF" }, "fetch.update_interval"},
		{"interval trailing space", validBaseline, func(c *Config) { c.Fetch.UpdateInterval = "1h " }, "fetch.update_interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.baseline()
			tc.change(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			cliErr, ok := err.(*cli.Error)
			if !ok {
				t.Fatalf("error type = %T, want *cli.Error", err)
			}
			if cliErr.Code != CodeConfigInvalid {
				t.Errorf("code = %q, want %q", cliErr.Code, CodeConfigInvalid)
			}
			if cliErr.Exit != cli.ExitUsage {
				t.Errorf("exit = %d, want %d", cliErr.Exit, cli.ExitUsage)
			}
			if !strings.Contains(cliErr.Message, tc.contains) {
				t.Errorf("message %q does not mention %q", cliErr.Message, tc.contains)
			}
		})
	}
}

// TestErrorsNeverEchoSecrets feeds secret-looking values into invalid
// configurations and asserts no error message carries them.
func TestErrorsNeverEchoSecrets(t *testing.T) {
	secretURL := "https://secret.example/sub/TOKEN"
	secretPassword := "hunter2-secret-value"
	secretProxy := "secretpw"

	dup := validBaseline()
	dup.Subscriptions[0].Name = secretURL
	dup.Subscriptions[1].Name = secretURL

	pw := validBaseline()
	pw.Auth.Password = secretPassword

	proxy := validBaseline()
	proxy.Fetch.Proxy = "ftp://alice:" + secretProxy + "@proxy.example:1080"

	for _, cfg := range []*Config{dup, pw, proxy} {
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := err.Error()
		for _, secret := range []string{secretURL, secretPassword, secretProxy} {
			if strings.Contains(msg, secret) {
				t.Errorf("error leaks a secret: %q", msg)
			}
		}
	}
	if err := dup.Validate(); err != nil {
		if cliErr := err.(*cli.Error); !strings.Contains(cliErr.Message, "duplicates subscriptions[0]") {
			t.Errorf("duplicate error should name positions: %q", cliErr.Message)
		}
	}
}
