package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestDoctorServerChecksUseInjectedListen(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "0.0.0.0", Port: 12345, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveServerSpecs(home, cfg); err != nil {
		t.Fatal(err)
	}
	old := doctorListen
	doctorListen = func(string) error { return errors.New("occupied") }
	t.Cleanup(func() { doctorListen = old })
	d := &doctorContext{home: home, cfg: cfg}
	checks := d.checkServers()
	if len(checks) < 2 || !strings.Contains(checks[len(checks)-1].Detail, "binds publicly") || !strings.Contains(checks[len(checks)-1].Detail, "port is taken") {
		t.Fatalf("checks=%+v", checks)
	}
}
