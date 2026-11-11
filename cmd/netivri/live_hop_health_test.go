package main

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/hopstate"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

type liveHopCoreFake struct {
	*fakeCore
	snapshots  []core.HopPoolSnapshot
	states     []core.HopPoolState
	restored   []core.HopPoolState
	afterClose atomic.Bool
}

func (f *liveHopCoreFake) SnapshotHopPools() []core.HopPoolSnapshot {
	f.afterClose.Store(f.isClosed())
	return append([]core.HopPoolSnapshot(nil), f.snapshots...)
}

func (f *liveHopCoreFake) ExportHopPools() []core.HopPoolState {
	out := append([]core.HopPoolState(nil), f.states...)
	for i := range out {
		out[i].Entries = append([]hoppool.Entry(nil), out[i].Entries...)
	}
	return out
}

func (f *liveHopCoreFake) RestoreHopPools(states []core.HopPoolState) error {
	f.restored = append([]core.HopPoolState(nil), states...)
	return nil
}

func TestLiveHopLifecycleSharesBinderAndLoadsThenFlushes(t *testing.T) {
	home := t.TempDir()
	l := newLiveAngel(upOptions{Home: home}, func(string, ...any) {}, func() {})

	first, second := core.Options{}, core.Options{}
	l.configureHopOptions(&first)
	l.configureHopOptions(&second)
	binder, health := l.hopServices()
	if binder == nil || first.SiteBinder != second.SiteBinder || first.SiteBinder != binder {
		t.Fatal("options did not share the live site's binder")
	}
	if health != nil {
		t.Fatal("option construction eagerly created the health worker")
	}

	key, identity := hopHealthTestKey('a'), hopHealthTestKey('b')
	seed := hopstate.NewStore(hopHealthPath(home, "pre"))
	if err := seed.Save([]hoppool.Entry{{Key: key, Trips: 3, NextProbe: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	startOptions := core.Options{}
	health = l.prepareHopStart(&startOptions, false)
	if startOptions.SiteBinder != binder || startOptions.OnHopEvent == nil {
		t.Fatal("startup options omitted the shared binder or event hook")
	}
	fake := &liveHopCoreFake{
		fakeCore:  &fakeCore{addr: net.JoinHostPort("127.0.0.1", "12345"), inPlace: true},
		snapshots: []core.HopPoolSnapshot{{Role: "pre", RouteKey: "opaque-route", PoolIdentity: identity, Key: key, State: "healthy"}},
		states:    []core.HopPoolState{{Role: "pre", RouteKey: "opaque-route", PoolIdentity: identity, Entries: []hoppool.Entry{{Key: key, Trips: 4, NextProbe: time.Now().UTC()}}}},
	}
	l.attachHopHealth(fake)
	if len(fake.restored) != 1 || len(fake.restored[0].Entries) != 1 || fake.restored[0].Entries[0].Trips != 3 {
		t.Fatalf("startup did not load hop state: %#v", fake.restored)
	}

	// Exercise the in-place reload path itself: it builds options with the same
	// binder, adopts the new hook only after Reconfigure succeeds, and keeps the
	// current core attached for clean-stop persistence.
	l.mu.Lock()
	l.inst, l.applied, l.startOpts = fake, testDesired(), startOptions
	l.mu.Unlock()
	d := testDesired()
	d.pool.nodes["p~DE"][0]["server"] = "192.0.2.2"
	if !l.reconfigureSince(d, 0) {
		t.Fatal("route reload was not applied in place")
	}
	reconfigurations := fake.reconfigured()
	if len(reconfigurations) != 1 || reconfigurations[0].SiteBinder != binder || reconfigurations[0].OnHopEvent == nil {
		t.Fatalf("in-place options did not reuse hop services: %#v", reconfigurations)
	}
	reconfigurations[0].OnHopEvent(core.HopEvent{Role: "pre", RouteKey: "opaque-route", Key: key, Kind: core.HopEventQuarantined, Trips: 4})
	health.mu.Lock()
	activeHook, pendingHook := health.activeHook, health.pendingHook
	health.mu.Unlock()
	if activeHook == 0 || pendingHook != 0 {
		t.Fatalf("successful reconfigure hook not activated: active=%d pending=%d", activeHook, pendingHook)
	}

	l.mu.Lock()
	l.inst = fake
	l.mu.Unlock()
	l.close() // clean-stop flushes before closing the still-live fake core
	rows, err := hopstate.NewStore(hopHealthPath(home, "pre")).Load()
	if err != nil || len(rows) != 1 || rows[0].Trips != 4 {
		t.Fatalf("clean-stop health flush: entries=%#v err=%v", rows, err)
	}
	if fake.afterClose.Load() || !fake.isClosed() {
		t.Fatal("live close did not flush hop state before closing the core")
	}
}
