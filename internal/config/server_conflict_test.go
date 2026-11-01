package config

import (
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestValidateServersSharedPortErrorsHaveConflictCodes(t *testing.T) {
	cfg := Default()
	cfg.Servers = []Server{
		{Name: "one", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Params: map[string]string{"sni": "same.example"}},
		{Name: "two", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "same.example"}},
	}
	err := cfg.Validate()
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Exit != cli.ExitConflict {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateServersSharedPortAllowsDistinctSNI(t *testing.T) {
	cfg := Default()
	cfg.Servers = []Server{
		{Name: "one", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Params: map[string]string{"sni": "one.example"}},
		{Name: "two", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "two.example"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("distinct SNI rejected: %v", err)
	}
}

func TestValidateServersSharedPortRound3NoSNIValidation(t *testing.T) {
	plain := Server{Name: "plain", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice"}
	anyTLS := Server{Name: "anytls", Protocol: "anytls", Bind: "127.0.0.1", Port: 12345, Accounts: "bob", Params: map[string]string{"sni": "any.example"}}

	tests := []struct {
		name     string
		servers  []Server
		wantCode string
		wantExit int
		wantHint string
	}{
		{
			name:     "no SNI is ambiguous",
			servers:  []Server{plain, anyTLS},
			wantCode: "SERVER_PORT_AMBIGUOUS",
			wantExit: cli.ExitConflict,
			wantHint: "set sni on plain or mark it as the default",
		},
		{
			name:    "default allows no SNI",
			servers: []Server{{Name: plain.Name, Protocol: plain.Protocol, Bind: plain.Bind, Port: plain.Port, Accounts: plain.Accounts, Default: true}, anyTLS},
		},
		{
			name:    "explicit SNI allows sharing",
			servers: []Server{{Name: plain.Name, Protocol: plain.Protocol, Bind: plain.Bind, Port: plain.Port, Accounts: plain.Accounts, Params: map[string]string{"sni": "trojan.example"}}, anyTLS},
		},
		{
			name:    "solo no SNI is valid",
			servers: []Server{plain},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Accounts = []Account{{Login: "alice", Password: InitPlaceholder}, {Login: "bob", Password: InitPlaceholder}}
			cfg.Servers = tt.servers
			err := cfg.Validate()
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != tt.wantCode || ce.Exit != tt.wantExit || ce.Hint != tt.wantHint {
				t.Fatalf("Validate() error = %v, want code %q, exit %d, hint %q", err, tt.wantCode, tt.wantExit, tt.wantHint)
			}
		})
	}
}

func TestServerDefaultRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Servers = []Server{{Name: "one", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", Default: true, Params: map[string]string{"sni": "one.example"}}}
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ktavLoadsIntoConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != 1 || !loaded.Servers[0].Default {
		t.Fatalf("server default did not round trip: %#v", loaded.Servers)
	}
}
