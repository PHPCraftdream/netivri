package stats

import (
	"fmt"
	"time"
)

// BindingTTL is how long an idle persisted binding is kept: the retention of
// the (login, site) rows, which hold the sites a user visited. It matches the
// core's default affinity TTL.
const BindingTTL = 24 * time.Hour

// maxBindingRows bounds the stored and loaded sets; the oldest excess is
// dropped.
const maxBindingRows = 100000

// Binding is one persisted site-to-node binding. Label is the stable node
// label ({PROVIDER}/{NODE_ID}); member indexes are never stored.
type Binding struct {
	Login, Site, Label string
	BoundAt, LastUsed  time.Time
}

// bindingSchemaVersion is the schema version that introduced the binding
// table; older databases have no bindings to load or replace.
const bindingSchemaVersion = 4

// LoadBindings returns stored bindings that are not expired by
// lastUsed + ttl, the freshest last_used first, at most limit of them
// (0 = maxBindingRows). On a writable store rows older than the TTL are
// deleted first; on a read-only store they are only filtered. A database
// older than the binding schema has no rows.
func (s *Store) LoadBindings(now time.Time, ttl time.Duration, limit int) ([]Binding, error) {
	if s.version < bindingSchemaVersion {
		return nil, nil
	}
	if limit <= 0 {
		limit = maxBindingRows
	}
	cutoff := now.UTC().Add(-ttl).UnixMilli()
	if !s.ro {
		if _, err := s.db.Exec("DELETE FROM binding WHERE last_used < ?", cutoff); err != nil {
			return nil, fmt.Errorf("stats: delete stale bindings: %w", err)
		}
	}
	rows, err := s.db.Query(
		"SELECT login, site, label, bound_at, last_used FROM binding ORDER BY last_used DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("stats: query bindings: %w", err)
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		var boundAt, lastUsed int64
		if err := rows.Scan(&b.Login, &b.Site, &b.Label, &boundAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("stats: scan binding: %w", err)
		}
		if lastUsed < cutoff {
			continue
		}
		b.BoundAt = time.UnixMilli(boundAt).UTC()
		b.LastUsed = time.UnixMilli(lastUsed).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// bindingWrite is a queued ReplaceBindings request for the writer goroutine.
type bindingWrite struct {
	rows []Binding
}

// ReplaceBindings replaces the stored set with rows in one transaction:
// rows are upserted, rows not present any more are deleted, and rows older
// than the binding TTL are deleted, including incoming ones. On a read-only
// store, after Close, or on a database older than the binding schema it is a
// no-op.
func (s *Store) ReplaceBindings(rows []Binding) error {
	if s.ro || s.version < bindingSchemaVersion {
		return nil
	}
	if len(rows) > maxBindingRows {
		rows = rows[:maxBindingRows]
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil
	}
	ack := make(chan error, 1)
	s.queue <- item{bindings: &bindingWrite{rows: rows}, ack: ack}
	s.mu.RUnlock()
	return <-ack
}

// writeBindings replaces the stored set in one transaction. Called only from
// the writer goroutine; all times come from the store clock.
func (s *Store) writeBindings(w *bindingWrite) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("stats: begin: %w", err)
	}
	defer tx.Rollback()

	cutoff := s.now().UTC().Add(-BindingTTL).UnixMilli()
	if _, err := tx.Exec("DELETE FROM binding WHERE last_used < ?", cutoff); err != nil {
		return fmt.Errorf("stats: delete stale bindings: %w", err)
	}
	// The kept keys go into a temp table so rows that are no longer live can
	// be deleted in one statement inside the same transaction.
	if _, err := tx.Exec(
		"CREATE TEMP TABLE IF NOT EXISTS binding_keep (login TEXT NOT NULL, site TEXT NOT NULL, PRIMARY KEY (login, site))"); err != nil {
		return fmt.Errorf("stats: prepare binding keep: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM binding_keep"); err != nil {
		return fmt.Errorf("stats: clear binding keep: %w", err)
	}
	upsert, err := tx.Prepare(`
INSERT INTO binding (login, site, label, bound_at, last_used) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(login, site) DO UPDATE SET
  label = excluded.label, bound_at = excluded.bound_at, last_used = excluded.last_used`)
	if err != nil {
		return fmt.Errorf("stats: prepare upsert binding: %w", err)
	}
	defer upsert.Close()
	keep, err := tx.Prepare("INSERT INTO binding_keep (login, site) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("stats: prepare binding keep row: %w", err)
	}
	defer keep.Close()

	for _, b := range w.rows {
		lastUsed := b.LastUsed.UTC().UnixMilli()
		if lastUsed < cutoff {
			// An expired row is never (re)written: the TTL delete above must
			// not be undone by the snapshot.
			continue
		}
		if _, err := upsert.Exec(b.Login, b.Site, b.Label, b.BoundAt.UTC().UnixMilli(), lastUsed); err != nil {
			return fmt.Errorf("stats: upsert binding: %w", err)
		}
		if _, err := keep.Exec(b.Login, b.Site); err != nil {
			return fmt.Errorf("stats: keep binding: %w", err)
		}
	}
	if _, err := tx.Exec("DELETE FROM main.binding WHERE (login, site) NOT IN (SELECT login, site FROM binding_keep)"); err != nil {
		return fmt.Errorf("stats: delete dropped bindings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats: commit bindings: %w", err)
	}
	return nil
}
