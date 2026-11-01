package config

import (
	"reflect"
	"strings"
	"testing"
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

func TestServerParseAndParamsMarshalRoundTrip(t *testing.T) {
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
