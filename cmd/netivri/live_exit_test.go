package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestReloadExplicitDirectFlipsInPlace(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"}, config.Account{Login: "bob", Provider: "p", ExitCountry: "DE"})
	running := f.script.inst(0)
	running.setInPlace(nil)
	ctx := t.Context()
	original := running
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Provider: "p", ExitCountry: "DE"}, config.Account{Login: "bob", Password: f.hash, Exit: "direct"})
	f.l.reload(ctx)
	if f.script.calls() != 1 || original.isClosed() || f.reloads() != 1 {
		t.Fatalf("route to direct restarted: starts=%d closed=%v reloads=%d", f.script.calls(), original.isClosed(), f.reloads())
	}
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Provider: "p", ExitCountry: "DE"}, config.Account{Login: "bob", Password: f.hash, Provider: "p", ExitCountry: "DE"})
	f.l.reload(ctx)
	if f.script.calls() != 1 || original.isClosed() || f.reloads() != 2 {
		t.Fatalf("direct to route restarted: starts=%d closed=%v reloads=%d", f.script.calls(), original.isClosed(), f.reloads())
	}
}

func TestReloadFirstDirectRouteAppearsAndDisappearsRestarts(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice"})
	running := f.script.inst(0)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct"})
	f.l.reload(t.Context())
	if f.script.calls() != 2 || !running.isClosed() {
		t.Fatalf("first direct exit: starts=%d old closed=%v", f.script.calls(), running.isClosed())
	}
	running = f.script.inst(1)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash})
	f.l.reload(t.Context())
	if f.script.calls() != 3 || !running.isClosed() {
		t.Fatalf("last route disappeared: starts=%d old closed=%v", f.script.calls(), running.isClosed())
	}
}

func TestReloadMultipleAccountsRouteAndDirectInPlace(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"}, config.Account{Login: "bob", Provider: "p", ExitCountry: "DE"})
	running := f.script.inst(0)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct"}, config.Account{Login: "bob", Password: f.hash, Exit: "direct"})
	f.l.reload(t.Context())
	if f.script.calls() != 2 || !running.isClosed() {
		t.Fatalf("last routes disappeared: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}

	running = f.script.inst(1)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct"}, config.Account{Login: "bob", Password: f.hash, Provider: "p", ExitCountry: "DE"})
	f.l.reload(t.Context())
	if f.script.calls() != 3 || !running.isClosed() {
		t.Fatalf("first route appeared: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}

	running = f.script.inst(2)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct"}, config.Account{Login: "bob", Password: f.hash, Exit: "direct"})
	f.l.reload(t.Context())
	if f.script.calls() != 4 || !running.isClosed() {
		t.Fatalf("last route disappeared again: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}
}

func TestReloadDirectOnlyPostChangeRestarts(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Exit: "direct"})
	running := f.script.inst(0)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct", PostSOCKS5: "socks5://post.example:1080"})
	f.l.reload(t.Context())
	if f.script.calls() != 2 || !running.isClosed() {
		t.Fatalf("direct-only post change: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}
}
