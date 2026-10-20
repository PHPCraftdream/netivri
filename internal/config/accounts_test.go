package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestAccountHelpers(t *testing.T) {
	for _, login := range []string{"a", "Abc_@.-9", strings.Repeat("x", 64)} {
		if !ValidAccountLogin(login) {
			t.Errorf("expected valid login")
		}
	}
	for _, login := range []string{"", strings.Repeat("x", 65), "a/b", "a b", "é"} {
		if ValidAccountLogin(login) {
			t.Errorf("expected invalid login")
		}
	}
	if got, ok := NormalizeCountry("dE"); !ok || got != "DE" {
		t.Fatalf("NormalizeCountry = %q, %v", got, ok)
	}
	for _, code := range []string{"", "USA", "1A", "éA", " D", "D ", "DE "} {
		if _, ok := NormalizeCountry(code); ok {
			t.Errorf("accepted invalid country")
		}
	}
	c := &Config{Accounts: []Account{{Login: "alice"}, {Login: "Alice"}}}
	if a, i := c.Account("alice"); a == nil || i != 0 {
		t.Fatal("account lookup failed")
	} else {
		a.Login = "changed"
		if c.Accounts[0].Login != "changed" {
			t.Fatal("lookup is not slice pointer")
		}
	}
	if a, i := c.Account("Alice"); a == nil || i != 1 {
		t.Fatal("case-sensitive lookup failed")
	}
	if a, i := c.Account("missing"); a != nil || i != -1 {
		t.Fatal("absent lookup mismatch")
	}
}

func TestValidateAccounts(t *testing.T) {
	valid := func() *Config {
		c := Default()
		c.Accounts = []Account{{Login: "alice", Password: InitPlaceholder, ExitCountry: "de"}}
		return c
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	phc := valid()
	phc.Accounts[0].Password = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$hash"
	if err := phc.Validate(); err != nil {
		t.Fatalf("PHC: %v", err)
	}
	remote := valid()
	remote.Listen.Bind = "0.0.0.0"
	remote.Auth.AllowInitRemote = true
	if err := remote.Validate(); err != nil {
		t.Fatalf("remote init: %v", err)
	}
	remote.Accounts[0].Password = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$hash"
	remote.Auth.AllowInitRemote = false
	if err := remote.Validate(); err != nil {
		t.Fatalf("remote hash: %v", err)
	}
	legacy := valid()
	legacy.Auth = Auth{Login: "init", Password: "init"}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("auth init exemption: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad login", func(c *Config) { c.Accounts[0].Login = "bad/login" }, "accounts[0].login"},
		{"empty login", func(c *Config) { c.Accounts[0].Login = "" }, "accounts[0].login"},
		{"duplicate", func(c *Config) { c.Accounts = append(c.Accounts, c.Accounts[0]) }, "duplicates accounts[0]"},
		{"reserved", func(c *Config) { c.Accounts[0].Login = InitPlaceholder }, "reserved"},
		{"auth collision", func(c *Config) { c.Auth = Auth{Login: "alice", Password: "init"} }, "duplicates auth.login"},
		{"bad password", func(c *Config) { c.Accounts[0].Password = "password-secret" }, "accounts[0].password"},
		{"empty password", func(c *Config) { c.Accounts[0].Password = "" }, "accounts[0].password"},
		{"password line break", func(c *Config) { c.Accounts[0].Password = "$argon2id$\nsecret" }, "accounts[0].password"},
		{"bad country", func(c *Config) { c.Accounts[0].ExitCountry = "country-secret" }, "exit_country"},
		{"remote pending", func(c *Config) { c.Listen.Bind = "0.0.0.0" }, "allow_init_remote"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(c)
			err := c.Validate()
			ce, ok := err.(*cli.Error)
			if !ok || ce.Code != CodeConfigInvalid || ce.Exit != cli.ExitUsage || !strings.Contains(ce.Message, tc.want) {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestAccountValidationErrorsDoNotLeakValues(t *testing.T) {
	cases := []*Config{}
	dup := Default()
	dup.Accounts = []Account{{Login: "secret-login", Password: "init"}, {Login: "secret-login", Password: "init"}}
	cases = append(cases, dup)
	collision := Default()
	collision.Auth = Auth{Login: "collision-secret", Password: "init"}
	collision.Accounts = []Account{{Login: "collision-secret", Password: "init"}}
	cases = append(cases, collision)
	pw := Default()
	pw.Accounts = []Account{{Login: "alice", Password: "password-secret"}}
	cases = append(cases, pw)
	country := Default()
	country.Accounts = []Account{{Login: "alice", Password: "init", ExitCountry: "country-secret"}}
	cases = append(cases, country)
	for _, c := range cases {
		err := c.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		ce, ok := err.(*cli.Error)
		if !ok || ce.Code != CodeConfigInvalid {
			t.Fatalf("error type/code: %T %v", err, err)
		}
		for _, secret := range []string{"secret-login", "collision-secret", "password-secret", "country-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatal("account error leaked value")
			}
		}
	}
}

type previousConfig struct {
	Listen        Listen         `json:"listen"`
	Auth          Auth           `json:"auth"`
	Fetch         Fetch          `json:"fetch"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	Selection     Selection      `json:"selection"`
}

func TestMarshalAccountsBackwardCompatibility(t *testing.T) {
	requireKtavLib(t)
	for _, base := range []*Config{Default(), fullConfig()} {
		for _, accounts := range [][]Account{nil, {}} {
			cfg := *base
			cfg.Accounts = accounts
			got, err := Marshal(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			body, err := ktavDumps(previousConfig{
				Listen: cfg.Listen, Auth: cfg.Auth, Fetch: cfg.Fetch,
				Subscriptions: cfg.Subscriptions, Selection: cfg.Selection,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != managedHeader+body {
				t.Fatal("no-account rendering changed")
			}
		}
	}
}

func TestMarshalAndLoadAccountsRawScalarsAndNormalization(t *testing.T) {
	requireKtavLib(t)
	cfg := Default()
	cfg.Accounts = []Account{
		{Login: "123", Password: "init", ExitCountry: "NO"},
		{Login: "true", Password: "init", ExitCountry: "TR"},
		{Login: "null", Password: "init"},
	}
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"login:: 123", "login:: true", "login:: null"} {
		if !strings.Contains(doc, line) {
			t.Fatalf("missing raw marker %q", line)
		}
	}
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("roundtrip mismatch: %#v", loaded)
	}
}

func TestLoadNormalizesLowercaseAccountCountry(t *testing.T) {
	requireKtavLib(t)
	fixture := `listen.bind: 127.0.0.1
listen.port: 7890
accounts: [
    {
        login: alice
        password:: init
        exit_country: de
    }
]
`
	path := filepath.Join(t.TempDir(), "lower-country.ktav")
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Accounts[0].ExitCountry != "DE" {
		t.Fatalf("loaded country = %q, want DE", loaded.Accounts[0].ExitCountry)
	}
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Accounts[0].ExitCountry != "DE" {
		t.Fatalf("saved/reloaded country = %q, want DE", reloaded.Accounts[0].ExitCountry)
	}
}
