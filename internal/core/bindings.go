package core

import (
	"time"
)

// BindingRecord is one persisted site binding: the node label that served
// the site for the login, and when the binding was made and last used.
// Labels ({PROVIDER}/{NODE_ID}) are the stable identity of a node; member
// indexes are resolved only at load time.
type BindingRecord struct {
	Login, Site, Label string
	BoundAt, LastUsed  time.Time
}

// bindingSnapshot is one live binding of the table without its identity
// resolved to a label yet.
type bindingSnapshot struct {
	key      affinityKey
	member   int
	boundAt  time.Time
	lastUsed time.Time
}

// snapshot returns every live, non-provisional binding of the table, at most
// max of them. It walks the shards under read locks and never creates or
// refreshes a binding.
func (t *affinityTable) snapshot(now time.Time, max int) []bindingSnapshot {
	out := make([]bindingSnapshot, 0, 8)
	for _, shard := range t.shards {
		shard.mu.RLock()
		for key, b := range shard.bindings {
			if len(out) >= max {
				break
			}
			if b.provisional || t.expired(b, now) {
				continue
			}
			out = append(out, bindingSnapshot{
				key:      key,
				member:   b.member,
				boundAt:  b.boundAt,
				lastUsed: time.Unix(0, b.lastUsed.Load()).UTC(),
			})
		}
		shard.mu.RUnlock()
	}
	return out
}

// loadBinding installs one initial binding without creating or refreshing
// anything else. It reports whether the record was loaded: a binding that is
// already expired by lastUsed + ttl is dropped, an existing binding for the
// key or a shard at its cap keeps what it has.
func (t *affinityTable) loadBinding(key affinityKey, member int, boundAt, lastUsed time.Time) bool {
	now := t.now()
	if now.Sub(lastUsed) >= t.ttl {
		return false
	}
	shard := t.shards[t.shardIndex(key.login, key.site)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if _, ok := shard.bindings[key]; ok {
		return false
	}
	if len(shard.bindings) >= shard.cap {
		return false
	}
	b := &binding{member: member, boundAt: boundAt}
	b.lastUsed.Store(lastUsed.UnixNano())
	shard.bindings[key] = b
	return true
}

// SnapshotBindings returns the live, non-provisional, non-expired bindings
// with their node labels, at most maxAffinityBindings of them. It is safe to
// call while the instance runs and never creates, refreshes or counts
// bindings.
func (i *Instance) SnapshotBindings() []BindingRecord {
	table := i.stickyTable
	if table == nil {
		return nil
	}
	snap := table.snapshot(table.now(), maxAffinityBindings)
	out := make([]BindingRecord, 0, len(snap))
	for _, s := range snap {
		labels := i.routeLabels[i.accountRoutes[s.key.login]]
		if s.member < 0 || s.member >= len(labels) {
			continue
		}
		out = append(out, BindingRecord{
			Login:    s.key.login,
			Site:     s.key.site,
			Label:    labels[s.member],
			BoundAt:  s.boundAt,
			LastUsed: s.lastUsed,
		})
	}
	return out
}

// loadInitialBindings maps records to member indexes through the account
// routes and the route plans and loads the rest into the table. A record
// whose login has no route, whose label is not a member of that route, or
// that is already expired is ignored; so is a key the table already holds.
// A stored binding may point at a node that is currently down: acquire
// re-picks for such a member, so loading is safe. Returns how many records
// were loaded.
func loadInitialBindings(table *affinityTable, plans []routePlan, routes map[string]string, records []BindingRecord) int {
	byRoute := make(map[string]*routePlan, len(plans))
	for i := range plans {
		byRoute[plans[i].key] = &plans[i]
	}
	loaded := 0
	for _, r := range records {
		if r.Login == "" || r.Site == "" || r.Label == "" {
			continue
		}
		plan := byRoute[routes[r.Login]]
		if plan == nil {
			continue
		}
		member := -1
		for i, label := range plan.labels {
			if label == r.Label {
				member = i
				break
			}
		}
		if member < 0 {
			continue
		}
		if table.loadBinding(affinityKey{login: r.Login, site: r.Site}, member, r.BoundAt, r.LastUsed) {
			loaded++
		}
	}
	return loaded
}

// routeLabels maps every route key to the statistics label per member.
func routeLabels(plans []routePlan) map[string][]string {
	out := make(map[string][]string, len(plans))
	for _, plan := range plans {
		out[plan.key] = plan.labels
	}
	return out
}
