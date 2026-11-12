package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/hopstate"
	hopkey "github.com/PHPCraftdream/netivri/pkg/hopkit/hop"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

const (
	hopHealthSaveInterval = 5 * time.Second
	hopHealthMaxPending   = 2048
)

// Role-scoped files prevent a pre-hop verdict from poisoning the same post-hop key.
func hopHealthPath(home, role string) string {
	return filepath.Join(angel.RunDir(home), "hops-"+role+".health")
}

type hopHealthCore interface {
	SnapshotHopPools() []core.HopPoolSnapshot
	ExportHopPools() []core.HopPoolState
	RestoreHopPools([]core.HopPoolState) error
}

type hopHealthStore interface {
	Load() ([]hoppool.Entry, error)
	Save([]hoppool.Entry) error
}

type hopHealth struct {
	mu          sync.Mutex
	ioMu        sync.Mutex
	core        hopHealthCore
	stores      map[string]hopHealthStore
	info        logFunc
	warn        logFunc
	now         func() time.Time
	ticks       <-chan time.Time
	wake        chan struct{}
	stop        chan struct{}
	done        chan struct{}
	close       sync.Once
	manual      bool
	memoryValid bool

	closing                           bool
	nextHook, pendingHook, activeHook uint64
	pending                           []pendingHopEvent
	rescan                            bool
	dirty                             bool
	dirtyVersion                      uint64
	lastWrite                         time.Time
	hasWritten                        bool
	loadWarned, saveWarned            bool

	observed   map[hopHealthEventKey]bool
	quarantine map[hopHealthEventKey]time.Time
	lastEvent  map[hopHealthEventKey]core.HopEventKind
	memory     map[string]map[string]hoppool.Entry
	allDown    map[hopHealthGroup]bool
}

type hopHealthEventKey struct {
	role, key string
}
type pendingHopEvent struct {
	event core.HopEvent
	hook  uint64
}
type hopHealthGroup struct {
	role, route, identity string
}

func newHopHealth(home string, infof, warnf logFunc, now func() time.Time, ticks <-chan time.Time) *hopHealth {
	return newHopHealthWithStores(map[string]hopHealthStore{
		"pre":  hopstate.NewStore(hopHealthPath(home, "pre")),
		"post": hopstate.NewStore(hopHealthPath(home, "post")),
	}, infof, warnf, now, ticks)
}

func newHopHealthWithStores(stores map[string]hopHealthStore, infof, warnf logFunc, now func() time.Time, ticks <-chan time.Time) *hopHealth {
	return newHopHealthWithMode(stores, infof, warnf, now, ticks, false)
}

func newHopHealthWithMode(stores map[string]hopHealthStore, infof, warnf logFunc, now func() time.Time, ticks <-chan time.Time, manual bool) *hopHealth {
	if now == nil {
		now = time.Now
	}
	h := &hopHealth{
		stores: stores, info: infof, warn: warnf, now: now, ticks: ticks, manual: manual,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		observed: make(map[hopHealthEventKey]bool), quarantine: make(map[hopHealthEventKey]time.Time),
		lastEvent: make(map[hopHealthEventKey]core.HopEventKind),
		memory:    map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}, allDown: make(map[hopHealthGroup]bool),
	}
	if !manual {
		go h.run()
	}
	return h
}

// Hook returns a nonblocking core callback; it never calls back into core.
func (h *hopHealth) Hook() func(core.HopEvent) {
	h.mu.Lock()
	h.nextHook++
	h.pendingHook = h.nextHook
	hook := h.pendingHook
	h.mu.Unlock()
	return func(event core.HopEvent) { h.enqueue(hook, event) }
}

