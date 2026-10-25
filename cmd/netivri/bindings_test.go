package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// bindingTestBase is one minute ago: a row written with it survives the real
// clock that initialBindings and the store use.
var bindingTestBase = time.Now().Add(-time.Minute).Truncate(time.Second).UTC()

// seededRecord is the one binding the tests store and snapshot.
func seededRecord() core.BindingRecord {
	return core.BindingRecord{
		Login: "alice", Site: "example.com", Label: "p/n1",
		BoundAt: bindingTestBase.Add(-time.Hour), LastUsed: bindingTestBase,
	}
}

func seededRow() stats.Binding {
	r := seededRecord()
	return stats.Binding{Login: r.Login, Site: r.Site, Label: r.Label, BoundAt: r.BoundAt, LastUsed: r.LastUsed}
}

// openBindingStore opens a store on the fixture's stats.db path.
func openBindingStore(t *testing.T, f *reloadFixture) *stats.Store {
	t.Helper()
	store, err := stats.Open(stats.Options{Path: filepath.Join(f.home, statsFile)})
	if err != nil {
		t.Fatalf("open stats store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func (f *reloadFixture) setStore(store *stats.Store) {
	f.l.mu.Lock()
	f.l.store = store
	f.l.mu.Unlock()
}

func (f *reloadFixture) setSnap(inst *fakeCore, snap ...core.BindingRecord) {
	inst.mu.Lock()
	inst.snap = snap
	inst.mu.Unlock()
}

// countWrites replaces the write seam with a counting wrapper around the
// real store write and returns the counter's address.
func countWrites(f *reloadFixture) *int {
	writes := 0
	real := f.l.writeBindings
	f.l.mu.Lock()
	f.l.writeBindings = func(store *stats.Store, rows []stats.Binding) error {
		writes++
		return real(store, rows)
	}
	f.l.mu.Unlock()
	return &writes
}

// TestBindingWriterStoresSnapshots drives the writer loop through the
// injected tick channel: the send, the cancel and the done channel
// synchronize without sleeps. The write of a tick completes before the loop
// returns.
func TestBindingWriterStoresSnapshots(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	f.setSnap(f.script.inst(0), seededRecord())
	writes := countWrites(f)

	ticks := make(chan time.Time)
	f.l.mu.Lock()
	f.l.bindingTickerC = ticks
	f.l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.l.bindingWriterLoop(ctx)
		close(done)
	}()
	ticks <- time.Now()
	cancel()
	<-done

	if *writes != 1 {
		t.Fatalf("store writes = %d, want exactly 1", *writes)
	}
	got, err := store.LoadBindings(time.Now(), stats.BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Login != "alice" || got[0].Site != "example.com" || got[0].Label != "p/n1" {
		t.Fatalf("stored bindings = %+v", got)
	}
	f.l.mu.Lock()
	saved := f.l.bindingsSavedAt.IsZero()
	f.l.mu.Unlock()
	if saved {
		t.Error("the successful write did not record saved_at")
	}
}

// TestBindingWriterSkipsRetiredInstance is the exact interleaving the
// generation seal exists for: a periodic writer captures the old instance,
// then a reload's final write and restart complete before it proceeds. The
// delayed write of the retired core must never reach the store, while a
// writer for the new instance writes normally.
func TestBindingWriterSkipsRetiredInstance(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	f.setSnap(f.script.inst(0), seededRecord())
	writes := countWrites(f)

	// The delayed periodic writer captures the old instance.
	target, ok := f.l.captureBindingsTarget()
	if !ok || target.inst != coreInstance(f.script.inst(0)) {
		t.Fatalf("capture = %+v %v, want the old instance", target, ok)
	}

	// The reload seals and writes the old instance, then the new core takes
	// over with a different snapshot.
	f.l.writeBindingsFinal(f.script.inst(0), store)
	newRec := seededRecord()
	newRec.Site = "other.example"
	newCore := &fakeCore{addr: "127.0.0.1:1"}
	newCore.snap = []core.BindingRecord{newRec}
	f.l.mu.Lock()
	f.l.inst = newCore
	f.l.mu.Unlock()

	// The delayed writer proceeds with the retired target: nothing is
	// written.
	f.l.saveBindingsFor(target)
	if *writes != 1 {
		t.Fatalf("store writes = %d, want 1 (the retired instance must not write)", *writes)
	}
	got, err := store.LoadBindings(time.Now(), stats.BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Site != "example.com" {
		t.Fatalf("stored bindings = %+v, want only the final write of the old core", got)
	}

	// A writer for the current instance writes normally and replaces the
	// stored set with its own snapshot.
	f.l.saveBindingsOnce()
	if *writes != 2 {
		t.Fatalf("store writes = %d, want 2 (the new instance writes)", *writes)
	}
	got, err = store.LoadBindings(time.Now(), stats.BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Site != "other.example" {
		t.Fatalf("stored bindings = %+v, want the snapshot of the new core", got)
	}
}

func TestBindingWriterWriteFailureIsLogged(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	f.setSnap(f.script.inst(0), seededRecord())
	f.l.mu.Lock()
	f.l.writeBindings = func(*stats.Store, []stats.Binding) error { return errors.New("disk on fire") }
	f.l.mu.Unlock()

	f.l.saveBindingsOnce()

	if !strings.Contains(f.logText(), "bindings not stored") {
		t.Errorf("the write failure was not logged: %s", f.logText())
	}
	if f.stopped.Load() {
		t.Error("a binding write failure stopped the angel")
	}
	if f.script.calls() != 1 {
		t.Error("a binding write failure restarted the core")
	}
}

func TestReloadPassesStoredBindingsAsInitialBindings(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	if err := store.ReplaceBindings([]stats.Binding{seededRow()}); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	f.setStore(store)
	// The old instance holds the binding live, so its final write keeps the
	// seeded row in the store.
	f.setSnap(f.script.inst(0), seededRecord())

	f.saveNodes(t, "192.0.2.9", "n1")
	f.l.reload(t.Context())
	if f.script.calls() != 2 {
		t.Fatalf("starts = %d, want 2", f.script.calls())
	}
	o := f.script.call(1)
	if len(o.InitialBindings) != 1 || o.InitialBindings[0].Label != "p/n1" ||
		o.InitialBindings[0].Login != "alice" || o.InitialBindings[0].Site != "example.com" ||
		!o.InitialBindings[0].LastUsed.Equal(bindingTestBase) {
		t.Fatalf("InitialBindings of the new core = %+v", o.InitialBindings)
	}
	// The final write of the old core ran while it was still running.
	if f.script.inst(0).sawClosedSnapshot() {
		t.Error("the last snapshot was taken after the old core closed")
	}
	// The angel reports what the new core says it installed.
	f.script.inst(1).mu.Lock()
	f.script.inst(1).restored = 1
	f.script.inst(1).mu.Unlock()
	f.l.noteBindingsRestored(f.script.inst(1))
	f.l.mu.Lock()
	restored := f.l.bindingsRestored
	f.l.mu.Unlock()
	if restored != 1 {
		t.Errorf("restored = %d, want 1", restored)
	}
}

// TestReloadRollbackKeepsFreshBindings: the new configuration fails to
// start, the previous one is restarted with the bindings that were just
// saved from the old instance.
func TestReloadRollbackKeepsFreshBindings(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	f.setSnap(f.script.inst(0), seededRecord())
	f.script.mu.Lock()
	f.script.fail = map[int]error{2: errors.New("config rejected")}
	f.script.mu.Unlock()

	f.saveNodes(t, "192.0.2.9", "n1")
	f.l.reload(t.Context())
	if f.script.calls() != 3 {
		t.Fatalf("starts = %d, want 3 (start, failed new start, rollback)", f.script.calls())
	}
	back := f.script.call(2)
	if len(back.InitialBindings) != 1 || back.InitialBindings[0].Label != "p/n1" {
		t.Fatalf("InitialBindings of the rollback = %+v, want the freshly saved binding", back.InitialBindings)
	}
	if f.stopped.Load() {
		t.Error("a rollback with a working previous configuration stopped the angel")
	}
}

func TestFinalWriteBeforeClose(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	f.setSnap(f.script.inst(0), seededRecord())
	writes := countWrites(f)

	f.l.close()
	if f.script.inst(0).sawClosedSnapshot() {
		t.Error("the final snapshot was taken after the core closed")
	}
	if *writes != 1 {
		t.Fatalf("store writes = %d, want exactly the one final write", *writes)
	}
	ro, err := stats.OpenReadOnly(filepath.Join(f.home, statsFile))
	if err != nil {
		t.Fatalf("reopen read-only: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	got, err := ro.LoadBindings(time.Now(), stats.BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p/n1" {
		t.Fatalf("bindings stored at close = %+v", got)
	}
}

// TestStatusRestoredUsesInstalledCount drives the real start(): the status
// must report what the running instance installed (the fake stands in for
// the real core's count), not how many rows are stored.
func TestStatusRestoredUsesInstalledCount(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	// Two stored candidates: one whose label is still a member of the route
	// and one with an unknown label. A real core installs only the first.
	if err := store.ReplaceBindings([]stats.Binding{
		seededRow(),
		{Login: "alice", Site: "gone.example", Label: "p/nope",
			BoundAt: bindingTestBase, LastUsed: bindingTestBase},
	}); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	f.setStore(store)
	f.script.mu.Lock()
	f.script.restored = 1 // the real core's installed count for these rows
	f.script.mu.Unlock()

	d, err := f.l.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := f.l.start(t.Context(), d, nil, store); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := f.l.status("2026-10-25T10:00:00Z")
	if st.Affinity == nil || st.Affinity.Restored != 1 {
		t.Fatalf("affinity = %+v, want restored 1 (the installed count), not the 2 stored candidates", st.Affinity)
	}
}

// TestStatusSavedAtAfterWrite: saved_at stays empty until a write succeeds.
func TestStatusSavedAtAfterWrite(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	store := openBindingStore(t, f)
	f.setStore(store)
	inst := f.script.inst(0)

	st := f.l.status("2026-10-25T10:00:00Z")
	if st.Affinity == nil || st.Affinity.SavedAt != "" || st.Affinity.Restored != 0 {
		t.Fatalf("affinity without a write = %+v, want no saved_at and restored 0", st.Affinity)
	}

	f.setSnap(inst, seededRecord())
	f.l.saveBindingsOnce()
	st = f.l.status("2026-10-25T10:00:00Z")
	if st.Affinity == nil || st.Affinity.Restored != 0 || st.Affinity.SavedAt == "" {
		t.Fatalf("affinity after a write = %+v, want a saved_at", st.Affinity)
	}
}
