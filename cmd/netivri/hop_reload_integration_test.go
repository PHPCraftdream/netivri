//go:build integration

package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	hopkey "github.com/PHPCraftdream/netivri/pkg/hopkit/hop"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func TestHopReloadCarriesQuarantinedHealthAcrossPoolEdits(t *testing.T) {
	reserveLoopback := func() string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve loopback address: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln.Addr().String()
	}

	home := t.TempDir()
	live := &liveAngel{opts: upOptions{Home: home}}
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Provider: "p", ExitCountry: "DE"}}
	nodeAddr := reserveLoopback()
	nodeHost, nodePortText, err := net.SplitHostPort(nodeAddr)
	if err != nil {
		t.Fatalf("split node address: %v", err)
	}
	nodePort, err := strconv.Atoi(nodePortText)
	if err != nil {
		t.Fatalf("parse node port: %v", err)
	}
	snapshots := map[string]*subscription.Snapshot{"p": {Nodes: []subscription.Node{{
		ID: "local", Name: "local", Type: "socks5", Server: nodeHost, Port: nodePort, Country: "DE",
		Raw: map[string]any{"name": "local", "type": "socks5", "server": nodeHost, "port": nodePort},
	}}}}
	optionsFor := func(hops, providerPre config.HopList) core.Options {
		account := cfg.Accounts[0]
		account.PostSOCKS5 = hops
		subs := []config.Subscription{{Name: "p", PreSOCKS5: providerPre}}
		routes, err := buildAccountRoutePoolHops([]config.Account{account}, snapshots, nil, nil, hopsOf(subs))
		if err != nil {
			t.Fatalf("build account routes: %v", err)
		}
		o, _, err := live.routeOptionsWithLoggerChecked(desiredState{cfg: cfg, bind: "127.0.0.1", pool: routes}, nil, nil)
		if err != nil {
			t.Fatalf("build core options: %v", err)
		}
		// No dials or probes are needed to exercise lifecycle carry.
		o.Warm = nil
		return o
	}
	firstURL := "socks5://" + reserveLoopback()
	secondURL := "socks5://" + reserveLoopback()
	thirdURL := "socks5://" + reserveLoopback()

	providerPre := "socks5://" + reserveLoopback()
	initial := optionsFor(config.HopList{firstURL, secondURL}, config.HopList{providerPre})
	route := initial.AccountRoute["alice"]
	if route == "" || len(initial.RoutePostPool[route]) != 2 {
		t.Fatalf("initial post pool was not mapped: route=%q pools=%v", route, initial.RoutePostPool)
	}
	inst, err := core.Start(context.Background(), initial)
	if err != nil {
		t.Fatalf("core.Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	addr := inst.Addr()

	key, ok := hopkey.Key(firstURL)
	if !ok {
		t.Fatal("hop.Key rejected test URL")
	}
	var metadata *core.HopPoolSnapshot
	for _, snapshot := range inst.SnapshotHopPools() {
		if snapshot.Role == "post" && snapshot.RouteKey == route && snapshot.Key == key {
			copy := snapshot
			metadata = &copy
			break
		}
	}
	if metadata == nil {
		t.Fatalf("core snapshot omitted configured post hop %q: %+v", key, inst.SnapshotHopPools())
	}
	state := core.HopPoolState{
		Role: metadata.Role, RouteKey: metadata.RouteKey, PoolIdentity: metadata.PoolIdentity,
		Entries: []hoppool.Entry{{Key: key, Trips: 2, NextProbe: time.Now()}},
	}
	if err := inst.RestoreHopPools([]core.HopPoolState{state}); err != nil {
		t.Fatalf("RestoreHopPools: %v", err)
	}

	assertCarried := func(o core.Options, want config.HopList) {
		t.Helper()
		if o.AccountRoute["alice"] != route {
			t.Fatalf("pool edit changed route key: got %q, want %q", o.AccountRoute["alice"], route)
		}
		got := o.RoutePostPool[route]
		if len(got) != len(want) {
			t.Fatalf("post pool = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("post pool = %v, want %v", got, want)
			}
		}
		if o.SiteBinder != initial.SiteBinder {
			t.Fatal("reconfigured options replaced the shared site binder")
		}
		if err := inst.Reconfigure(o); err != nil {
			t.Fatalf("Instance.Reconfigure: %v", err)
		}
		if inst.Addr() != addr {
			t.Fatalf("listener moved from %q to %q", addr, inst.Addr())
		}
		for _, snapshot := range inst.SnapshotHopPools() {
			if snapshot.Role == "post" && snapshot.RouteKey == route && snapshot.Key == key {
				if snapshot.State == "healthy" || snapshot.Trips != 2 {
					t.Fatalf("carried hop state = %+v, want nonhealthy with trips=2", snapshot)
				}
				return
			}
		}
		t.Fatalf("reconfigured core snapshot omitted carried hop %q: %+v", key, inst.SnapshotHopPools())
	}

	// Reorder, add a third candidate, then remove the other original candidate.
	assertCarried(optionsFor(config.HopList{secondURL, firstURL}, nil), config.HopList{secondURL, firstURL})
	assertCarried(optionsFor(config.HopList{secondURL, firstURL, thirdURL}, nil), config.HopList{secondURL, firstURL, thirdURL})
	assertCarried(optionsFor(config.HopList{firstURL, thirdURL}, nil), config.HopList{firstURL, thirdURL})
}