func (h *hopHealth) enqueue(hook uint64, event core.HopEvent) {
	if !validHopHealthRole(event.Role) || !validHopHealthKey(event.Key) ||
		(event.Kind != core.HopEventQuarantined && event.Kind != core.HopEventRevived) {
		return
	}
	h.mu.Lock()
	if h.closing || (hook != h.activeHook && hook != h.pendingHook) {
		h.mu.Unlock()
		return
	}
	for i := len(h.pending) - 1; i >= 0; i-- {
		old := h.pending[i]
		if old.hook == hook && old.event.Role == event.Role && old.event.Key == event.Key {
			if old.event.Kind != event.Kind {
				h.rescan = true
			}
			h.pending[i] = pendingHopEvent{event: event, hook: hook}
			h.dirty, h.dirtyVersion = true, h.dirtyVersion+1
			h.mu.Unlock()
			h.signal()
			return
		}
	}
	if len(h.pending) < hopHealthMaxPending {
		h.pending = append(h.pending, pendingHopEvent{event: event, hook: hook})
	} else {
		h.rescan = true
	}
	h.dirty, h.dirtyVersion = true, h.dirtyVersion+1
	h.mu.Unlock()
	h.signal()
}

func (h *hopHealth) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Attach is called after core startup; it restores only records matched to current pool metadata.
func (h *hopHealth) Attach(inst hopHealthCore) {
	if inst == nil {
		return
	}
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.attachLocked(inst)
}

func (h *hopHealth) attachLocked(inst hopHealthCore) {
	h.mu.Lock()
	old, oldHook := h.core, h.activeHook
	h.activeHook = 0
	h.mu.Unlock()
	if old != nil && oldHook != 0 {
		h.processForLocked(old, oldHook)
	}
	if old != nil {
		h.cacheCore(old)
		h.saveCoreLocked(old, false)
	}

	stored, failed := h.loadRoles()
	if failed {
		stored = map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}
		h.warnLoadOnce()
	}
	h.mu.Lock()
	if h.memoryValid {
		// Memory of the previous core is newer than the files.
		stored = map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}
	}
	for role, rows := range h.memory {
		for key, entry := range rows {
			if stored[role] == nil {
				stored[role] = make(map[string]hoppool.Entry)
			}
			stored[role][key] = entry
		}
	}
	h.mu.Unlock()

	snapshots := inst.SnapshotHopPools()
	states := hopHealthRestoreStates(snapshots, stored)
	restored := len(states) > 0 && inst.RestoreHopPools(states) == nil
	if len(states) > 0 && !restored {
		h.warnLoadOnce()
	}
	if restored {
		snapshots = inst.SnapshotHopPools()
	}
	// Drop role-scoped in-memory state for keys no longer in the current pools.
	currentKeys := make(map[string]map[string]bool, 2)
	currentKeys["pre"], currentKeys["post"] = make(map[string]bool), make(map[string]bool)
	for _, snapshot := range snapshots {
		if validHopMetadata(snapshot) {
			currentKeys[snapshot.Role][snapshot.Key] = true
		}
	}
	h.mu.Lock()
	for role, rows := range h.memory {
		for key := range rows {
			if !currentKeys[role][key] {
				delete(rows, key)
			}
		}
	}
	h.core, h.activeHook = inst, h.pendingHook
	h.pendingHook = 0
	h.dirty, h.dirtyVersion = true, h.dirtyVersion+1
	h.observed = make(map[hopHealthEventKey]bool)
	for _, snapshot := range snapshots {
		if !validHopMetadata(snapshot) {
			continue
		}
		key := hopHealthEventKey{role: snapshot.Role, key: snapshot.Key}
		down := snapshot.State != "healthy"
		h.observed[key] = down
		if down {
			if _, exists := h.quarantine[key]; !exists {
				h.quarantine[key] = h.now()
			}
		}
	}
	h.memory = map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}
	h.memoryValid = false
	h.mu.Unlock()
	h.signal()
}

func (h *hopHealth) loadRoles() (map[string]map[string]hoppool.Entry, bool) {
	loaded := make(map[string]map[string]hoppool.Entry, 2)
	failed := false
	for _, role := range []string{"pre", "post"} {
		store := h.stores[role]
		if store == nil {
			failed = true
			continue
		}
		entries, err := store.Load()
		if err != nil {
			failed = true
			continue
		}
		loaded[role] = make(map[string]hoppool.Entry)
		for _, entry := range entries {
			if validHopHealthKey(entry.Key) && entry.Trips >= 0 {
				loaded[role][entry.Key] = entry
			}
		}
	}
	return loaded, failed
}

