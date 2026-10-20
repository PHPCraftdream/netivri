package stats

import (
	"fmt"
	"sort"
	"time"
)

// Query selects a statistics window and optional node filters.
type Query struct {
	Since    time.Duration // 0 = 24h
	Provider string        // empty = any
	Country  string
	Label    string
}

// NodeStats is the aggregated view of one node over the query window.
type NodeStats struct {
	Label, Provider, Country             string
	Probes, ProbesOK                     int
	DelayAvgMS, DelayP50MS, DelayP95MS   int64
	Dials, DialsOK                       int
	DialAvgMS                            int64
	ThroughputP50KBps, ThroughputMaxKBps int64 // per window, KiB/s
	Bytes                                int64
	LastSeen, LastOK                     time.Time // zero when never
}

// nodeMeta is the identity of a node row passing the query filters.
type nodeMeta struct {
	id                       int64
	label, provider, country string
	lastSeen                 time.Time
}

// Nodes aggregates the samples of the window per node, sorted by label.
// A window inside the raw retention reads the exact raw samples (including
// percentiles); a longer window reads the hourly table and reports no
// percentiles. Nodes without data in the window are omitted.
func (s *Store) Nodes(q Query) ([]NodeStats, error) {
	since := q.Since
	if since <= 0 {
		since = 24 * time.Hour
	}
	from := s.now().UTC().Add(-since)

	metas, err := s.filteredNodes(q)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*nodeMeta, len(metas))
	for _, m := range metas {
		byID[m.id] = m
	}
	// The aggregation queries read all nodes; only nodes that pass the
	// filters and have data in the window end up in acc.
	acc := map[int64]*NodeStats{}
	get := func(id int64) *NodeStats {
		m, ok := byID[id]
		if !ok {
			return nil // the node does not pass the filters
		}
		st, ok := acc[id]
		if !ok {
			st = &NodeStats{Label: m.label, Provider: m.provider, Country: m.country}
			acc[id] = st
		}
		return st
	}

	if since <= s.opts.Retention {
		err = s.queryRaw(from.UnixMilli(), get)
	} else {
		err = s.queryHourly(from.Unix()/3600, byID, acc)
	}
	if err != nil {
		return nil, err
	}

	out := make([]NodeStats, 0, len(acc))
	for _, st := range acc {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// filteredNodes loads the node rows passing the query filters.
func (s *Store) filteredNodes(q Query) ([]*nodeMeta, error) {
	query := "SELECT id, label, provider, country, last_seen FROM node WHERE 1=1"
	var args []any
	if q.Provider != "" {
		query += " AND provider = ?"
		args = append(args, q.Provider)
	}
	if q.Country != "" {
		query += " AND country = ?"
		args = append(args, q.Country)
	}
	if q.Label != "" {
		query += " AND label = ?"
		args = append(args, q.Label)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("stats: query nodes: %w", err)
	}
	defer rows.Close()
	var metas []*nodeMeta
	for rows.Next() {
		m := &nodeMeta{}
		var seen int64
		if err := rows.Scan(&m.id, &m.label, &m.provider, &m.country, &seen); err != nil {
			return nil, fmt.Errorf("stats: scan node: %w", err)
		}
		m.lastSeen = time.UnixMilli(seen).UTC()
		metas = append(metas, m)
	}
	return metas, rows.Err()
}

// queryRaw fills the aggregates from the exact raw samples of the window.
func (s *Store) queryRaw(fromMS int64, get func(int64) *NodeStats) error {
	// Counts per kind.
	rows, err := s.db.Query(`
SELECT node, kind, COUNT(*), SUM(ok) FROM sample
WHERE ts >= ? AND kind IN (?, ?) GROUP BY node, kind`, fromMS, int(KindProbe), int(KindDial))
	if err != nil {
		return fmt.Errorf("stats: query counts: %w", err)
	}
	for rows.Next() {
		var id, kind, n, ok int64
		if err := rows.Scan(&id, &kind, &n, &ok); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan counts: %w", err)
		}
		st := get(id)
		if st == nil {
			continue // filtered out
		}
		if kind == int64(KindProbe) {
			st.Probes, st.ProbesOK = int(n), int(ok)
		} else {
			st.Dials, st.DialsOK = int(n), int(ok)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query counts: %w", err)
	}
	rows.Close()

	// Delay of each OK probe; percentiles are computed in Go.
	delays := map[int64][]int64{}
	if err := s.collectInts(delays, `
SELECT node, ms FROM sample WHERE ts >= ? AND kind = ? AND ok = 1`,
		fromMS, int(KindProbe)); err != nil {
		return err
	}
	for id, ms := range delays {
		if st := get(id); st != nil {
			st.DelayAvgMS = avgMS(ms)
			st.DelayP50MS = percentile(ms, 50)
			st.DelayP95MS = percentile(ms, 95)
		}
	}

	// Dial time of each OK dial.
	dials := map[int64][]int64{}
	if err := s.collectInts(dials, `
SELECT node, ms FROM sample WHERE ts >= ? AND kind = ? AND ok = 1`,
		fromMS, int(KindDial)); err != nil {
		return err
	}
	for id, ms := range dials {
		if st := get(id); st != nil {
			st.DialAvgMS = avgMS(ms)
		}
	}

	// Throughput rate of each window, KiB/s.
	rates := map[int64][]int64{}
	if err := s.collectInts(rates, `
SELECT node, bytes * 1000 / ms / 1024 FROM sample
WHERE ts >= ? AND kind = ? AND ok = 1 AND ms > 0`,
		fromMS, int(KindThroughput)); err != nil {
		return err
	}
	for id, r := range rates {
		if st := get(id); st != nil {
			st.ThroughputP50KBps = percentile(r, 50)
			st.ThroughputMaxKBps = percentile(r, 100)
		}
	}

	// Total bytes, latest sample and latest OK probe or dial of the window.
	if err := s.scanSum(fromMS, "SELECT node, SUM(bytes) FROM sample WHERE ts >= ? GROUP BY node",
		func(id, v int64) {
			if st := get(id); st != nil {
				st.Bytes = v
			}
		}); err != nil {
		return err
	}
	if err := s.scanSum(fromMS, "SELECT node, MAX(ts) FROM sample WHERE ts >= ? GROUP BY node",
		func(id, v int64) {
			if st := get(id); st != nil {
				st.LastSeen = time.UnixMilli(v).UTC()
			}
		}); err != nil {
		return err
	}
	return s.scanSum(fromMS, `
SELECT node, MAX(ts) FROM sample WHERE ts >= ? AND ok = 1 AND kind IN (?, ?) GROUP BY node`,
		func(id, v int64) {
			if st := get(id); st != nil {
				st.LastOK = time.UnixMilli(v).UTC()
			}
		},
		int(KindProbe), int(KindDial))
}

// queryHourly fills the aggregates from the hourly table of the window;
// percentiles and throughput are not available there.
func (s *Store) queryHourly(fromHour int64, byID map[int64]*nodeMeta, acc map[int64]*NodeStats) error {
	// accFor creates the accumulator for a known node on first use, so a
	// node with only throughput rows is reported too.
	accFor := func(id int64) *NodeStats {
		if _, ok := byID[id]; !ok {
			return nil // filtered out
		}
		st, ok := acc[id]
		if !ok {
			m := byID[id]
			st = &NodeStats{Label: m.label, Provider: m.provider, Country: m.country}
			acc[id] = st
		}
		return st
	}

	rows, err := s.db.Query(`
SELECT node, kind, SUM(n), SUM(ok), SUM(ms_sum) FROM hourly
WHERE hour >= ? AND kind IN (?, ?) GROUP BY node, kind`, fromHour, int(KindProbe), int(KindDial))
	if err != nil {
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	for rows.Next() {
		var id, kind, n, ok, msSum int64
		if err := rows.Scan(&id, &kind, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan hourly: %w", err)
		}
		st := accFor(id)
		if st == nil {
			continue // filtered out
		}
		avg := int64(0)
		if ok > 0 {
			avg = msSum / ok
		}
		if kind == int64(KindProbe) {
			st.Probes, st.ProbesOK, st.DelayAvgMS = int(n), int(ok), avg
		} else {
			st.Dials, st.DialsOK, st.DialAvgMS = int(n), int(ok), avg
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	rows.Close()

	if err := s.scanSum(fromHour, "SELECT node, SUM(bytes) FROM hourly WHERE hour >= ? GROUP BY node",
		func(id, v int64) {
			if st := accFor(id); st != nil {
				st.Bytes = v
			}
		}); err != nil {
		return err
	}
	// LastOK is the latest hourly row; LastSeen comes from node.last_seen.
	if err := s.scanSum(fromHour, "SELECT node, MAX(hour) FROM hourly WHERE hour >= ? GROUP BY node",
		func(id, v int64) {
			if st := accFor(id); st != nil {
				st.LastOK = time.Unix(v*3600, 0).UTC()
			}
		}); err != nil {
		return err
	}
	for id, st := range acc {
		st.LastSeen = byID[id].lastSeen
	}
	return nil
}

// collectInts runs query with args and appends the second column per node id.
func (s *Store) collectInts(out map[int64][]int64, query string, args ...any) error {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("stats: query samples: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v int64
		if err := rows.Scan(&id, &v); err != nil {
			return fmt.Errorf("stats: scan samples: %w", err)
		}
		out[id] = append(out[id], v)
	}
	return rows.Err()
}

// scanSum runs a (node, value) query whose first parameter is the window
// start and applies fn per row.
func (s *Store) scanSum(from int64, query string, fn func(id, v int64), args ...any) error {
	all := append([]any{from}, args...)
	rows, err := s.db.Query(query, all...)
	if err != nil {
		return fmt.Errorf("stats: query aggregate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v int64
		if err := rows.Scan(&id, &v); err != nil {
			return fmt.Errorf("stats: scan aggregate: %w", err)
		}
		fn(id, v)
	}
	return rows.Err()
}

// avgMS is the truncating integer mean.
func avgMS(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	var sum int64
	for _, x := range v {
		sum += x
	}
	return sum / int64(len(v))
}

// percentile is the nearest rank on the sorted slice: index
// ceil(p/100*n)-1.
func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	sorted := make([]int64, len(v))
	copy(sorted, v)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}
