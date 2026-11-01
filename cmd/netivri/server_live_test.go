package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

type serverInfoFake struct{ infos []servers.Info }

func (f serverInfoFake) Servers() []servers.Info { return f.infos }

func TestServerFingerprintIncludesSecretRotation(t *testing.T) {
	a := servers.Spec{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 1234, Accounts: []string{"alice"}, Users: servers.UserSecrets{"alice": {"password": "secret-one"}}}
	b := a.Clone()
	b.Users["alice"]["password"] = "secret-two"
	if serverFingerprint([]servers.Spec{a}) == serverFingerprint([]servers.Spec{b}) {
		t.Fatal("rotation did not change fingerprint")
	}
	if strings.Contains(serverFingerprint([]servers.Spec{a}), "secret-one") {
		t.Fatal("fingerprint exposed secret")
	}
}

func TestServerOptionsAndTransactionalFailure(t *testing.T) {
	f := newReloadFixture(t, upOptions{})
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, AllowedClients: "10.0.0.0/8"}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 1234, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
		t.Fatal(err)
	}
	d, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.l.start(t.Context(), d, nil, nil); err != nil {
		t.Fatal(err)
	}
	o := f.script.call(1)
	if len(o.Servers) != 1 || len(o.Servers[0].Users["alice"]) == 0 || len(o.AccountAllowedClients["alice"]) != 1 {
		t.Fatalf("server options incomplete")
	}
	broken := d
	broken.servers = []servers.Spec{d.servers[0].Clone()}
	broken.servers[0].Port++
	f.script.inst(1).reconfErr = errors.New("stage failed")
	if !f.l.reconfigure(broken) || f.script.calls() != 2 || f.script.inst(1).isClosed() {
		t.Fatal("failed stage restarted/closed old core")
	}
	// A staged server change with routes present must use the same in-place
	// transactional boundary and preserve the prior core on stage failure.
	f.l.mu.Lock()
	f.l.applied.pool.accountRoutes = map[string]string{"alice": "route"}
	f.l.applied.pool.nodes = map[string][]map[string]any{"route": {{"name": "n", "type": "socks5", "server": "127.0.0.1", "port": 1}}}
	broken.pool.accountRoutes = map[string]string{"alice": "route"}
	broken.pool.nodes = f.l.applied.pool.nodes
	f.l.mu.Unlock()
	if !f.l.reconfigure(broken) || f.script.calls() != 2 || f.script.inst(1).isClosed() {
		t.Fatal("routed server stage failure restarted/closed the old core")
	}
}

func TestServerDiagnosticsDeltaAndRedaction(t *testing.T) {
	var info, warn []string
	l := &liveAngel{logf: func(f string, a ...any) { info = append(info, fmt.Sprintf(f, a...)) }, warnf: func(f string, a ...any) { warn = append(warn, fmt.Sprintf(f, a...)) }}
	l.flushServerDiagnostics(serverInfoFake{[]servers.Info{{Name: "edge", DroppedClients: 99}}})
	l.flushServerDiagnostics(serverInfoFake{[]servers.Info{{Name: "edge", DroppedClients: 199}}})
	l.flushServerDiagnostics(serverInfoFake{[]servers.Info{{Name: "edge", DroppedClients: 4}}})
	if len(info) != 2 || len(warn) != 1 || !strings.Contains(warn[0], "100 clients dropped") || !strings.Contains(info[1], "4 clients dropped") {
		t.Fatalf("info=%v warn=%v", info, warn)
	}
	l.logServerDelta(nil, []servers.Spec{{Name: "edge", Protocol: "ss", Bind: "127.0.0.1", Port: 3, Accounts: []string{"alice"}, Users: servers.UserSecrets{"alice": {"password": "DO_NOT_LOG"}}}})
	if strings.Contains(strings.Join(append(info, warn...), " "), "DO_NOT_LOG") {
		t.Fatal("logs exposed secret")
	}
}