func (h *hopHealth) warnLoadOnce() {
	h.mu.Lock()
	first := !h.loadWarned
	h.loadWarned = true
	h.mu.Unlock()
	if first && h.warn != nil {
		h.warn("hop state not loaded")
	}
}

func (h *hopHealth) Activate() {
	h.mu.Lock()
	if !h.closing && h.pendingHook != 0 && h.core != nil {
		h.activeHook, h.pendingHook = h.pendingHook, 0
		h.dirty, h.dirtyVersion = true, h.dirtyVersion+1
	}
	h.mu.Unlock()
	h.signal()
}

// Refresh schedules a post-reconfigure snapshot without reloading persisted state.
func (h *hopHealth) Refresh() {
	h.mu.Lock()
	if h.core != nil {
		if h.pendingHook != 0 {
			h.activeHook, h.pendingHook = h.pendingHook, 0
		}
		h.rescan = true
		h.dirty, h.dirtyVersion = true, h.dirtyVersion+1
	}
	h.mu.Unlock()
	h.signal()
}

// RejectPending retires callbacks from a failed candidate configuration.
func (h *hopHealth) RejectPending() {
	h.mu.Lock()
	candidate := h.pendingHook
	h.pendingHook = 0
	filtered := h.pending[:0]
	for _, event := range h.pending {
		if event.hook != candidate {
			filtered = append(filtered, event)
		}
	}
	h.pending = filtered
	if len(h.pending) == 0 {
		h.rescan = true
	}
	h.mu.Unlock()
	h.signal()
}

// Detach preserves in-memory quarantine state but does not force an early disk write.
func (h *hopHealth) Detach() {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.detachLocked()
}

func (h *hopHealth) detachLocked() {
	h.mu.Lock()
	inst, hook := h.core, h.activeHook
	h.activeHook, h.pendingHook = 0, 0
	h.mu.Unlock()
	if inst != nil {
		h.processForLocked(inst, hook)
		h.cacheCore(inst)
		h.saveCoreLocked(inst, false)
	}
	h.mu.Lock()
	if h.core == inst {
		h.core = nil
	}
	h.mu.Unlock()
}

func (h *hopHealth) cacheCore(inst hopHealthCore) {
	snapshots, exported := inst.SnapshotHopPools(), inst.ExportHopPools()
	entries := hopHealthEntries(snapshots, exported)
	h.mu.Lock()
	h.memory = map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}
	h.memoryValid = true
	valid := map[string]map[string]bool{"pre": {}, "post": {}}
	for _, snapshot := range snapshots {
		if validHopMetadata(snapshot) {
			valid[snapshot.Role][snapshot.Key] = true
		}
	}
	for role, rows := range entries {
		for _, entry := range rows {
			if valid[role][entry.Key] {
				h.memory[role][entry.Key] = entry
			}
		}
	}
	h.mu.Unlock()
}

func (h *hopHealth) run() {
	defer close(h.done)
	ticks := h.ticks
	if ticks == nil {
		ticker := time.NewTicker(hopHealthSaveInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-h.stop:
			h.flushAndStop()
			return
		case <-h.wake:
			h.processEvents()
		case <-ticks:
			h.saveTick()
		}
	}
}

func (h *hopHealth) saveTick() {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.mu.Lock()
	inst, dirty, closing := h.core, h.dirty, h.closing
	h.mu.Unlock()
	if inst != nil && dirty && !closing {
		h.saveCoreLocked(inst, false)
	}
}

func (h *hopHealth) processEvents() {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.mu.Lock()
	inst, hook := h.core, h.activeHook
	h.mu.Unlock()
	if inst != nil && hook != 0 {
		h.processForLocked(inst, hook)
	}
}

func (h *hopHealth) processForLocked(inst hopHealthCore, hook uint64) {
	h.mu.Lock()
	var events []core.HopEvent
	pending := h.pending[:0]
	for _, queued := range h.pending {
		if queued.hook == hook {
			events = append(events, queued.event)
		} else {
			pending = append(pending, queued)
		}
	}
	h.pending = pending
	rescan := h.rescan
	h.rescan = false
	h.mu.Unlock()
	if len(events) == 0 && !rescan {
		return
	}
	snapshots, exported := inst.SnapshotHopPools(), inst.ExportHopPools()
	if rescan {
		h.reconcileSnapshots(snapshots, exported)
	}
	for _, event := range events {
		h.logTransition(event, snapshots, exported)
	}
	h.updateAllDown(snapshots)
}

