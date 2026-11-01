package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ktav-lang/golang/pure"
)

func serverBaseline() *Config {
	c := Default()
	c.Servers = []Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 8388, Accounts: "alice", Params: map[string]string{"cipher": "aes-128-gcm"}}}
	return c
}

func TestServerValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{"unknown protocol key", func(c *Config) { c.Servers[0].Params["token-secret"] = "x" }, "servers[0].token-secret"},
		{"invalid cipher", func(c *Config) { c.Servers[0].Params["cipher"] = "secret-value" }, "servers[0].cipher"},
		{"udp unsupported", func(c *Config) { c.Servers[0].Params["udp"] = "true" }, "servers[0].udp"},
		{"wrong UDP type in direct config", func(c *Config) { c.Servers[0].Params["udp"] = "false" }, ""},
		{"accounts empty", func(c *Config) { c.Servers[0].Accounts = "" }, "accounts"},
		{"accounts duplicate", func(c *Config) { c.Servers[0].Accounts = "alice,alice" }, "accounts"},
		{"accounts wildcard single", func(c *Config) { c.Servers[0].Accounts = "*" }, "accounts"},
		{"port low", func(c *Config) { c.Servers[0].Port = 0 }, "port"},
		{"port high", func(c *Config) { c.Servers[0].Port = 65536 }, "port"},
		{"bind invalid", func(c *Config) { c.Servers[0].Bind = "host.invalid" }, "bind"},
		{"port conflict with proxy", func(c *Config) { c.Servers[0].Port = c.Listen.Port }, "port"},
		{"unknown protocol", func(c *Config) { c.Servers[0].Protocol = "not-registered" }, "protocol"},
		{"bad name", func(c *Config) { c.Servers[0].Name = "bad name" }, "name"},
		{"duplicate name", func(c *Config) { c.Servers = append(c.Servers, c.Servers[0]) }, "name"},
		{"wildcard overlap", func(c *Config) {
			s := c.Servers[0]
			s.Name = "other"
			s.Bind = "0.0.0.0"
			c.Servers = append(c.Servers, s)
		}, "port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serverBaseline()
			tc.change(c)
			err := c.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			for _, secret := range []string{"secret-value", "token-secret"} {
				if strings.Contains(err.Error(), secret) && tc.name != "unknown protocol key" {
					t.Fatalf("error leaked value: %v", err)
				}
			}
		})
	}
}

func TestResolveServerAccountsAndLegacyInit(t *testing.T) {
	c := serverBaseline()
	c.Auth = Auth{Login: "legacy-user", Password: InitPlaceholder}
	c.Servers[0].Accounts = "legacy-user"
	resolved, err := c.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || len(resolved[0].Accounts) != 1 || resolved[0].Accounts[0] != "legacy-user" {
		t.Fatalf("resolved: %#v", resolved)
	}
	c.Servers[0].Accounts = "missing"
	if _, err = c.ResolveServers(); err == nil || !strings.Contains(err.Error(), "servers[0].accounts") {
		t.Fatalf("missing account error: %v", err)
	}
}

func TestServerEmptyArrayAndMarshalNoMutation(t *testing.T) {
	loaded, err := ktavLoadsIntoConfig("servers: []\n")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Servers == nil || len(loaded.Servers) != 0 {
		t.Fatalf("empty servers = %#v", loaded.Servers)
	}
	cfg := serverBaseline()
	cfg.Servers[0].Bind = ""
	cfg.Servers[0].Params = map[string]string{}
	bindBefore := cfg.Servers[0].Bind
	paramsBefore := cloneStringParams(cfg.Servers[0].Params)
	if _, err := Marshal(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Bind != bindBefore || !reflect.DeepEqual(cfg.Servers[0].Params, paramsBefore) {
		t.Fatalf("Marshal mutated server: %#v", cfg.Servers[0])
	}
}

func TestServerPortParsingUsesLosslessGenericValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  int
	}{
		{"integer", "8388", 8388},
		{"large integer scalar", "9223372036854775808", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "servers: [{name: edge, protocol: shadowsocks, port: " + tc.value + ", accounts: alice}]"
			cfg, err := ktavLoadsIntoConfig(doc)
			if tc.want == 0 {
				if err == nil || strings.Contains(err.Error(), tc.value) {
					t.Fatalf("error = %v, expected redacted invalid port", err)
				}
				return
			}
			if err != nil || len(cfg.Servers) != 1 || cfg.Servers[0].Port != tc.want {
				t.Fatalf("config = %#v, error = %v", cfg, err)
			}
		})
	}
}

