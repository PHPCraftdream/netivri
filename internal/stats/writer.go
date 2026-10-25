package stats

import (
	"database/sql"
	"fmt"
	"time"
)

// writerLoop collects queued items and writes them in one transaction when
// flushBatchSize samples are pending or every flushInterval. It never takes
// s.mu: a blocked sender must never prevent it from draining the queue.
func (s *Store) writerLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]Sample, 0, flushBatchSize)
	acctBatch := make([]AccountSample, 0, flushBatchSize)

	// resetCaches drops both id caches: ids read inside a rolled back
	// transaction may collide with rows other labels or logins insert later.
	resetCaches := func() {
		s.ids = map[string]int64{}
		s.accIDs = map[string]int64{}
	}

	// tryFlush writes the pending node and account batches. On failure the
	// failed batch is kept for the next attempt (the transaction is atomic,
	// so retrying it is safe), the succeeded one is cleared and both caches
	// are reset.
	tryFlush := func() error {
		if len(batch) > 0 {
			if err := s.writeBatch(batch); err != nil {
				resetCaches()
				batch = s.trimPending(batch)
				// The other batch is trimmed too: it would otherwise grow
				// without bound under a persistent node-write failure.
				acctBatch = s.trimPendingAcct(acctBatch)
				return err
			}
			batch = batch[:0]
		}
		if len(acctBatch) > 0 {
			if err := s.writeAccountBatch(acctBatch); err != nil {
				resetCaches()
				acctBatch = s.trimPendingAcct(acctBatch)
				return err
			}
			acctBatch = acctBatch[:0]
		}
		return nil
	}

	for {
		select {
		case it, ok := <-s.queue:
			if !ok {
				s.writerErr = tryFlush()
				return
			}
			if it.ack != nil {
				if len(it.nodes) > 0 {
					it.ack <- s.writeNodes(it.nodes)
				} else {
					// Flush request: everything queued before it has been
					// attempted; the samples are kept on failure.
					it.ack <- tryFlush()
				}
				continue
			}
			if it.acct != nil {
				acctBatch = append(acctBatch, *it.acct)
			} else {
				batch = append(batch, it.sample)
			}
			if len(batch) >= flushBatchSize || len(acctBatch) >= flushBatchSize {
				// Timer and batch flushes retry through the kept batch.
				_ = tryFlush()
			}
		case <-ticker.C:
			_ = tryFlush()
		}
	}
}

// trimPending bounds the retry buffer: when the batch exceeds
// maxPendingSamples the oldest excess samples are dropped and counted.
func (s *Store) trimPending(batch []Sample) []Sample {
	if len(batch) <= maxPendingSamples {
		return batch
	}
	s.dropped.Add(uint64(len(batch) - maxPendingSamples))
	keep := batch[len(batch)-maxPendingSamples:]
	out := make([]Sample, len(keep))
	copy(out, keep)
	return out
}

// writeBatch inserts the samples and creates or touches their node rows in
// one transaction. Called only from the writer goroutine, so the label
// cache needs no lock.
func (s *Store) writeBatch(batch []Sample) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("stats: begin: %w", err)
	}
	defer tx.Rollback()

	insertNode, err := tx.Prepare(
		"INSERT INTO node (label, provider, country, first_seen, last_seen) VALUES (?, '', '', ?, ?)")
	if err != nil {
		return fmt.Errorf("stats: prepare insert node: %w", err)
	}
	defer insertNode.Close()
	touchNode, err := tx.Prepare(
		"UPDATE node SET last_seen = ? WHERE id = ? AND last_seen < ?")
	if err != nil {
		return fmt.Errorf("stats: prepare touch node: %w", err)
	}
	defer touchNode.Close()
	insertSample, err := tx.Prepare(
		"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("stats: prepare insert sample: %w", err)
	}
	defer insertSample.Close()

	for _, sm := range batch {
		id, err := s.nodeID(tx, insertNode, sm.Label, sm.At)
		if err != nil {
			return err
		}
		ts := sm.At.UTC().UnixMilli()
		if _, err := touchNode.Exec(ts, id, ts); err != nil {
			return fmt.Errorf("stats: touch node %s: %w", sm.Label, err)
		}
		ok := 0
		if sm.OK {
			ok = 1
		}
		if _, err := insertSample.Exec(ts, id, int(sm.Kind), ok, sm.Delay.Milliseconds(), sm.Bytes); err != nil {
			return fmt.Errorf("stats: insert sample for %s: %w", sm.Label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats: commit batch: %w", err)
	}
	return nil
}

// nodeID returns the id of the label, creating the node row with
// first_seen = last_seen = at when it is unknown.
func (s *Store) nodeID(tx *sql.Tx, insertNode *sql.Stmt, label string, at time.Time) (int64, error) {
	if id, ok := s.ids[label]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow("SELECT id FROM node WHERE label = ?", label).Scan(&id)
	if err == nil {
		s.ids[label] = id
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("stats: select node %s: %w", label, err)
	}
	ts := at.UTC().UnixMilli()
	res, err := insertNode.Exec(label, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("stats: insert node %s: %w", label, err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("stats: node id for %s: %w", label, err)
	}
	s.ids[label] = id
	return id, nil
}

// writeNodes upserts provider/country for the labels and refreshes the
// label cache. A failure resets the cache: ids read inside a rolled-back
// transaction may be reused by other labels later.
func (s *Store) writeNodes(infos []NodeInfo) (err error) {
	defer func() {
		if err != nil {
			s.ids = map[string]int64{}
			s.accIDs = map[string]int64{}
		}
	}()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("stats: begin: %w", err)
	}
	defer tx.Rollback()
	now := s.now().UTC().UnixMilli()
	upsert, err := tx.Prepare(`
INSERT INTO node (label, provider, country, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(label) DO UPDATE SET provider = excluded.provider, country = excluded.country`)
	if err != nil {
		return fmt.Errorf("stats: prepare upsert node: %w", err)
	}
	defer upsert.Close()

	for _, info := range infos {
		if _, err := upsert.Exec(info.Label, info.Provider, info.Country, now, now); err != nil {
			return fmt.Errorf("stats: upsert node %s: %w", info.Label, err)
		}
		var id int64
		if err := tx.QueryRow("SELECT id FROM node WHERE label = ?", info.Label).Scan(&id); err != nil {
			return fmt.Errorf("stats: select node %s: %w", info.Label, err)
		}
		s.ids[info.Label] = id
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats: commit nodes: %w", err)
	}
	return nil
}
