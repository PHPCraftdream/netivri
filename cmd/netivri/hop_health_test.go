package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/hopstate"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

type fakeHopHealthCore struct {
	mu         sync.Mutex
	snapshots  []core.HopPoolSnapshot
	exported   []core.HopPoolState
	restored   [][]core.HopPoolState
	restoreErr error
	calls      int
}

func (f *fakeHopHealthCore) SnapshotHopPools() []core.HopPoolSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]core.HopPoolSnapshot(nil), f.snapshots...)
}
func (f *fakeHopHealthCore) ExportHopPools() []core.HopPoolState {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := append([]core.HopPoolState(nil), f.exported...)
	for i := range out {
		out[i].Entries = append([]hoppool.Entry(nil), out[i].Entries...)
	}
	return out
}
func (f *fakeHopHealthCore) RestoreHopPools(states []core.HopPoolState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	copyStates := append([]core.HopPoolState(nil), states...)
	for i := range copyStates {
		copyStates[i].Entries = append([]hoppool.Entry(nil), states[i].Entries...)
	}
	f.restored = append(f.restored, copyStates)
	return f.restoreErr
}
func (f *fakeHopHealthCore) setSnapshots(states ...core.HopPoolSnapshot) {
	f.mu.Lock()
	f.snapshots = append([]core.HopPoolSnapshot(nil), states...)
	f.mu.Unlock()
}

type fakeHopHealthStore struct {
	mu      sync.Mutex
	entries []hoppool.Entry
	loadErr error
	saves   int
}

func (s *fakeHopHealthStore) Load() ([]hoppool.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hoppool.Entry(nil), s.entries...), s.loadErr
}
func (s *fakeHopHealthStore) Save(entries []hoppool.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.entries = append([]hoppool.Entry(nil), entries...)
	return nil
}
func (s *fakeHopHealthStore) snapshot() ([]hoppool.Entry, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hoppool.Entry(nil), s.entries...), s.saves
}

type hopTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *hopTestClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *hopTestClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type hopLogRecorder struct {
	mu           sync.Mutex
	infos, warns []string
}

func (r *hopLogRecorder) info(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, fmt.Sprintf(format, args...))
}
func (r *hopLogRecorder) warn(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warns = append(r.warns, fmt.Sprintf(format, args...))
}
func (r *hopLogRecorder) get() (infos, warns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.infos...), append([]string(nil), r.warns...)
}

func hopHealthTestKey(c byte) string { return strings.Repeat(string(c), 64) }
func hopHealthSnapshot(role, route, identity, key, state string, trips int) core.HopPoolSnapshot {
	return core.HopPoolSnapshot{Role: role, RouteKey: route, PoolIdentity: identity, Key: key, State: state, Trips: trips}
}
func newHopHealthTestHarness(clock *hopTestClock, ticks <-chan time.Time) (*hopHealth, *fakeHopHealthStore, *fakeHopHealthStore, *hopLogRecorder) {
	pre, post, logs := &fakeHopHealthStore{}, &fakeHopHealthStore{}, &hopLogRecorder{}
	stores := map[string]hopHealthStore{"pre": pre, "post": post}
	h := newHopHealthWithMode(stores, logs.info, logs.warn, clock.Now, ticks, true)
	return h, pre, post, logs
}

func TestHopHealthLoadErrorWarnsOnceWithoutErrorText(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(100, 0)}
	pre, post, logs := &fakeHopHealthStore{loadErr: errors.New("secret-url-password")}, &fakeHopHealthStore{}, &hopLogRecorder{}
	h := newHopHealthWithMode(map[string]hopHealthStore{"pre": pre, "post": post}, nil, logs.warn, clock.Now, nil, true)
	for i := 0; i < 2; i++ {
		h.Hook()
		h.Attach(&fakeHopHealthCore{})
	}
	h.Close()
	_, warns := logs.get()
	if !sameHopLines(warns, []string{"hop state not loaded"}) {
		t.Fatalf("warnings = %q", warns)
	}
}