func TestServerPortRejectsNonIntegerValues(t *testing.T) {
	for _, value := range []string{`8388.5`, `"8388"`, `true`, `null`, `999999999999999999999999999999999999999`} {
		t.Run(value, func(t *testing.T) {
			doc := "servers: [{name: edge, protocol: shadowsocks, port: " + value + ", accounts: alice}]"
			cfg, err := ktavLoadsIntoConfig(doc)
			if err == nil || cfg != nil || strings.Contains(err.Error(), value) {
				t.Fatalf("config = %#v, error = %v", cfg, err)
			}
		})
	}
}

func TestServerPortMarshalSaveLoad(t *testing.T) {
	cfg := serverBaseline()
	cfg.Servers[0].Params["udp"] = "false"
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "port: 8388") {
		t.Fatalf("Marshal did not emit numeric port: %s", doc)
	}
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Servers[0].Port != cfg.Servers[0].Port || loaded.Servers[0].Params["cipher"] != "aes-128-gcm" || loaded.Servers[0].Params["udp"] != "false" {
		t.Fatalf("loaded server = %#v", loaded.Servers[0])
	}
}

func TestServerParseAndParamsMarshalRoundTrip(t *testing.T) {
	src := `servers: [{name: edge, protocol: shadowsocks, port: 8388, accounts: alice, numeric: 123}]`
	var genericInto map[string]any
	if err := pure.LoadsInto(src, &genericInto); err != nil {
		t.Fatal(err)
	}
	intoPort := genericInto["servers"].([]any)[0].(map[string]any)["port"]
	if _, ok := intoPort.(float64); !ok {
		t.Fatalf("LoadsInto generic port = %T (%v), want float64", intoPort, intoPort)
	}
	raw, err := pure.Loads(src)
	if err != nil {
		t.Fatal(err)
	}
	gotPort := raw.(map[string]any)["servers"].([]any)[0].(map[string]any)["port"]
	if _, ok := gotPort.(int64); !ok {
		t.Fatalf("generic Ktav port = %T (%v), want int64", gotPort, gotPort)
	}
	gotParam := raw.(map[string]any)["servers"].([]any)[0].(map[string]any)["numeric"]
	if gotParam != int64(123) {
		t.Fatalf("generic Ktav numeric parameter = %#v", gotParam)
	}
	doc := `listen.bind: 127.0.0.1
listen.port: 7890
servers: [
    {
        name: edge
        protocol: shadowsocks
        port: 8388
        accounts: alice
        cipher: aes-128-gcm
    }
]
accounts: [
    {
        login: alice
        password:: init
    }
]
`
	loaded, err := ktavLoadsIntoConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, chain := range []bool{false, true} {
		c := *loaded
		if chain {
			c.Chain = Chain{PreSOCKS5: "socks5://127.0.0.1:1080"}
		}
		for _, params := range []map[string]string{nil, {}, loaded.Servers[0].Params} {
			c.Servers = append([]Server(nil), loaded.Servers...)
			c.Servers[0].Params = params
			out, err := Marshal(&c)
			if err != nil {
				t.Fatal(err)
			}
			round, err := ktavLoadsIntoConfig(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(round.Servers) != 1 || round.Servers[0].Params["cipher"] != params["cipher"] {
				t.Fatalf("round trip params %#v", round.Servers)
			}
		}
	}
}
