package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestDoctorExplicitDirectAccount(t *testing.T) {
	account := config.Account{Login: "alice", Exit: "direct"}
	d := &doctorContext{cfg: &config.Config{Listen: config.Listen{Bind: "127.0.0.1"}, Accounts: []config.Account{account}}}
	check := d.checkAccount(&account)
	if check.Status != doctorOK || check.Detail != "exit: direct" {
		t.Fatalf("direct account check = %+v", check)
	}
}

func TestDoctorExplicitDirectExposure(t *testing.T) {
	account := config.Account{Login: "alice", Exit: "direct"}
	cases := []struct {
		name   string
		cfg    *config.Config
		status angel.Status
		up     bool
		want   string
	}{
		{name: "configured loopback", cfg: &config.Config{Listen: config.Listen{Bind: "::1"}, Accounts: []config.Account{account}}, want: doctorOK},
		{name: "configured public mixed bind", cfg: &config.Config{Listen: config.Listen{Bind: "0.0.0.0"}, Accounts: []config.Account{account}}, want: doctorWarn},
		{name: "live loopback server", cfg: &config.Config{Listen: config.Listen{Bind: "127.0.0.1"}, Accounts: []config.Account{account}, Servers: []config.Server{{Name: "edge", Bind: "0.0.0.0", Accounts: "*"}}}, status: angel.Status{Listen: "127.0.0.1:1234", Servers: []angel.ServerInfo{{Name: "edge", Listen: "localhost:4321"}}}, up: true, want: doctorOK},
		{name: "live public server", cfg: &config.Config{Listen: config.Listen{Bind: "127.0.0.1"}, Accounts: []config.Account{account}, Servers: []config.Server{{Name: "edge", Bind: "127.0.0.1", Accounts: "alice"}}}, status: angel.Status{Listen: "127.0.0.1:1234", Servers: []angel.ServerInfo{{Name: "edge", Listen: "0.0.0.0:4321"}}}, up: true, want: doctorWarn},
		{name: "unrelated server", cfg: &config.Config{Listen: config.Listen{Bind: "127.0.0.1"}, Accounts: []config.Account{account}, Servers: []config.Server{{Name: "edge", Bind: "0.0.0.0", Accounts: "bob"}}}, want: doctorOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &doctorContext{cfg: tc.cfg, status: tc.status, angelUp: tc.up}
			check := d.checkAccount(&account)
			if check.Status != tc.want {
				t.Fatalf("check = %+v, want status %q", check, tc.want)
			}
			if tc.want == doctorWarn && !strings.Contains(check.Detail, "non-loopback") && !strings.Contains(check.Detail, "public/non-loopback") {
				t.Fatalf("warning lacks exposure detail: %+v", check)
			}
		})
	}
}