func TestHopHealthRestoreUsesCurrentRoleRouteIdentityKeys(t *testing.T) {
	key, identity := hopHealthTestKey('a'), hopHealthTestKey('b')
	snapshots := []core.HopPoolSnapshot{
		hopHealthSnapshot("pre", "route-a", identity, key, "healthy", 0),
		hopHealthSnapshot("post", "route-a", identity, key, "healthy", 0),
		hopHealthSnapshot("pre", "route-a", identity, hopHealthTestKey('c'), "healthy", 0),
		hopHealthSnapshot("pre", "route-a", identity, "invalid-key", "healthy", 0),
	}
	stored := map[string]map[string]hoppool.Entry{
		"pre":  {key: {Key: key, Trips: 1}, hopHealthTestKey('d'): {Key: hopHealthTestKey('d'), Trips: 9}},
		"post": {key: {Key: key, Trips: 3}},
	}
	states := hopHealthRestoreStates(snapshots, stored)
	if len(states) != 2 || states[0].Role != "post" || states[1].Role != "pre" {
		t.Fatalf("states = %#v", states)
	}
	if states[0].Entries[0].Trips != 3 || states[1].Entries[0].Trips != 1 {
		t.Fatalf("role collision: %#v", states)
	}
	for _, state := range states {
		if len(state.Entries) != 1 || state.Entries[0].Key != key {
			t.Fatalf("invalid/removed key restored: %#v", state)
		}
	}
}

func TestHopHealthCallbackIsNonblockingAndWritesCoalesce(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(200, 0)}
	ticks := make(chan time.Time, 4)
	h, pre, post, _ := newHopHealthTestHarness(clock, ticks)
	defer h.Close()
	key, identity := hopHealthTestKey('e'), hopHealthTestKey('f')
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{hopHealthSnapshot("pre", "r", identity, key, "quarantined", 1)}, exported: []core.HopPoolState{{Role: "pre", RouteKey: "r", PoolIdentity: identity, Entries: []hoppool.Entry{{Key: key, Trips: 1, NextProbe: clock.Now().Add(30 * time.Second)}}}}}
	hook := h.Hook()
	h.Attach(inst)
	// Hold persistence serialization so no asynchronous worker can touch the fake core.
	h.ioMu.Lock()
	inst.mu.Lock()
	calls := inst.calls
	inst.mu.Unlock()
	hook(core.HopEvent{Role: "pre", RouteKey: "r", Key: key, Kind: core.HopEventQuarantined, Trips: 1})
	inst.mu.Lock()
	after := inst.calls
	inst.mu.Unlock()
	h.mu.Lock()
	pending := len(h.pending)
	h.mu.Unlock()
	h.ioMu.Unlock()
	if after != calls {
		t.Fatal("callback synchronously queried core")
	}
	if pending != 1 {
		t.Fatal("callback did not enqueue exactly one event")
	}
	h.processEvents()
	ticks <- clock.Now()
	h.saveTick()
	if _, n := pre.snapshot(); n != 1 {
		t.Fatalf("writes=%d", n)
	}
	if _, n := post.snapshot(); n != 1 {
		t.Fatalf("post role saves=%d", n)
	}
	clock.Add(2 * time.Second)
	inst.setSnapshots(hopHealthSnapshot("pre", "r", identity, key, "healthy", 0))
	inst.mu.Lock()
	inst.exported = nil
	inst.mu.Unlock()
	hook(core.HopEvent{Role: "pre", RouteKey: "r", Key: key, Kind: core.HopEventRevived})
	h.processEvents()
	ticks <- clock.Now()
	h.saveTick()
	if _, n := pre.snapshot(); n != 1 {
		t.Fatal("write was not debounced")
	}
	clock.Add(hopHealthSaveInterval)
	ticks <- clock.Now()
	h.saveTick()
	if _, n := pre.snapshot(); n != 2 {
		t.Fatal("last state was not saved at the debounce tick")
	}
}

func TestHopHealthCleanStopFlushesAndEmptySaveRemovesFile(t *testing.T) {
	home := t.TempDir()
	clock := &hopTestClock{now: time.Unix(500, 0)}
	key, identity := hopHealthTestKey('1'), hopHealthTestKey('2')
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{hopHealthSnapshot("pre", "r", identity, key, "quarantined", 1)}, exported: []core.HopPoolState{{Role: "pre", RouteKey: "r", PoolIdentity: identity, Entries: []hoppool.Entry{{Key: key, Trips: 1, NextProbe: clock.Now().Add(time.Minute)}}}}}
	h := newHopHealth(home, nil, nil, clock.Now, nil)
	_ = h.Hook()
	h.Attach(inst)
	h.Close()
	if _, err := os.Stat(hopHealthPath(home, "pre")); err != nil {
		t.Fatalf("clean stop did not flush: %v", err)
	}
	inst.setSnapshots()
	inst.mu.Lock()
	inst.exported = nil
	inst.mu.Unlock()
	h2 := newHopHealth(home, nil, nil, clock.Now, nil)
	_ = h2.Hook()
	h2.Attach(inst)
	h2.Close()
	if _, err := os.Stat(hopHealthPath(home, "pre")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty save left file: %v", err)
	}
}