func (h *hopHealth) reconcileSnapshots(snapshots []core.HopPoolSnapshot, exported []core.HopPoolState) {
	current := make(map[hopHealthEventKey]bool)
	var events []core.HopEvent
	now := h.now()
	h.mu.Lock()
	for _, snapshot := range snapshots {
		if !validHopMetadata(snapshot) {
			continue
		}
		id := hopHealthEventKey{role: snapshot.Role, key: snapshot.Key}
		current[id] = true
		down := snapshot.State != "healthy"
		previous, exists := h.observed[id]
		if !exists {
			h.observed[id] = down
			if down {
				h.quarantine[id] = now
				events = append(events, core.HopEvent{Role: id.role, RouteKey: snapshot.RouteKey, Key: id.key, Kind: core.HopEventQuarantined, Trips: snapshot.Trips})
			}
		} else if previous != down {
			h.observed[id] = down
			kind := core.HopEventRevived
			if down {
				kind = core.HopEventQuarantined
				h.quarantine[id] = now
			}
			events = append(events, core.HopEvent{Role: id.role, RouteKey: snapshot.RouteKey, Key: id.key, Kind: kind, Trips: snapshot.Trips})
		}
	}
	for id := range h.observed {
		if !current[id] {
			delete(h.observed, id)
			delete(h.quarantine, id)
			delete(h.lastEvent, id)
		}
	}
	h.mu.Unlock()
	for _, event := range events {
		h.logTransition(event, snapshots, exported)
	}
}

func (h *hopHealth) logTransition(event core.HopEvent, snapshots []core.HopPoolSnapshot, exported []core.HopPoolState) {
	if !validHopHealthRole(event.Role) || !validHopHealthKey(event.Key) {
		return
	}
	id := hopHealthEventKey{role: event.Role, key: event.Key}
	h.mu.Lock()
	if h.lastEvent[id] == event.Kind {
		h.mu.Unlock()
		return
	}
	// Ensure queued transitions are still represented by current role/route/key metadata.
	current := false
	for _, snapshot := range snapshots {
		if snapshot.Role == event.Role && snapshot.RouteKey == event.RouteKey && snapshot.Key == event.Key && validHopMetadata(snapshot) {
			current = (event.Kind == core.HopEventQuarantined && snapshot.State != "healthy") ||
				(event.Kind == core.HopEventRevived && snapshot.State == "healthy")
			break
		}
	}
	if !current {
		h.mu.Unlock()
		return
	}
	h.lastEvent[id] = event.Kind
	now := h.now()
	started, hasStart := h.quarantine[id]
	switch event.Kind {
	case core.HopEventQuarantined:
		h.quarantine[id] = now
		h.observed[id] = true
	case core.HopEventRevived:
		delete(h.quarantine, id)
		h.observed[id] = false
	}
	h.mu.Unlock()

	short := event.Key[:8]
	if event.Kind == core.HopEventQuarantined {
		backoff := hopHealthBackoff(snapshots, exported, event, now)
		if h.warn != nil {
			h.warn("hop %s %s quarantined: trips=%d backoff=%s", event.Role, short, event.Trips, backoff)
		}
	} else if event.Kind == core.HopEventRevived && h.info != nil {
		downtime := time.Duration(0)
		if hasStart && now.After(started) {
			downtime = now.Sub(started)
		}
		h.info("hop %s %s back: downtime=%s", event.Role, short, downtime)
	}
}

