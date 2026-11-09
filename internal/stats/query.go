package stats

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Query selects a statistics window and optional node filters.
type Query struct {
	Since    time.Duration // 0 = 24h
	Provider string        // empty = any
	Country  string
	Label    string
	Account  string // empty = any; only used by the account query
}

// NodeStats is the aggregated view of one node over the query window.
type NodeStats struct {
	Label, Provider, Country, Name       string
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
	id                             int64
	label, provider, country, name string
	lastSeen                       time.Time
}

// Nodes aggregates the samples of the window per node, sorted by label.
// A window inside the raw retention reads the exact raw samples (including
// percentiles); a longer window combines raw and hourly aggregates and reports
// no percentiles. Nodes without data in the window are omitted.
func (s *Store) Nodes(q Query) ([]NodeStats, error) {
	since := q.Since
	if since <= 0 {
		since = 24 * time.Hour
	}
	from := s.now().UTC().Add(-since)

	// Keep the node metadata and aggregates on one database snapshot, so a
	// concurrent retention fold cannot make the same samples disappear from
	// both reads or appear in both.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("stats: begin node read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	metas, err := s.filteredNodes(tx, q)
	if err != nil {
		return nil, err
	}
	return s.aggregateNodes(tx, q, since, from, metas)
}

func (s *Store) aggregateNodes(qr querier, filters Query, since time.Duration, from time.Time, metas []*nodeMeta) ([]NodeStats, error) {
	byID := make(map[int64]*nodeMeta, len(metas))
	for _, m := range metas {
		byID[m.id] = m
	}
	acc := map[int64]*NodeStats{}
	get := func(id int64) *NodeStats {
		m, ok := byID[id]
		if !ok {
			return nil
		}
		st, ok := acc[id]
		if !ok {
			st = &NodeStats{Label: m.label, Provider: m.provider, Country: m.country, Name: m.name}
			acc[id] = st
		}
		return st
	}

	means := map[int64]*nodeMeans{}
	long := since > s.opts.Retention
	if err := s.queryRaw(qr, from.UnixMilli(), filters, !long, get, means); err != nil {
		return nil, err
	}
	if long {
		if err := s.queryHourly(qr, from.Unix()/3600, filters, byID, acc, means); err != nil {
			return nil, err
		}
	}
	for id, st := range acc {
		if mean := means[id]; mean != nil {
			if mean.probeOK > 0 {
				st.DelayAvgMS = mean.probeSum / mean.probeOK
			}
			if mean.dialOK > 0 {
				st.DialAvgMS = mean.dialSum / mean.dialOK
			}
		}
		if long {
			st.DelayP50MS, st.DelayP95MS = 0, 0
			st.ThroughputP50KBps, st.ThroughputMaxKBps = 0, 0
		}
	}

	out := make([]NodeStats, 0, len(acc))
	for _, st := range acc {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

type nodeMeans struct {
	probeSum, probeOK int64
	dialSum, dialOK   int64
}

// filteredNodes loads the node rows passing the query filters.
func (s *Store) filteredNodes(q querier, filters Query) ([]*nodeMeta, error) {
	// A v1 or v2 database opened read-only has no name column.
	nameCol := s.hasName
	query := "SELECT id, label, provider, country, last_seen"
	if nameCol {
		query += ", name"
	}
	query += " FROM node WHERE 1=1"
	var args []any
	if filters.Provider != "" {
		query += " AND provider = ?"
		args = append(args, filters.Provider)
	}
	if filters.Country != "" {
		query += " AND country = ?"
		args = append(args, filters.Country)
	}
	if filters.Label != "" {
		query += " AND label = ?"
		args = append(args, filters.Label)
	}
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("stats: query nodes: %w", err)
	}
	defer rows.Close()
	var metas []*nodeMeta
	for rows.Next() {
		m := &nodeMeta{}
		var seen int64
		var err error
		if nameCol {
			err = rows.Scan(&m.id, &m.label, &m.provider, &m.country, &seen, &m.name)
		} else {
			err = rows.Scan(&m.id, &m.label, &m.provider, &m.country, &seen)
		}
		if err != nil {
			return nil, fmt.Errorf("stats: scan node: %w", err)
		}
		m.lastSeen = time.UnixMilli(seen).UTC()
		metas = append(metas, m)
	}
	return metas, rows.Err()
}

// queryRaw fills the aggregates from the exact raw samples of the window.
func (s *Store) queryRaw(q querier, fromMS int64, filters Query, includeSamples bool, get func(int64) *NodeStats, means map[int64]*nodeMeans) error {
	join, where, args := nodeWhere("sample", "ts", fromMS, filters, "sample.kind IN (?, ?)", int(KindProbe), int(KindDial))
	rows, err := q.Query(`
SELECT sample.node, sample.kind, COUNT(*), SUM(sample.ok),
       SUM(CASE WHEN sample.ok = 1 THEN sample.ms ELSE 0 END)
FROM sample `+join+where+` GROUP BY sample.node, sample.kind`, args...)
	if err != nil {
		return fmt.Errorf("stats: query counts: %w", err)
	}
	for rows.Next() {
		var id, kind, n, ok, msSum int64
		if err := rows.Scan(&id, &kind, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan counts: %w", err)
		}
		st := get(id)
		if st == nil {
			continue
		}
		mean := means[id]
		if mean == nil {
			mean = &nodeMeans{}
			means[id] = mean
		}
		if kind == int64(KindProbe) {
			st.Probes, st.ProbesOK = int(n), int(ok)
			mean.probeSum, mean.probeOK = msSum, ok
		} else {
			st.Dials, st.DialsOK = int(n), int(ok)
			mean.dialSum, mean.dialOK = msSum, ok
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query counts: %w", err)
	}
	rows.Close()

	if includeSamples {
		join, where, args = nodeWhere("sample", "ts", fromMS, filters, "sample.kind = ? AND sample.ok = 1", int(KindProbe))
		delays := map[int64][]int64{}
		if err := collectInts(q, delays, `SELECT sample.node, sample.ms FROM sample `+join+where, args...); err != nil {
			return err
		}
		for id, ms := range delays {
			if st := get(id); st != nil {
				st.DelayP50MS = percentile(ms, 50)
				st.DelayP95MS = percentile(ms, 95)
			}
		}

		join, where, args = nodeWhere("sample", "ts", fromMS, filters, "sample.kind = ? AND sample.ok = 1 AND sample.ms > 0", int(KindThroughput))
		rates := map[int64][]int64{}
		if err := collectInts(q, rates, `SELECT sample.node, sample.bytes * 1000 / sample.ms / 1024 FROM sample `+join+where, args...); err != nil {
			return err
		}
		for id, values := range rates {
			if st := get(id); st != nil {
				st.ThroughputP50KBps = percentile(values, 50)
				st.ThroughputMaxKBps = percentile(values, 100)
			}
		}
	}

	join, where, args = nodeWhere("sample", "ts", fromMS, filters, "")
	if err := scanSum(q, args, `SELECT sample.node, SUM(sample.bytes) FROM sample `+join+where+` GROUP BY sample.node`,
		func(id, v int64) {
			if st := get(id); st != nil {
				st.Bytes = v
			}
		}); err != nil {
		return err
	}
	if err := scanSum(q, args, `SELECT sample.node, MAX(sample.ts) FROM sample `+join+where+` GROUP BY sample.node`,
		func(id, v int64) {
			if st := get(id); st != nil {
				st.LastSeen = time.UnixMilli(v).UTC()
			}
		}); err != nil {
		return err
	}
	join, where, args = nodeWhere("sample", "ts", fromMS, filters, "sample.ok = 1 AND sample.kind IN (?, ?)", int(KindProbe), int(KindDial))
	return scanSum(q, args, `SELECT sample.node, MAX(sample.ts) FROM sample `+join+where+` GROUP BY sample.node`,
		func(id, v int64) {
			if st := get(id); st != nil {
				st.LastOK = time.UnixMilli(v).UTC()
			}
		})
}

// queryHourly adds the aggregates from folded hourly rows; means are combined
// with any exact raw means and percentiles remain unavailable for the window.
func (s *Store) queryHourly(q querier, fromHour int64, filters Query, byID map[int64]*nodeMeta, acc map[int64]*NodeStats, means map[int64]*nodeMeans) error {
	accFor := func(id int64) *NodeStats {
		if _, ok := byID[id]; !ok {
			return nil
		}
		st, ok := acc[id]
		if !ok {
			m := byID[id]
			st = &NodeStats{Label: m.label, Provider: m.provider, Country: m.country, Name: m.name}
			acc[id] = st
		}
		return st
	}

	join, where, args := nodeWhere("hourly", "hour", fromHour, filters, "hourly.kind IN (?, ?)", int(KindProbe), int(KindDial))
	rows, err := q.Query(`
SELECT hourly.node, hourly.kind, SUM(hourly.n), SUM(hourly.ok), SUM(hourly.ms_sum)
FROM hourly `+join+where+` GROUP BY hourly.node, hourly.kind`, args...)
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
			continue
		}
		mean := means[id]
		if mean == nil {
			mean = &nodeMeans{}
			means[id] = mean
		}
		if kind == int64(KindProbe) {
			st.Probes += int(n)
			st.ProbesOK += int(ok)
			mean.probeSum += msSum
			mean.probeOK += ok
		} else {
			st.Dials += int(n)
			st.DialsOK += int(ok)
			mean.dialSum += msSum
			mean.dialOK += ok
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	rows.Close()

	join, where, args = nodeWhere("hourly", "hour", fromHour, filters, "")
	if err := scanSum(q, args, `SELECT hourly.node, SUM(hourly.bytes) FROM hourly `+join+where+` GROUP BY hourly.node`,
		func(id, v int64) {
			if st := accFor(id); st != nil {
				st.Bytes += v
			}
		}); err != nil {
		return err
	}
	if err := scanSum(q, args, `SELECT hourly.node, MAX(hourly.hour) FROM hourly `+join+where+` GROUP BY hourly.node`,
		func(id, v int64) {
			if st := accFor(id); st != nil {
				at := time.Unix(v*3600, 0).UTC()
				if at.After(st.LastOK) {
					st.LastOK = at
				}
			}
		}); err != nil {
		return err
	}
	for id, st := range acc {
		if seen := byID[id].lastSeen; seen.After(st.LastSeen) {
			st.LastSeen = seen
		}
	}
	return nil
}

// nodeWhere builds the time and node-filter predicates for measurement SQL.
func nodeWhere(table, timeColumn string, from int64, filters Query, extra string, extraArgs ...any) (string, string, []any) {
	join, predicates, args := nodeFilter(table, filters)
	predicates = append(predicates, fmt.Sprintf("%s.%s >= ?", table, timeColumn))
	args = append(args, from)
	if extra != "" {
		predicates = append(predicates, extra)
		args = append(args, extraArgs...)
	}
	return join, " WHERE " + strings.Join(predicates, " AND "), args
}

// collectInts runs query with args and appends the second column per node id.
func collectInts(q querier, out map[int64][]int64, query string, args ...any) error {
	rows, err := q.Query(query, args...)
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

// scanSum runs a (node, value) query and applies fn per row.
func scanSum(q querier, args []any, query string, fn func(id, v int64)) error {
	rows, err := q.Query(query, args...)
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