func TestHopHealthLogsExactSafeLinesAndAllDownOutageRecovery(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(1000, 0)}
	h, _, _, logs := newHopHealthTestHarness(clock, nil)
	defer h.Close()
	key1, key2, identity := hopHealthTestKey('a'), hopHealthTestKey('b'), hopHealthTestKey('c')
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{
		hopHealthSnapshot("post", "route-secret", identity, key1, "quarantined", 1),
		hopHealthSnapshot("post", "route-secret", identity, key2, "probing", 1),
	}, exported: []core.HopPoolState{{Role: "post", RouteKey: "route-secret", PoolIdentity: identity, Entries: []hoppool.Entry{{Key: key1, Trips: 2, NextProbe: clock.Now().Add(45 * time.Second)}}}}}
	hook := h.Hook()
	h.Attach(inst)
	hook(core.HopEvent{Role: "post", RouteKey: "route-secret", Key: key1, Kind: core.HopEventQuarantined, Trips: 2})
	hook(core.HopEvent{Role: "post", RouteKey: "route-secret", Key: key1, Kind: core.HopEventQuarantined, Trips: 2})
	h.processEvents()
	_, warns := logs.get()
	want := []string{"hop post aaaaaaaa quarantined: trips=2 backoff=45s", "hop post pool: all 2 hops are down, trying them anyway"}
	if !sameHopLines(warns, want) {
		t.Fatalf("warnings=%q want=%q", warns, want)
	}
	clock.Add(3 * time.Second)
	inst.setSnapshots(hopHealthSnapshot("post", "route-secret", identity, key1, "healthy", 0), hopHealthSnapshot("post", "route-secret", identity, key2, "healthy", 0))
	hook(core.HopEvent{Role: "post", RouteKey: "route-secret", Key: key1, Kind: core.HopEventRevived})
	h.processEvents()
	infos, _ := logs.get()
	if !sameHopLines(infos, []string{"hop post aaaaaaaa back: downtime=3s"}) {
		t.Fatalf("infos=%q", infos)
	}
	inst.setSnapshots(hopHealthSnapshot("post", "route-secret", identity, key1, "quarantined", 2), hopHealthSnapshot("post", "route-secret", identity, key2, "quarantined", 1))
	hook(core.HopEvent{Role: "post", RouteKey: "route-secret", Key: key2, Kind: core.HopEventQuarantined, Trips: 1})
	h.processEvents()
	_, warns = logs.get()
	if len(warns) != 4 || warns[2] != "hop post bbbbbbbb quarantined: trips=1 backoff=0s" || warns[3] != "hop post pool: all 2 hops are down, trying them anyway" {
		t.Fatalf("recovery warnings=%q", warns)
	}
	if strings.Contains(strings.Join(append(infos, warns...), " "), "route-secret") {
		t.Fatal("logs contain route")
	}
}

func TestHopHealthRefreshReconcilesRemovedMetadataWithoutRestore(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(2000, 0)}
	h, pre, _, _ := newHopHealthTestHarness(clock, nil)
	defer h.Close()
	key, identity := hopHealthTestKey('a'), hopHealthTestKey('b')
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{hopHealthSnapshot("pre", "r", identity, key, "quarantined", 1)}, exported: []core.HopPoolState{{Role: "pre", RouteKey: "r", PoolIdentity: identity, Entries: []hoppool.Entry{{Key: key, Trips: 1, NextProbe: clock.Now().Add(time.Minute)}}}}}
	h.Hook()
	h.Attach(inst)
	inst.setSnapshots()
	before := len(inst.restored)
	h.Refresh()
	h.processEvents()
	if len(inst.restored) != before {
		t.Fatal("refresh reloaded/restored disk state")
	}
	clock.Add(hopHealthSaveInterval)
	h.saveTick()
	if entries, _ := pre.snapshot(); len(entries) != 0 {
		t.Fatalf("removed key persisted: %#v", entries)
	}
}

