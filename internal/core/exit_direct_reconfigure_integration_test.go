//go:build integration

package core

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func TestReconfigureAccountRouteToDirectAndBack(t *testing.T) {
	recorder, recorderAddr := startChainRecorder(t)
	backend, _ := countEchoServer(t)
	host, _, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("split backend address: %v", err)
	}
	port, err := strconv.Atoi(mustPort(t, backend))
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}
	domain := host
	options := func(direct bool) Options {
		o := Options{
			HomeDir:       testutil.TempHome(t),
			Authenticator: aliceAuthenticator(),
			RouteNodes:    map[string][]map[string]any{"route": {nodeMap(recorderAddr)}},
			AccountRoute:  map[string]string{"alice": "route", "keeper": "route"},
		}
		if direct {
			o.AccountRoute["alice"] = "direct"
			o.RoutePre = map[string]string{"direct": "none"}
			o.RoutePost = map[string]string{"direct": "none"}
		}
		return o
	}
	instance, err := Start(context.Background(), options(false))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	held, err := openAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open held routed connection: %v", err)
	}
	defer held.Close()
	if err := accountEcho(held); err != nil {
		t.Fatalf("initial routed echo: %v", err)
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if got := recorder.snapshot(); len(got) != 1 || got[0] != target {
		t.Fatalf("initial recorder targets = %v, want [%s]", got, target)
	}

	if err := instance.Reconfigure(options(true)); err != nil {
		t.Fatalf("reconfigure to direct: %v", err)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection after direct change: %v", err)
	}
	direct, err := openAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open direct connection: %v", err)
	}
	if err := accountEcho(direct); err != nil {
		_ = direct.Close()
		t.Fatalf("direct echo: %v", err)
	}
	_ = direct.Close()
	if got := recorder.snapshot(); len(got) != 1 || got[0] != target {
		t.Fatalf("recorder targets after direct connection = %v, want only [%s]", got, target)
	}

	if err := instance.Reconfigure(options(false)); err != nil {
		t.Fatalf("restore routed account: %v", err)
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection after route restoration: %v", err)
	}
	routed, err := openAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open restored routed connection: %v", err)
	}
	if err := accountEcho(routed); err != nil {
		_ = routed.Close()
		t.Fatalf("restored routed echo: %v", err)
	}
	_ = routed.Close()
	if got := recorder.snapshot(); len(got) != 2 || got[0] != target || got[1] != target {
		t.Fatalf("recorder targets after route restoration = %v, want [%s %s]", got, target, target)
	}
}
