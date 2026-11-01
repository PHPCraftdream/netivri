package config

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestRound1NonTrialNonTLSDefaultPreservesConfigError(t *testing.T) {
	cases := []struct {
		name, protocol string
		params         map[string]string
	}{{"snell", "snell", nil}, {"legacy shadowsocks", "shadowsocks", map[string]string{"cipher": "aes-128-gcm"}}, {"UDP hysteria2", "hysteria2", nil}}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}}
			cfg.Servers = []Server{{Name: "default", Protocol: tc.protocol, Bind: "127.0.0.1", Port: 39110 + i, Accounts: "alice", Default: true, Params: tc.params}}
			err := cfg.Validate()
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "CONFIG_INVALID" || ce.Message != "servers[0].default default requires a TLS-sniffable protocol" || ce.Hint != "fix the value in config.ktav; see netivri help config" || ce.Exit != cli.ExitUsage {
				t.Fatalf("Validate error = %#v", err)
			}
		})
	}
}

func TestRound1TrialDefaultsUseSharedValidator(t *testing.T) {
	for i, tc := range []struct {
		protocol string
		params   map[string]string
	}{{"shadowsocks", map[string]string{"cipher": "2022-blake3-aes-128-gcm"}}, {"vmess", nil}} {
		cfg := Default()
		cfg.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}}
		port := 39120 + i
		cfg.Servers = []Server{{Name: "trial", Protocol: tc.protocol, Bind: "127.0.0.1", Port: port, Accounts: "alice", Default: true, Params: tc.params}}
		err := cfg.Validate()
		var got *cli.Error
		if !errors.As(err, &got) {
			t.Fatalf("Validate error = %v", err)
		}
		wantErr := servers.ValidateSharedPort([]servers.Spec{{Name: "trial", Protocol: tc.protocol, Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Default: true, Params: tc.params}})
		var want *cli.Error
		if !errors.As(wantErr, &want) || got.Code != want.Code || got.Message != want.Message || got.Hint != want.Hint || got.Exit != want.Exit || got.Code != "SERVER_PORT_AMBIGUOUS" || got.Hint == "" {
			t.Fatalf("Validate error = %#v, shared validator error = %#v", got, want)
		}
	}
}

func TestValidateTrialServerEligibilityAndGroups(t *testing.T) {
	base := Default()
	base.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}, {Login: "bob", Password: InitPlaceholder}}
	cases := []struct {
		name     string
		servers  []Server
		wantCode string
	}{
		{"2022 may share", []Server{{Name: "s1", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39001, Accounts: "alice", Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}}, {Name: "s2", Protocol: "vmess", Bind: "127.0.0.1", Port: 39001, Accounts: "bob"}}, ""},
		{"legacy ss exclusive", []Server{{Name: "s1", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39002, Accounts: "alice", Params: map[string]string{"cipher": "aes-128-gcm"}}, {Name: "s2", Protocol: "vmess", Bind: "127.0.0.1", Port: 39002, Accounts: "bob"}}, "CONFIG_INVALID"},
		{"snell exclusive", []Server{{Name: "s1", Protocol: "snell", Bind: "127.0.0.1", Port: 39003, Accounts: "alice"}, {Name: "s2", Protocol: "vmess", Bind: "127.0.0.1", Port: 39003, Accounts: "bob"}}, "CONFIG_INVALID"},
		{"TLS then trial", []Server{{Name: "tls", Protocol: "trojan", Bind: "127.0.0.1", Port: 39005, Accounts: "alice", Params: map[string]string{"sni": "tls.example"}}, {Name: "trial", Protocol: "vmess", Bind: "127.0.0.1", Port: 39005, Accounts: "bob"}}, ""},
		{"trial then TLS", []Server{{Name: "trial", Protocol: "vmess", Bind: "127.0.0.1", Port: 39006, Accounts: "alice"}, {Name: "tls", Protocol: "trojan", Bind: "127.0.0.1", Port: 39006, Accounts: "bob", Params: map[string]string{"sni": "tls.example"}}}, ""},
		{"TCP and UDP same port", []Server{{Name: "tcp", Protocol: "vmess", Bind: "127.0.0.1", Port: 39007, Accounts: "alice"}, {Name: "udp", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 39007, Accounts: "bob"}}, ""},
		{"standalone trial default rejected", []Server{{Name: "s1", Protocol: "vmess", Bind: "127.0.0.1", Port: 39004, Accounts: "alice", Default: true}}, "SERVER_PORT_AMBIGUOUS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Servers = tc.servers
			err := cfg.Validate()
			if tc.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != tc.wantCode {
				t.Fatalf("Validate error=%v, want %s", err, tc.wantCode)
			}
		})
	}
}

func TestValidateTrialWildcardCountsWholeEndpoint(t *testing.T) {
	cfg := Default()
	cfg.Accounts = make([]Account, 17)
	for i := range cfg.Accounts {
		cfg.Accounts[i] = Account{Login: "user" + string(rune('a'+i)), Password: InitPlaceholder}
	}
	cfg.Servers = []Server{{Name: "one", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39011, Accounts: "usera", Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}}}
	for i := 0; i < 15; i++ {
		cfg.Servers = append(cfg.Servers, Server{Name: "member" + string(rune('a'+i)), Protocol: "vmess", Bind: "127.0.0.1", Port: 39011, Accounts: "usera"})
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("16 trial members rejected: %v", err)
	}
	cfg.Servers = append(cfg.Servers, Server{Name: "memberp", Protocol: "vmess", Bind: "127.0.0.1", Port: 39011, Accounts: "usera"})
	var ce *cli.Error
	if err := cfg.Validate(); !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("17 trial members error=%v, want group limit", err)
	}
}

func TestValidateTrialVMessUserLimitAndMixedTLS(t *testing.T) {
	cfg := Default()
	cfg.Accounts = make([]Account, 1025)
	for i := range cfg.Accounts {
		cfg.Accounts[i] = Account{Login: "u" + strconv.Itoa(i), Password: InitPlaceholder}
	}
	accounts := func(n int) string {
		logins := make([]string, n)
		for i := range logins {
			logins[i] = cfg.Accounts[i].Login
		}
		return strings.Join(logins, ",")
	}
	for _, n := range []int{1024, 1025} {
		cfg.Servers = []Server{{Name: "vm", Protocol: "vmess", Bind: "127.0.0.1", Port: 39012, Accounts: accounts(n)}}
		err := cfg.Validate()
		if n == 1024 && err != nil {
			t.Fatalf("1024 VMess users rejected: %v", err)
		}
		if n == 1025 {
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
				t.Fatalf("1025 VMess users error=%v", err)
			}
		}
	}
}