func TestHopHealthPathsUseRoleSpecificHopstateStore(t *testing.T) {
	home := t.TempDir()
	for _, role := range []string{"pre", "post"} {
		want := filepath.Join(home, "run", "hops-"+role+".health")
		if got := hopHealthPath(home, role); got != want {
			t.Fatalf("path=%q want=%q", got, want)
		}
	}
	store := hopstate.NewStore(hopHealthPath(home, "pre"))
	if err := store.Save([]hoppool.Entry{{Key: hopHealthTestKey('a'), Trips: 1, NextProbe: time.Now().UTC()}}); err != nil {
		t.Fatal("store save failed")
	}
	if err := store.Save(nil); err != nil {
		t.Fatal("empty save failed")
	}
	if _, err := os.Stat(hopHealthPath(home, "pre")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty state retained: %v", err)
	}
}

func TestHopHealthRefreshDoesNotRepeatHandledTransitions(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(3000, 0)}
	h, _, _, logs := newHopHealthTestHarness(clock, nil)
	defer h.Close()
	key, identity := hopHealthTestKey('d'), hopHealthTestKey('e')
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{hopHealthSnapshot("pre", "safe-route", identity, key, "healthy", 0)}}
	hook := h.Hook()
	h.Attach(inst)
	inst.setSnapshots(hopHealthSnapshot("pre", "safe-route", identity, key, "quarantined", 1))
	hook(core.HopEvent{Role: "pre", RouteKey: "safe-route", Key: key, Kind: core.HopEventQuarantined, Trips: 1})
	h.processEvents()
	_, before := logs.get()
	h.Refresh()
	h.processEvents()
	_, after := logs.get()
	if !sameHopLines(after, before) || len(after) != 2 {
		t.Fatalf("refresh repeated a handled quarantine: before=%q after=%q", before, after)
	}
	clock.Add(time.Second)
	inst.setSnapshots(hopHealthSnapshot("pre", "safe-route", identity, key, "healthy", 0))
	hook(core.HopEvent{Role: "pre", RouteKey: "safe-route", Key: key, Kind: core.HopEventRevived})
	h.processEvents()
	infos, _ := logs.get()
	h.Refresh()
	h.processEvents()
	infosAfter, _ := logs.get()
	if !sameHopLines(infosAfter, infos) || !sameHopLines(infos, []string{"hop pre dddddddd back: downtime=1s"}) {
		t.Fatalf("refresh repeated a handled revival: before=%q after=%q", infos, infosAfter)
	}
}

func TestHopHealthDetachedClosePersistsCurrentEmptyMemory(t *testing.T) {
	clock := &hopTestClock{now: time.Unix(4000, 0)}
	h, pre, post, _ := newHopHealthTestHarness(clock, nil)
	key, identity := hopHealthTestKey('a'), hopHealthTestKey('b')
	pre.mu.Lock()
	pre.entries = []hoppool.Entry{{Key: key, Trips: 1, NextProbe: clock.Now().Add(time.Minute)}}
	pre.mu.Unlock()
	inst := &fakeHopHealthCore{snapshots: []core.HopPoolSnapshot{hopHealthSnapshot("pre", "r", identity, key, "healthy", 0)}}
	h.Hook()
	h.Attach(inst)
	inst.setSnapshots()
	inst.mu.Lock()
	inst.exported = nil
	inst.mu.Unlock()
	h.Detach()
	_, preAfterDetach := pre.snapshot()
	_, postAfterDetach := post.snapshot()
	h.Close()
	_, preAfterClose := pre.snapshot()
	_, postAfterClose := post.snapshot()
	if preAfterClose != preAfterDetach+1 || postAfterClose != postAfterDetach+1 {
		t.Fatalf("detached close did not save both role memories: pre %d->%d post %d->%d", preAfterDetach, preAfterClose, postAfterDetach, postAfterClose)
	}
	if entries, _ := pre.snapshot(); len(entries) != 0 {
		t.Fatalf("stale persisted quarantine returned after detach: %#v", entries)
	}
}

func sameHopLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
