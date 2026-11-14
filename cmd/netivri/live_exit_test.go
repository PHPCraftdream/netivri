package main

import (
	"reflect"
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

func TestReloadDirectOnlyPostChangeReconfiguresInPlace(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Exit: "direct"})
	running := f.script.inst(0)
	running.setInPlace(nil)
	f.writeConfig(t, config.Account{Login: "alice", Password: f.hash, Exit: "direct", PostSOCKS5: config.HopList{"socks5://post.example:1080"}})
	f.l.reload(t.Context())
	if f.script.calls() != 1 || running.isClosed() {
		t.Fatalf("direct-only post change restarted: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}
	calls := running.reconfigured()
	if len(calls) != 1 {
		t.Fatalf("Reconfigure calls=%d, want 1", len(calls))
	}
	key := calls[0].AccountRoute["alice"]
	if key == "" || calls[0].RoutePost[key] != "socks5://post.example:1080" {
		t.Fatalf("direct route/post = %q/%v", key, calls[0].RoutePost)
	}
	if len(calls[0].RouteNodes) != 0 {
		t.Fatalf("direct-only Reconfigure contains route nodes: %v", calls[0].RouteNodes)
	}
}

func TestReloadReconfiguresDirectOnlyPostPoolEditsInPlace(t *testing.T) {
	first := config.HopList{"socks5://one.example:1080", "http://two.example:8080"}
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Exit: "direct", PostSOCKS5: first})
	running := f.script.inst(0)
	running.setInPlace(nil)
	cfg, err := config.Load(config.ConfigPath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 1 || running.isClosed() {
		t.Fatalf("unchanged direct pool changed lifecycle: starts=%d closed=%v", f.script.calls(), running.isClosed())
	}
	if len(running.reconfigured()) != 0 {
		t.Fatal("unchanged direct pool attempted Reconfigure")
	}

	second := config.HopList{"http://two.example:8080", "socks5://one.example:1080"}
	third := config.HopList{"http://two.example:8080", "socks5://one.example:1080", "socks5://three.example:1080"}
	fourth := config.HopList{"socks5://three.example:1080", "http://two.example:8080"}
	for i, hops := range []config.HopList{second, third, fourth} {
		cfg.Accounts[0].PostSOCKS5 = hops
		if err := config.Save(config.ConfigPath(f.home), cfg); err != nil {
			t.Fatal(err)
		}
		f.l.reload(t.Context())
		calls := running.reconfigured()
		if f.script.calls() != 1 || running.isClosed() || len(calls) != i+1 {
			t.Fatalf("direct pool edit %d changed lifecycle: starts=%d closed=%v reconfigures=%d", i+1, f.script.calls(), running.isClosed(), len(calls))
		}
		key := calls[i].AccountRoute["alice"]
		if key == "" {
			t.Fatalf("direct pool edit %d lost AccountRoute", i+1)
		}
		if i > 0 && key != calls[0].AccountRoute["alice"] {
			t.Fatalf("direct pool edit %d changed route key: got %q, want %q", i+1, key, calls[0].AccountRoute["alice"])
		}
		if !reflect.DeepEqual(calls[i].RoutePostPool[key], []string(hops)) {
			t.Fatalf("direct pool edit %d RoutePostPool = %v, want %v", i+1, calls[i].RoutePostPool[key], hops)
		}
		if len(calls[i].RouteNodes) != 0 {
			t.Fatalf("direct pool edit %d contains route nodes: %v", i+1, calls[i].RouteNodes)
		}
	}
}
