package stats

import (
	"fmt"
	"time"
)

// prune folds raw samples older than the retention into hourly, deletes
// them and drops hourly rows older than 90 days, all in one transaction.
func (s *Store) prune(now time.Time) error {
	cutoff := now.UTC().Add(-s.opts.Retention).UnixMilli()
	hourlyCutoff := now.UTC().Add(-hourlyTTL).Unix() / 3600

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("stats: begin prune: %w", err)
	}
	defer tx.Rollback()

	// Fold old raw samples into hourly, adding to existing rows.
	fold, err := tx.Prepare(`
INSERT INTO hourly (hour, node, kind, n, ok, ms_sum, ms_max, bytes)
SELECT ts / 3600000, node, kind, COUNT(*), SUM(ok),
       SUM(CASE WHEN ok = 1 THEN ms ELSE 0 END), MAX(ms), SUM(bytes)
FROM sample WHERE ts < ?
GROUP BY ts / 3600000, node, kind
ON CONFLICT(hour, node, kind) DO UPDATE SET
  n = n + excluded.n, ok = ok + excluded.ok, ms_sum = ms_sum + excluded.ms_sum,
  ms_max = MAX(ms_max, excluded.ms_max), bytes = bytes + excluded.bytes`)
	if err != nil {
		return fmt.Errorf("stats: prepare fold: %w", err)
	}
	defer fold.Close()
	if _, err := fold.Exec(cutoff); err != nil {
		return fmt.Errorf("stats: fold samples: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM sample WHERE ts < ?", cutoff); err != nil {
		return fmt.Errorf("stats: delete samples: %w", err)
	}

	// Same fold for the per-account samples.
	foldAcct, err := tx.Prepare(`
INSERT INTO account_hourly (hour, account, node, kind, n, ok, ms_sum, ms_max, bytes, up, down)
SELECT ts / 3600000, account, node, kind, COUNT(*), SUM(ok),
       SUM(CASE WHEN ok = 1 THEN ms ELSE 0 END), MAX(ms), SUM(bytes), SUM(up), SUM(down)
FROM account_sample WHERE ts < ?
GROUP BY ts / 3600000, account, node, kind
ON CONFLICT(hour, account, node, kind) DO UPDATE SET
  n = n + excluded.n, ok = ok + excluded.ok, ms_sum = ms_sum + excluded.ms_sum,
  ms_max = MAX(ms_max, excluded.ms_max), bytes = bytes + excluded.bytes,
  up = up + excluded.up, down = down + excluded.down`)
	if err != nil {
		return fmt.Errorf("stats: prepare fold: %w", err)
	}
	defer foldAcct.Close()
	if _, err := foldAcct.Exec(cutoff); err != nil {
		return fmt.Errorf("stats: fold samples: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM account_sample WHERE ts < ?", cutoff); err != nil {
		return fmt.Errorf("stats: delete samples: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM account_hourly WHERE hour < ?", hourlyCutoff); err != nil {
		return fmt.Errorf("stats: delete hourly: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM hourly WHERE hour < ?", hourlyCutoff); err != nil {
		return fmt.Errorf("stats: delete hourly: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats: commit prune: %w", err)
	}
	return nil
}

// retentionLoop prunes every pruneInterval until the store is closed; the
// open-time prune has already run synchronously in openStore.
func (s *Store) retentionLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = s.prune(s.now())
		case <-s.done:
			return
		}
	}
}
