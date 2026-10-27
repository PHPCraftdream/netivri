//go:build integration

package core

import (
	"context"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/testutil"
	mihomoAuth "github.com/metacubex/mihomo/component/auth"
)

func TestSOCKS5PrePostRouting(t *testing.T) {
	pre, preAddr := startChainRecorder(t)
	node, nodeAddr := startChainRecorder(t)
	post, postAddr := startChainRecorder(t)
	backend := echoServer(t)
	inst, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t), Authenticator: aliceAuthenticator(),
		RouteNodes: map[string][]map[string]any{"de": {nodeMap(nodeAddr)}}, AccountRoute: map[string]string{"alice": "de"},
		RoutePre: map[string]string{"de": "socks5://" + preAddr}, RoutePost: map[string]string{"de": "socks5://" + postAddr},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "alice", alicePassword); err != nil {
		t.Fatal(err)
	}
	target := backend
	if got := pre.snapshot(); len(got) != 1 || got[0] != nodeAddr {
		t.Errorf("pre CONNECTs=%v, want selected node %s", got, nodeAddr)
	}
	if got := node.snapshot(); len(got) != 1 || got[0] != postAddr {
		t.Errorf("node CONNECTs=%v, want post hop %s", got, postAddr)
	}
	if got := post.snapshot(); len(got) != 1 || got[0] != target {
		t.Errorf("post CONNECTs=%v, want destination %s", got, target)
	}
}

func TestSOCKS5PreReconfigureKeepsOpenConnection(t *testing.T) {
	pre1, pre1Addr := startChainRecorder(t)
	pre2, pre2Addr := startChainRecorder(t)
	node, nodeAddr := startChainRecorder(t)
	backend := echoServer(t)
	home := testutil.TempHome(t)
	options := func(pre string) Options {
		return Options{
			HomeDir: home, Authenticator: aliceAuthenticator(),
			RouteNodes: map[string][]map[string]any{"de": {nodeMap(nodeAddr)}}, AccountRoute: map[string]string{"alice": "de"},
			RoutePre: map[string]string{"de": "socks5://" + pre},
		}
	}
	// Keep HomeDir stable while changing only the chain configuration.
	o := options(pre1Addr)
	instance, err := Start(context.Background(), o)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer instance.Close()
	address := instance.Addr()
	held, err := openAccountSOCKS5(t, address, "127.0.0.1", backend, "alice", alicePassword)
	if err != nil {
		t.Fatalf("open held connection: %v", err)
	}
	defer held.Close()
	// The SOCKS5 success reply comes before the upstream dial: an echo proves
	// the held connection went through the old chain before the change.
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection before Reconfigure: %v", err)
	}
	if err := instance.Reconfigure(options(pre2Addr)); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	if instance.Addr() != address {
		t.Fatalf("listener changed from %s to %s", address, instance.Addr())
	}
	if err := accountEcho(held); err != nil {
		t.Fatalf("held connection after Reconfigure: %v", err)
	}
	if err := tryDialAccountSOCKS5(t, address, "127.0.0.1", backend, "alice", alicePassword); err != nil {
		t.Fatalf("new connection after Reconfigure: %v", err)
	}
	if got := pre1.snapshot(); len(got) != 1 || got[0] != nodeAddr {
		t.Errorf("old pre CONNECTs = %v, want one to node %s", got, nodeAddr)
	}
	if got := pre2.snapshot(); len(got) != 1 || got[0] != nodeAddr {
		t.Errorf("new pre CONNECTs = %v, want one to node %s", got, nodeAddr)
	}
	if got := node.snapshot(); len(got) != 2 {
		t.Errorf("node CONNECTs = %v, want held and new connections", got)
	}
}

func TestSOCKS5PrePostDirectBypassesAndPostOnly(t *testing.T) {
	post, postAddr := startChainRecorder(t)
	backend := echoServer(t)
	inst, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword}, {User: "bob", Pass: bobPassword},
		}),
		AccountRoute: map[string]string{"alice": "alice-direct", "bob": "bob-direct"},
		RoutePre:     map[string]string{"alice-direct": "none", "bob-direct": "none"},
		RoutePost:    map[string]string{"alice-direct": "socks5://" + postAddr, "bob-direct": "none"},
		GlobalPost:   "socks5://" + postAddr,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "bob", bobPassword); err != nil {
		t.Fatal(err)
	}
	if got := post.snapshot(); len(got) != 0 {
		t.Errorf("explicit direct account used post hop: %v", got)
	}
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "alice", alicePassword); err != nil {
		t.Fatal(err)
	}
	if got := post.snapshot(); len(got) != 1 || got[0] != backend {
		t.Errorf("account post recorder=%v, want one direct-account destination %s", got, backend)
	}
}
