package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestCovServerParseShapes(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"root scalar", "hello", "config parse error"},
		{"servers table", "servers: {}", "config parse error"},
		{"non table element", "servers: [hello]", "config parse error"},
		{"known field scalar", "servers: [{name: 3}]", "config parse error"},
		{"parameter sequence", "servers: [{name: edge, values: [a, b]}]", "servers[0].values must be a scalar string, boolean, or integer"},
		{"parameter table", "servers: [{name: edge, values: {a: b}}]", "servers[0].values must be a scalar string, boolean, or integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ktavLoadsIntoConfig(tc.doc)
			if err == nil || got != nil {
				t.Fatalf("config=%#v error=%v", got, err)
			}
			if e, ok := err.(*cli.Error); !ok || e.Code != CodeConfigInvalid || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error class=%T %v", err, err)
			}
		})
	}
}

func TestCovProtocolParamScalarTypes(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{"text", "text"}, {true, "true"}, {int(7), "7"}, {int64(8), "8"}, {uint64(9), "9"},
		{int32(10), "10"}, {uint32(11), "11"}, {int16(12), "12"}, {uint16(13), "13"}, {int8(14), "14"}, {uint8(15), "15"},
	} {
		got, ok := protocolParamString(tc.value)
		if !ok || got != tc.want {
			t.Errorf("protocolParamString(%T)=(%q,%v), want %q", tc.value, got, ok, tc.want)
		}
	}
	if got, ok := protocolParamString([]string{"bad"}); ok || got != "" {
		t.Fatalf("unsupported scalar=(%q,%v)", got, ok)
	}
}

func TestCovStrconvValueBranches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  int
		fail  bool
	}{
		{"int", int(0), 0, false},
		{"int64 valid", int64(65535), 65535, false}, {"int64 zero", int64(0), 0, true}, {"int64 high", int64(65536), 0, true},
		{"uint64 valid", uint64(1), 1, false}, {"uint64 zero", uint64(0), 0, true}, {"uint64 high", uint64(65536), 0, true},
		{"int32 valid", int32(42), 42, false}, {"int32 zero", int32(0), 0, true}, {"int32 high", int32(65536), 0, true},
		{"uint32 valid", uint32(42), 42, false}, {"uint32 zero", uint32(0), 0, true}, {"uint32 high", uint32(65536), 0, true},
		{"int16 valid", int16(42), 42, false}, {"int16 zero", int16(0), 0, true},
		{"uint16 valid", uint16(42), 42, false}, {"uint16 zero", uint16(0), 0, true},
		{"int8 valid", int8(42), 42, false}, {"int8 zero", int8(0), 0, true},
		{"uint8 valid", uint8(42), 42, false}, {"uint8 zero", uint8(0), 0, true},
		{"unsupported", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := strconvValue(tc.value)
			if tc.fail {
				if err == nil {
					t.Fatalf("strconvValue(%T)=%d, want error", tc.value, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("strconvValue(%T)=(%d,%v), want %d", tc.value, got, err, tc.want)
			}
		})
	}
}

func TestCovProtocolErrorKeyRedaction(t *testing.T) {
	for _, tc := range []struct {
		err    string
		params map[string]string
		want   string
	}{
		{`unknown parameter "Cipher"`, map[string]string{"cipher": "secret"}, "cipher"},
		{`unknown parameter hidden-secret`, map[string]string{"token-secret": "hidden-secret"}, ""},
		{`invalid cipher value`, map[string]string{"cipher": "secret"}, "cipher"},
		{`unrelated failure`, map[string]string{"cipher": "secret"}, ""},
	} {
		if got := parseProtocolErrorKey(fmt.Errorf("%s", tc.err), tc.params); got != tc.want {
			t.Errorf("key=%q, want %q", got, tc.want)
		}
	}
}

func TestCovServerValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"nil", nil, "configuration must not be nil"},
		{"invalid parameter key", func(c *Config) { c.Servers[0].Params["bad.key"] = "x" }, "invalid protocol parameter key"},
		{"reserved parameter", func(c *Config) { c.Servers[0].Params["port"] = "x" }, "servers[0].port is reserved"},
		{"scalar parameter", func(c *Config) { c.Servers[0].Params["cipher"] = "bad\nvalue" }, "servers[0].cipher"},
		{"scalar public host", func(c *Config) { c.Servers[0].PublicHost = "bad\nhost" }, "public_host"},
		{"wildcard without users", func(c *Config) {
			c.Servers[0].Protocol = "vmess"
			c.Servers[0].Params = nil
			c.Servers[0].Accounts = "*"
		}, "requires at least one configured account"},
		{"invalid login", func(c *Config) { c.Servers[0].Accounts = "bad/login" }, "invalid account login"},
		{"single user multiple", func(c *Config) { c.Servers[0].Protocol = "snell"; c.Servers[0].Accounts = "alice,bob" }, "exactly one account"},
		{"empty bind default", func(c *Config) { c.Servers[0].Bind = "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c *Config
			if tc.mutate != nil {
				c = serverBaseline()
				tc.mutate(c)
			}
			var before []Server
			if c != nil {
				before = append([]Server(nil), c.Servers...)
				before[0].Params = cloneStringParams(c.Servers[0].Params)
			}
			err := validateServers(c)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if e, ok := err.(*cli.Error); !ok || e.Code != CodeConfigInvalid {
				t.Fatalf("error class=%T %v", err, err)
			}
			if c != nil && !reflect.DeepEqual(c.Servers, before) {
				t.Fatal("validation mutated server state")
			}
		})
	}
}

