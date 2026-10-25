package main

import (
	"context"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// bindingSaveInterval is how often the angel replaces the stored bindings
// with the live snapshot.
const bindingSaveInterval = 30 * time.Second

// initialBindings loads the persisted bindings of the store for the core's
// InitialBindings. A nil store (no route nodes) or a read error yields
// nothing: the proxy never fails because of its bookkeeping.
func initialBindings(store *stats.Store, logf func(format string, args ...any)) []core.BindingRecord {
	if store == nil {
		return nil
	}
	rows, err := store.LoadBindings(time.Now(), stats.BindingTTL, 0)
	if err != nil {
		logf("stored bindings not loaded: %v", err)
		return nil
	}
	out := make([]core.BindingRecord, len(rows))
	for i, b := range rows {
		out[i] = core.BindingRecord{
			Login:    b.Login,
			Site:     b.Site,
			Label:    b.Label,
			BoundAt:  b.BoundAt,
			LastUsed: b.LastUsed,
		}
	}
	return out
}

// bindingRows converts a snapshot into the store's row shape.
func bindingRows(recs []core.BindingRecord) []stats.Binding {
	rows := make([]stats.Binding, len(recs))
	for i, r := range recs {
		rows[i] = stats.Binding{
			Login:    r.Login,
			Site:     r.Site,
			Label:    r.Label,
			BoundAt:  r.BoundAt,
			LastUsed: r.LastUsed,
		}
	}
	return rows
}

// bindingWriterLoop replaces the stored bindings with the live snapshot
// every bindingSaveInterval until the context is done. Ticks come from
// bindingTickerC when a test provides one.
func (l *liveAngel) bindingWriterLoop(ctx context.Context) {
	every := l.bindingInterval
	if every <= 0 {
		every = bindingSaveInterval
	}
	ticks := l.bindingTickerC
	if ticks == nil {
		t := time.NewTicker(every)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			l.saveBindingsOnce()
		}
	}
}

// bindingsTarget is the (instance, generation) pair a periodic write is
// bound to, captured under l.mu. Every replacement or final write of an
// instance bumps the generation under l.mu, so a captured pair is stale
// exactly when the current generation differs.
type bindingsTarget struct {
	inst  coreInstance
	store *stats.Store
	gen   uint64
}

// captureBindingsTarget reads the current write target under l.mu.
func (l *liveAngel) captureBindingsTarget() (bindingsTarget, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.inst == nil || l.store == nil {
		return bindingsTarget{}, false
	}
	return bindingsTarget{inst: l.inst, store: l.store, gen: l.bindingsGen.Load()}, true
}

// saveBindingsFor writes the snapshot of the target unless its generation is
// no longer current. It never takes l.mu while holding bindingsMu (the final
// write holds l.mu and then takes bindingsMu), so the two cannot deadlock. A
// delayed writer whose instance was replaced by a reload fails the
// generation check and writes nothing; a writer that is already inside
// bindingsMu when the final write starts completes first, and the final
// write then overwrites it with fresher data.
func (l *liveAngel) saveBindingsFor(t bindingsTarget) {
	l.bindingsMu.Lock()
	if l.bindingsGen.Load() != t.gen {
		// The instance of this target was sealed or replaced: its snapshot
		// must never reach the store.
		l.bindingsMu.Unlock()
		return
	}
	err := l.writeBindings(t.store, bindingRows(t.inst.SnapshotBindings()))
	l.bindingsMu.Unlock()
	if err != nil {
		l.warnf("bindings not stored: %v", err)
		return
	}
	l.mu.Lock()
	l.bindingsSavedAt = time.Now()
	l.mu.Unlock()
}

// saveBindingsOnce takes one snapshot of the current instance and replaces
// the stored set.
func (l *liveAngel) saveBindingsOnce() {
	if t, ok := l.captureBindingsTarget(); ok {
		l.saveBindingsFor(t)
	}
}

// writeBindingsFinalLocked writes the last snapshot before the core closes;
// l.mu must be held. It must run while the instance still runs and before
// the store is closed. Bumping the generation first retires the instance for
// every periodic writer, and bindingsMu orders the final write after a
// periodic write that is already in flight.
func (l *liveAngel) writeBindingsFinalLocked(inst coreInstance, store *stats.Store) {
	if inst == nil || store == nil {
		return
	}
	l.bindingsGen.Add(1)
	now := time.Now()
	l.bindingsMu.Lock()
	err := l.writeBindings(store, bindingRows(inst.SnapshotBindings()))
	l.bindingsMu.Unlock()
	if err != nil {
		l.warnf("bindings not stored: %v", err)
		return
	}
	l.bindingsSavedAt = now
}

// writeBindingsFinal is writeBindingsFinalLocked for callers that do not
// hold l.mu.
func (l *liveAngel) writeBindingsFinal(inst coreInstance, store *stats.Store) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeBindingsFinalLocked(inst, store)
}

// noteBindingsRestored records how many persisted bindings the running
// instance actually installed; l.mu must be held. It is the one code path
// the start, restart and rollback paths use.
func (l *liveAngel) noteBindingsRestored(inst coreInstance) {
	l.bindingsRestored = inst.RestoredBindings()
}