func hopHealthBackoff(snapshots []core.HopPoolSnapshot, states []core.HopPoolState, event core.HopEvent, now time.Time) time.Duration {
	identities := make(map[string]bool)
	for _, snapshot := range snapshots {
		if validHopMetadata(snapshot) && snapshot.Role == event.Role && snapshot.RouteKey == event.RouteKey && snapshot.Key == event.Key {
			identities[snapshot.PoolIdentity] = true
		}
	}
	var next time.Time
	for _, state := range states {
		if state.Role != event.Role || state.RouteKey != event.RouteKey || !identities[state.PoolIdentity] {
			continue
		}
		for _, entry := range state.Entries {
			if entry.Key == event.Key && entry.NextProbe.After(next) {
				next = entry.NextProbe
			}
		}
	}
	if !next.After(now) {
		return 0
	}
	return next.Sub(now)
}

func (h *hopHealth) updateAllDown(snapshots []core.HopPoolSnapshot) {
	type count struct{ total, down int }
	groups := make(map[hopHealthGroup]count)
	for _, snapshot := range snapshots {
		if !validHopMetadata(snapshot) {
			continue
		}
		group := hopHealthGroup{role: snapshot.Role, route: snapshot.RouteKey, identity: snapshot.PoolIdentity}
		value := groups[group]
		value.total++
		if snapshot.State != "healthy" {
			value.down++
		}
		groups[group] = value
	}
	var warnings []string
	h.mu.Lock()
	for group, value := range groups {
		all := value.total > 0 && value.down == value.total
		if all && !h.allDown[group] {
			h.allDown[group] = true
			warnings = append(warnings, fmt.Sprintf("hop %s pool: all %d hops are down, trying them anyway", group.role, value.total))
		} else if !all {
			h.allDown[group] = false
		}
	}
	for group := range h.allDown {
		if _, ok := groups[group]; !ok {
			delete(h.allDown, group)
		}
	}
	h.mu.Unlock()
	for _, warning := range warnings {
		if h.warn != nil {
			h.warn("%s", warning)
		}
	}
}

func (h *hopHealth) saveCoreLocked(inst hopHealthCore, force bool) {
	h.mu.Lock()
	if h.core != inst {
		h.mu.Unlock()
		return
	}
	dirty, version, now := h.dirty, h.dirtyVersion, h.now()
	tooSoon := h.hasWritten && now.Sub(h.lastWrite) < hopHealthSaveInterval
	h.mu.Unlock()
	if !force && (!dirty || tooSoon) {
		return
	}
	entries := hopHealthEntries(inst.SnapshotHopPools(), inst.ExportHopPools())
	failed := false
	for _, role := range []string{"pre", "post"} {
		if store := h.stores[role]; store == nil || store.Save(entries[role]) != nil {
			failed = true
		}
	}
	h.mu.Lock()
	if h.core != inst {
		h.mu.Unlock()
		return
	}
	if failed {
		first := !h.saveWarned
		h.saveWarned = true
		h.mu.Unlock()
		if first && h.warn != nil {
			h.warn("hop state not stored")
		}
		return
	}
	h.lastWrite, h.hasWritten = h.now(), true
	if h.dirtyVersion == version {
		h.dirty = false
	}
	h.mu.Unlock()
}

func hopHealthEntries(snapshots []core.HopPoolSnapshot, exported []core.HopPoolState) map[string][]hoppool.Entry {
	valid := make(map[hopHealthGroup]map[string]bool)
	for _, snapshot := range snapshots {
		if !validHopMetadata(snapshot) {
			continue
		}
		group := hopHealthGroup{role: snapshot.Role, route: snapshot.RouteKey, identity: snapshot.PoolIdentity}
		if valid[group] == nil {
			valid[group] = make(map[string]bool)
		}
		valid[group][snapshot.Key] = true
	}
	byRole := map[string]map[string]hoppool.Entry{"pre": {}, "post": {}}
	for _, state := range exported {
		if !validHopHealthRole(state.Role) || !validHopHealthKey(state.PoolIdentity) {
			continue
		}
		group := hopHealthGroup{role: state.Role, route: state.RouteKey, identity: state.PoolIdentity}
		for _, entry := range state.Entries {
			if !valid[group][entry.Key] || !validHopHealthKey(entry.Key) || entry.Trips < 0 {
				continue
			}
			old, exists := byRole[state.Role][entry.Key]
			if !exists || entry.Trips > old.Trips || entry.NextProbe.After(old.NextProbe) {
				byRole[state.Role][entry.Key] = entry
			}
		}
	}
	out := map[string][]hoppool.Entry{"pre": nil, "post": nil}
	for _, role := range []string{"pre", "post"} {
		for _, entry := range byRole[role] {
			out[role] = append(out[role], entry)
		}
		sort.Slice(out[role], func(i, j int) bool { return out[role][i].Key < out[role][j].Key })
	}
	return out
}