func TestCovServerParseScalarParams(t *testing.T) {
	cfg, err := ktavLoadsIntoConfig(`servers: [{name: edge, protocol: shadowsocks, port: 8388, accounts: alice, flag: true, number: 123, public_host: edge.example}]`)
	if err != nil || len(cfg.Servers) != 1 {
		t.Fatalf("config=%#v error=%v", cfg, err)
	}
	if cfg.Servers[0].Params["flag"] != "true" || cfg.Servers[0].Params["number"] != "123" || cfg.Servers[0].PublicHost != "edge.example" {
		t.Fatalf("server=%#v", cfg.Servers[0])
	}
}

func TestCovServerResolution(t *testing.T) {
	if _, err := (*Config)(nil).ResolveServers(); err == nil || !strings.Contains(err.Error(), "configuration must not be nil") {
		t.Fatalf("nil resolve error=%v", err)
	}
	c := serverBaseline()
	c.Servers[0].Protocol, c.Servers[0].Params, c.Servers[0].Accounts = "vmess", nil, "*"
	c.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}}
	c.Auth = Auth{Login: "legacy", Password: InitPlaceholder}
	resolved, err := c.ResolveServers()
	if err != nil || len(resolved) != 1 || !reflect.DeepEqual(resolved[0].Accounts, []string{"alice", "legacy"}) || resolved[0].Bind != "127.0.0.1" {
		t.Fatalf("resolved=%#v error=%v", resolved, err)
	}
	for _, tc := range []struct {
		name  string
		setup func(*Config)
		want  string
	}{
		{"missing", func(c *Config) { c.Servers[0].Accounts = "missing" }, "does not exist"},
		{"empty wildcard", func(c *Config) { c.Servers[0].Accounts = "*"; c.Accounts = nil; c.Auth = Auth{} }, "resolves to no accounts"},
		{"duplicate", func(c *Config) { c.Servers[0].Accounts = "alice,alice"; c.Auth = Auth{} }, "duplicate account"},
		{"single user wildcard", func(c *Config) {
			c.Servers[0].Protocol = "snell"
			c.Servers[0].Params = nil
			c.Servers[0].Accounts = "*"
		}, "cannot be *"},
		{"invalid spec", func(c *Config) {
			c.Servers[0].Protocol = "vmess"
			c.Servers[0].Params = nil
			c.Servers[0].Accounts = "alice"
			c.Servers[0].Name = "bad/name"
			c.Auth = Auth{}
		}, "servers[0] is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := serverBaseline()
			x.Servers[0].Protocol, x.Servers[0].Params = "vmess", nil
			x.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}}
			tc.setup(x)
			got, err := x.ResolveServers()
			if err == nil || got != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolved=%#v error=%v, want %q", got, err, tc.want)
			}
			if e, ok := err.(*cli.Error); !ok || e.Code != CodeConfigInvalid {
				t.Fatalf("error class=%T %v", err, err)
			}
		})
	}
}

func TestCovServerEndpointHelpers(t *testing.T) {
	if validServerName("") || validServerName(strings.Repeat("a", 33)) || validServerName("bad/name") || !validServerName("A._-9") {
		t.Fatal("server-name boundary mismatch")
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{{"0.0.0.0", "127.0.0.1", true}, {"::", "192.0.2.1", true}, {"192.0.2.1", "192.0.2.1", true}, {"bad", "127.0.0.1", false}} {
		if got := bindsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("bindsOverlap(%q,%q)=%v", tc.a, tc.b, got)
		}
	}
	if !bindsOverlap("127.0.0.1", "") {
		t.Fatal("empty peer bind must mean loopback")
	}
	c := serverBaseline()
	if s, i := c.GetServer("edge"); s == nil || i != 0 {
		t.Fatalf("found=(%v,%d)", s, i)
	}
	if s, i := c.GetServer("absent"); s != nil || i != -1 {
		t.Fatalf("missing=(%v,%d)", s, i)
	}
}