func hopHealthRestoreStates(snapshots []core.HopPoolSnapshot, stored map[string]map[string]hoppool.Entry) []core.HopPoolState {
	groups := make(map[hopHealthGroup]map[string]hoppool.Entry)
	for _, snapshot := range snapshots {
		if !validHopMetadata(snapshot) {
			continue
		}
		entry, ok := stored[snapshot.Role][snapshot.Key]
		if !ok {
			continue
		}
		group := hopHealthGroup{role: snapshot.Role, route: snapshot.RouteKey, identity: snapshot.PoolIdentity}
		if groups[group] == nil {
			groups[group] = make(map[string]hoppool.Entry)
		}
		groups[group][snapshot.Key] = entry
	}
	ordered := make([]hopHealthGroup, 0, len(groups))
	for group := range groups {
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].role != ordered[j].role {
			return ordered[i].role < ordered[j].role
		}
		if ordered[i].route != ordered[j].route {
			return ordered[i].route < ordered[j].route
		}
		return ordered[i].identity < ordered[j].identity
	})
	states := make([]core.HopPoolState, 0, len(ordered))
	for _, group := range ordered {
		state := core.HopPoolState{Role: group.role, RouteKey: group.route, PoolIdentity: group.identity}
		for _, entry := range groups[group] {
			state.Entries = append(state.Entries, entry)
		}
		sort.Slice(state.Entries, func(i, j int) bool { return state.Entries[i].Key < state.Entries[j].Key })
		states = append(states, state)
	}
	return states
}

func validHopMetadata(snapshot core.HopPoolSnapshot) bool {
	return validHopHealthRole(snapshot.Role) && snapshot.RouteKey != "" &&
		validHopHealthKey(snapshot.PoolIdentity) && validHopHealthKey(snapshot.Key)
}
func validHopHealthRole(role string) bool { return role == "pre" || role == "post" }
func validHopHealthKey(key string) bool   { return hopkey.ValidKey(key) }

func (h *hopHealth) flushAndStop() {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.mu.Lock()
	inst, hook := h.core, h.activeHook
	h.activeHook = 0
	h.mu.Unlock()
	if inst != nil && hook != 0 {
		h.processForLocked(inst, hook)
		h.saveCoreLocked(inst, true)
	} else if inst != nil {
		h.saveCoreLocked(inst, true)
	} else if h.memoryValid {
		h.saveMemoryLocked()
	}
	h.mu.Lock()
	h.core = nil
	h.mu.Unlock()
}

func (h *hopHealth) saveMemoryLocked() {
	h.mu.Lock()
	entries := map[string][]hoppool.Entry{"pre": {}, "post": {}}
	for role, byKey := range h.memory {
		for _, entry := range byKey {
			entries[role] = append(entries[role], entry)
		}
	}
	h.mu.Unlock()
	failed := false
	for _, role := range []string{"pre", "post"} {
		if store := h.stores[role]; store == nil || store.Save(entries[role]) != nil {
			failed = true
		}
	}
	if failed {
		h.warnSaveOnce()
	}
}

func (h *hopHealth) warnSaveOnce() {
	h.mu.Lock()
	first := !h.saveWarned
	h.saveWarned = true
	h.mu.Unlock()
	if first && h.warn != nil {
		h.warn("hop state not stored")
	}
}

// Close must run while the core remains available; it performs the final
// flush once. In manual mode (tests drive the worker steps themselves) there is
// no worker goroutine and the flush runs here.
func (h *hopHealth) Close() {
	h.close.Do(func() {
		h.mu.Lock()
		h.closing = true
		h.mu.Unlock()
		if h.manual {
			h.flushAndStop()
			return
		}
		close(h.stop)
	})
	if !h.manual {
		<-h.done
	}
}
