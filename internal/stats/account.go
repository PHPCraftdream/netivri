package stats

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// querier is the subset of *sql.DB/*sql.Tx the account queries need.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// AccountSample is one measurement for a login. Bytes is the total moved in
// the window (up plus down); Up and Down are only meaningful for
// KindThroughput.
type AccountSample struct {
	At    time.Time
	Login string
	Label string // node label
	Kind  Kind
	OK    bool
	Delay time.Duration
	Bytes int64
	Up    int64
	Down  int64
}

// AccountStats is the aggregated per-account view over the query window.
type AccountStats struct {
	Login                string
	BytesUp, BytesDown   int64
	Dials, DialsOK       int
	DialAvgMS            int64
	Nodes                int
	Countries, Providers []string
	LastSeen             time.Time

	// msSum and msOK carry the weighted dial-time sum across the raw and
	// hourly reads of a combined window; DialAvgMS is derived from them.
	msSum, msOK int64
}

// trimPendingAcct bounds the account retry buffer like trimPending bounds
// the node one.
func (s *Store) trimPendingAcct(batch []AccountSample) []AccountSample {
	if len(batch) <= maxPendingSamples {
		return batch
	}
	s.dropped.Add(uint64(len(batch) - maxPendingSamples))
	keep := batch[len(batch)-maxPendingSamples:]
	out := make([]AccountSample, len(keep))
	copy(out, keep)
	return out
}

// writeAccountBatch inserts the account samples and creates or touches their
// account and node rows in one transaction. Called only from the writer
// goroutine, so the id caches need no lock.
func (s *Store) writeAccountBatch(batch []AccountSample) error {
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
	insertAccount, err := tx.Prepare(
		"INSERT INTO account (login, first_seen, last_seen) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("stats: prepare insert account: %w", err)
	}
	defer insertAccount.Close()
	touchAccount, err := tx.Prepare(
		"UPDATE account SET last_seen = ? WHERE id = ? AND last_seen < ?")
	if err != nil {
		return fmt.Errorf("stats: prepare touch account: %w", err)
	}
	defer touchAccount.Close()
	insertSample, err := tx.Prepare(
		"INSERT INTO account_sample (ts, account, node, kind, ok, ms, bytes, up, down) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("stats: prepare insert account sample: %w", err)
	}
	defer insertSample.Close()

	for _, sm := range batch {
		accID, err := s.accountID(tx, insertAccount, sm.Login, sm.At)
		if err != nil {
			return err
		}
		nodeID, err := s.nodeID(tx, insertNode, sm.Label, sm.At)
		if err != nil {
			return err
		}
		ts := sm.At.UTC().UnixMilli()
		if _, err := touchAccount.Exec(ts, accID, ts); err != nil {
			return fmt.Errorf("stats: touch account %s: %w", sm.Login, err)
		}
		if _, err := touchNode.Exec(ts, nodeID, ts); err != nil {
			return fmt.Errorf("stats: touch node %s: %w", sm.Label, err)
		}
		ok := 0
		if sm.OK {
			ok = 1
		}
		if _, err := insertSample.Exec(ts, accID, nodeID, int(sm.Kind), ok,
			sm.Delay.Milliseconds(), sm.Bytes, sm.Up, sm.Down); err != nil {
			return fmt.Errorf("stats: insert account sample for %s: %w", sm.Login, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats: commit account batch: %w", err)
	}
	return nil
}

// accountID returns the id of the login, creating the account row with
// first_seen = last_seen = at when it is unknown.
func (s *Store) accountID(tx *sql.Tx, insertAccount *sql.Stmt, login string, at time.Time) (int64, error) {
	if id, ok := s.accIDs[login]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow("SELECT id FROM account WHERE login = ?", login).Scan(&id)
	if err == nil {
		s.accIDs[login] = id
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("stats: select account %s: %w", login, err)
	}
	ts := at.UTC().UnixMilli()
	res, err := insertAccount.Exec(login, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("stats: insert account %s: %w", login, err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("stats: account id for %s: %w", login, err)
	}
	s.accIDs[login] = id
	return id, nil
}

// Accounts aggregates the per-account samples of the window per login,
// sorted by login. A window inside the raw retention reads the exact raw
// samples; a longer window reads BOTH tables over the same window start:
// account_hourly covers the folded past and account_sample the raw samples
// newer than the retention boundary (on a fresh database a long window
// would otherwise report nothing). This cannot double count because the
// retention fold moves every sample into exactly one of the two tables —
// it inserts into account_hourly and deletes the raw rows in one
// transaction — and the reads of one query call run in one read
// transaction, so they see a single snapshot of the database and a
// concurrent retention fold is either wholly before or wholly after it. A
// folded sample's hour is always >= the window's start hour, matching the
// hourly boundary the node query uses. The snapshot relies on the database
// being in WAL mode, which the writer sets at open; a read transaction on a
// WAL database sees one consistent snapshot even on the read-only
// connection, so a plain Begin is used (BeginTx with ReadOnly:true is not
// needed and may not be supported by the driver). On a database that has
// not been migrated to schema version 2 it returns an empty slice, so
// `netivri stats` keeps working on an old database.
func (s *Store) Accounts(q Query) ([]AccountStats, error) {
	if s.version < 2 {
		return []AccountStats{}, nil
	}
	since := q.Since
	if since <= 0 {
		since = 24 * time.Hour
	}
	now := s.now().UTC()
	from := now.Add(-since)

	acc := map[int64]*AccountStats{}
	get := func(id int64) *AccountStats {
		st, ok := acc[id]
		if !ok {
			st = &AccountStats{}
			acc[id] = st
		}
		return st
	}

	// Begin the snapshot first, so every read below sees one consistent
	// snapshot of the database. A read transaction is only rolled back, never
	// committed: the deferred Rollback is the intended end of it.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("stats: begin account read: %w", err)
	}
	defer tx.Rollback()

	// A long window must also see the raw samples the fold has not reached
	// yet, so both tables are read and accumulated into the same stats.
	long := since > s.opts.Retention
	err = accountsRaw(tx, from.UnixMilli(), q, get)
	if err == nil && long {
		err = accountsHourly(tx, from.Unix()/3600, q, get)
	}
	if err != nil {
		return nil, err
	}

	// Logins for the collected ids, plus the Nodes/Countries/Providers
	// breakdown from the node metadata of the window.
	logins, err := accountLogins(tx)
	if err != nil {
		return nil, err
	}
	tables := []string{"account_sample"}
	if long {
		tables = append(tables, "account_hourly")
	}
	nodeIDs, err := accountNodeIDs(tx, from, tables, q)
	if err != nil {
		return nil, err
	}
	for id, st := range acc {
		if st.msOK > 0 {
			st.DialAvgMS = st.msSum / st.msOK
		}
		st.Login = logins[id]
		if st.Login == "" {
			st.Login = fmt.Sprintf("#%d", id)
		}
		ids := nodeIDs[id]
		st.Nodes = len(ids)
		countries, providers, err := nodeCountriesProviders(tx, ids)
		if err != nil {
			return nil, err
		}
		st.Countries = countries
		st.Providers = providers
	}
	out := make([]AccountStats, 0, len(acc))
	for _, st := range acc {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out, nil
}

// nodeFilter returns the join and predicates restricting the rows to
// samples recorded on nodes matching the query's provider/country/label
// filters; it is empty when no filter is set.
func nodeFilter(table string, q Query) (join string, preds []string, args []any) {
	if q.Provider == "" && q.Country == "" && q.Label == "" {
		return "", nil, nil
	}
	join = fmt.Sprintf("JOIN node nf ON nf.id = %s.node", table)
	if q.Provider != "" {
		preds = append(preds, "nf.provider = ?")
		args = append(args, q.Provider)
	}
	if q.Country != "" {
		preds = append(preds, "nf.country = ?")
		args = append(args, q.Country)
	}
	if q.Label != "" {
		preds = append(preds, "nf.label = ?")
		args = append(args, q.Label)
	}
	return join, preds, args
}

// accountWhere returns the join, the WHERE prefix and the leading args
// (login first, then node filters) shared by the account queries. The login
// filter lives in the join; the node filters restrict the rows to samples
// of matching nodes.
func accountWhere(table, tsCol, login string, q Query) (join, pred string, args []any) {
	nfJoin, preds, nfArgs := nodeFilter(table, q)
	join = nfJoin
	if login != "" {
		join += fmt.Sprintf(" JOIN account a ON a.id = %s.account AND a.login = ?",
			table)
		args = append(args, login)
	}
	args = append(args, nfArgs...)
	preds = append(preds, fmt.Sprintf("%s.%s >= ?", table, tsCol))
	pred = "WHERE " + strings.Join(preds, " AND ")
	return join, pred, args
}

// accountsRaw adds the aggregates of the exact raw account samples.
func accountsRaw(q querier, fromMS int64, qy Query, get func(int64) *AccountStats) error {
	join, pred, args := accountWhere("account_sample", "ts", qy.Account, qy)
	args = append(args, fromMS)

	rows, err := q.Query(`
SELECT account_sample.account, COUNT(*), SUM(ok), SUM(CASE WHEN ok = 1 THEN ms ELSE 0 END)
FROM account_sample `+join+` `+pred+`
AND kind = ? GROUP BY account_sample.account`, append(args[:len(args):len(args)], int(KindDial))...)
	if err != nil {
		return fmt.Errorf("stats: query counts: %w", err)
	}
	for rows.Next() {
		var id, n, ok, msSum int64
		if err := rows.Scan(&id, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan counts: %w", err)
		}
		st := get(id)
		st.Dials += int(n)
		st.DialsOK += int(ok)
		st.msSum += msSum
		st.msOK += ok
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query counts: %w", err)
	}
	rows.Close()

	if err := scanAccountSum(q, args, `
SELECT account_sample.account, SUM(up), SUM(down) FROM account_sample `+join+` `+pred+`
GROUP BY account_sample.account`,
		func(id, up, down int64) {
			st := get(id)
			st.BytesUp += up
			st.BytesDown += down
		}); err != nil {
		return err
	}
	return scanAccountPair(q, args, `
SELECT account_sample.account, MAX(ts) FROM account_sample `+join+` `+pred+`
GROUP BY account_sample.account`,
		func(id, ts int64) {
			accountSeen(get(id), time.UnixMilli(ts).UTC())
		})
}

// accountSeen keeps the latest of the reported times as LastSeen; the raw
// and hourly reads of a combined window must not overwrite each other.
func accountSeen(st *AccountStats, at time.Time) {
	if at.After(st.LastSeen) {
		st.LastSeen = at
	}
}

// accountsHourly adds the aggregates of the per-account hourly table.
func accountsHourly(q querier, fromHour int64, qy Query, get func(int64) *AccountStats) error {
	join, pred, args := accountWhere("account_hourly", "hour", qy.Account, qy)
	args = append(args, fromHour)

	rows, err := q.Query(`
SELECT account_hourly.account, SUM(n), SUM(ok), SUM(ms_sum) FROM account_hourly `+join+` `+pred+`
AND kind = ? GROUP BY account_hourly.account`,
		append(args[:len(args):len(args)], int(KindDial))...)
	if err != nil {
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	for rows.Next() {
		var id, n, ok, msSum int64
		if err := rows.Scan(&id, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan hourly: %w", err)
		}
		st := get(id)
		st.Dials += int(n)
		st.DialsOK += int(ok)
		st.msSum += msSum
		st.msOK += ok
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	rows.Close()

	if err := scanAccountSum(q, args, `
SELECT account_hourly.account, SUM(up), SUM(down) FROM account_hourly `+join+` `+pred+`
GROUP BY account_hourly.account`,
		func(id, up, down int64) {
			st := get(id)
			st.BytesUp += up
			st.BytesDown += down
		}); err != nil {
		return err
	}
	return scanAccountPair(q, args, `
SELECT account_hourly.account, MAX(hour) FROM account_hourly `+join+` `+pred+`
GROUP BY account_hourly.account`,
		func(id, hour int64) {
			accountSeen(get(id), time.Unix(hour*3600, 0).UTC())
		})
}

// scanAccountPair runs a (account, value) query and applies fn per row.
func scanAccountPair(q querier, args []any, query string, fn func(id, v int64)) error {
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

// scanAccountSum runs a query whose first columns are the account id and
// two values and applies fn per row.
func scanAccountSum(q querier, args []any, query string, fn func(id, v, w int64)) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return fmt.Errorf("stats: query aggregate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v, w int64
		if err := rows.Scan(&id, &v, &w); err != nil {
			return fmt.Errorf("stats: scan aggregate: %w", err)
		}
		fn(id, v, w)
	}
	return rows.Err()
}

// accountLogins maps account ids to logins.
func accountLogins(q querier) (map[int64]string, error) {
	rows, err := q.Query("SELECT id, login FROM account")
	if err != nil {
		return nil, fmt.Errorf("stats: query accounts: %w", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			return nil, fmt.Errorf("stats: scan account: %w", err)
		}
		out[id] = login
	}
	return out, rows.Err()
}

// accountNodeIDs returns the distinct node ids seen per account in the
// window, unioned over the given tables (raw uses milliseconds, hourly
// whole hours). The node filters restrict which samples count, but the
// Countries/Providers still come from the metadata of the surviving nodes.
func accountNodeIDs(q querier, from time.Time, tables []string, qy Query) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	seen := map[int64]map[int64]bool{}
	for _, table := range tables {
		tsCol, fromV := "ts", from.UnixMilli()
		if table == "account_hourly" {
			tsCol, fromV = "hour", from.Unix()/3600
		}
		join, pred, args := accountWhere(table, tsCol, qy.Account, qy)
		args = append(args, fromV)
		rows, err := q.Query(`
SELECT `+table+`.account, `+table+`.node FROM `+table+` `+join+` `+pred+`
GROUP BY `+table+`.account, `+table+`.node`, args...)
		if err != nil {
			return nil, fmt.Errorf("stats: query account nodes: %w", err)
		}
		for rows.Next() {
			var acc, node int64
			if err := rows.Scan(&acc, &node); err != nil {
				rows.Close()
				return nil, fmt.Errorf("stats: scan account nodes: %w", err)
			}
			if seen[acc] == nil {
				seen[acc] = map[int64]bool{}
			}
			if !seen[acc][node] {
				seen[acc][node] = true
				out[acc] = append(out[acc], node)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("stats: query account nodes: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// nodeCountriesProviders maps the node ids to sorted deduplicated country
// and provider lists.
func nodeCountriesProviders(q querier, ids []int64) ([]string, []string, error) {
	if len(ids) == 0 {
		return []string{}, []string{}, nil
	}
	countries := map[string]bool{}
	providers := map[string]bool{}
	for _, id := range ids {
		var country, provider string
		if err := q.QueryRow("SELECT country, provider FROM node WHERE id = ?", id).
			Scan(&country, &provider); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, nil, fmt.Errorf("stats: query node %d: %w", id, err)
		}
		if country != "" {
			countries[country] = true
		}
		if provider != "" {
			providers[provider] = true
		}
	}
	return sortedKeys(countries), sortedKeys(providers), nil
}

// sortedKeys returns the sorted keys of a string set; empty stays empty.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AccountNodes is a subset of the node view built from the account samples
// only: it reports per node the dials and dial outcomes of real connections
// (dials, dial_ok_pct, dial_ms_avg) and the moved bytes (up plus down
// combined). Account samples carry no probes and no throughput rates, so
// probes, the delay percentiles and the throughput rates are always 0 and
// uptime_pct is null in the rendered output. The Account filter selects one
// login, the Provider/Country/Label filters restrict the rows to samples on
// matching nodes. The same window logic as Accounts applies: a window inside
// the raw retention reads the raw samples, a longer window reads BOTH
// tables over the same window start. The reads run in one read transaction,
// so they see a single snapshot of the database and a concurrent retention
// fold is either wholly before or wholly after it — a sample is never
// counted in both tables. Rows are sorted by label; on a
// schema v1 database it returns an empty slice.
func (s *Store) AccountNodes(q Query) ([]NodeStats, error) {
	if s.version < 2 {
		return []NodeStats{}, nil
	}
	since := q.Since
	if since <= 0 {
		since = 24 * time.Hour
	}
	now := s.now().UTC()
	from := now.Add(-since)

	acc := map[int64]*accountNodeAgg{}
	get := func(id int64) *accountNodeAgg {
		st, ok := acc[id]
		if !ok {
			st = &accountNodeAgg{}
			acc[id] = st
		}
		return st
	}

	long := since > s.opts.Retention
	// Same snapshot logic as Accounts: one read transaction, so the raw and
	// hourly reads plus the node metadata lookups see one consistent
	// snapshot. Rolled back, never committed.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("stats: begin account nodes read: %w", err)
	}
	defer tx.Rollback()
	if err := accountNodesRaw(tx, from.UnixMilli(), q, get); err != nil {
		return nil, err
	}
	if long {
		if err := accountNodesHourly(tx, from.Unix()/3600, q, get); err != nil {
			return nil, err
		}
	}

	out := make([]NodeStats, 0, len(acc))
	// A v1 or v2 database opened read-only has no name column.
	nameCol := s.hasName
	for id, agg := range acc {
		var label, provider, country, name string
		var nodeSeen int64
		sel := "SELECT label, provider, country, last_seen"
		if nameCol {
			sel += ", name"
		}
		sel += " FROM node WHERE id = ?"
		var err error
		if nameCol {
			err = tx.QueryRow(sel, id).Scan(&label, &provider, &country, &nodeSeen, &name)
		} else {
			err = tx.QueryRow(sel, id).Scan(&label, &provider, &country, &nodeSeen)
		}
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stats: query node %d: %w", id, err)
		}
		st := NodeStats{
			Label: label, Provider: provider, Country: country, Name: name,
			Dials: agg.dials, DialsOK: agg.dialsOK,
			Bytes:    agg.bytes,
			LastSeen: agg.lastSeen,
		}
		if agg.msOK > 0 {
			st.DialAvgMS = agg.msSum / agg.msOK
		}
		if st.LastSeen.IsZero() {
			st.LastSeen = time.UnixMilli(nodeSeen).UTC()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// accountNodeAgg accumulates one node's account-sample aggregates across the
// raw and hourly reads of a combined window.
type accountNodeAgg struct {
	dials, dialsOK int
	msSum, msOK    int64
	bytes          int64
	lastSeen       time.Time
}

// accountNodesRaw adds the per-node aggregates of the raw account samples.
func accountNodesRaw(q querier, fromMS int64, qy Query, get func(int64) *accountNodeAgg) error {
	join, pred, args := accountWhere("account_sample", "ts", qy.Account, qy)
	args = append(args, fromMS)

	rows, err := q.Query(`
SELECT account_sample.node, COUNT(*), SUM(ok), SUM(CASE WHEN ok = 1 THEN ms ELSE 0 END)
FROM account_sample `+join+` `+pred+`
AND kind = ? GROUP BY account_sample.node`, append(args[:len(args):len(args)], int(KindDial))...)
	if err != nil {
		return fmt.Errorf("stats: query counts: %w", err)
	}
	for rows.Next() {
		var id, n, ok, msSum int64
		if err := rows.Scan(&id, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan counts: %w", err)
		}
		agg := get(id)
		agg.dials += int(n)
		agg.dialsOK += int(ok)
		agg.msSum += msSum
		agg.msOK += ok
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query counts: %w", err)
	}
	rows.Close()

	return scanAccountNodePair(q, args, `
SELECT account_sample.node, SUM(up) + SUM(down), MAX(ts) FROM account_sample `+join+` `+pred+`
GROUP BY account_sample.node`,
		false,
		func(id, bytes int64, at time.Time) {
			agg := get(id)
			agg.bytes += bytes
			if at.After(agg.lastSeen) {
				agg.lastSeen = at
			}
		})
}

// accountNodesHourly adds the per-node aggregates of the account hourly table.
func accountNodesHourly(q querier, fromHour int64, qy Query, get func(int64) *accountNodeAgg) error {
	join, pred, args := accountWhere("account_hourly", "hour", qy.Account, qy)
	args = append(args, fromHour)

	rows, err := q.Query(`
SELECT account_hourly.node, SUM(n), SUM(ok), SUM(ms_sum) FROM account_hourly `+join+` `+pred+`
AND kind = ? GROUP BY account_hourly.node`,
		append(args[:len(args):len(args)], int(KindDial))...)
	if err != nil {
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	for rows.Next() {
		var id, n, ok, msSum int64
		if err := rows.Scan(&id, &n, &ok, &msSum); err != nil {
			rows.Close()
			return fmt.Errorf("stats: scan hourly: %w", err)
		}
		agg := get(id)
		agg.dials += int(n)
		agg.dialsOK += int(ok)
		agg.msSum += msSum
		agg.msOK += ok
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("stats: query hourly: %w", err)
	}
	rows.Close()

	return scanAccountNodePair(q, args, `
SELECT account_hourly.node, SUM(up) + SUM(down), MAX(hour) FROM account_hourly `+join+` `+pred+`
GROUP BY account_hourly.node`,
		true,
		func(id, bytes int64, at time.Time) {
			agg := get(id)
			agg.bytes += bytes
			if at.After(agg.lastSeen) {
				agg.lastSeen = at
			}
		})
}

// scanAccountNodePair runs a (node, value, time) query and applies fn per
// row; the time column is milliseconds for the raw table and whole hours for
// the hourly one.
func scanAccountNodePair(q querier, args []any, query string, hourly bool, fn func(id, v int64, at time.Time)) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return fmt.Errorf("stats: query aggregate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v, t int64
		if err := rows.Scan(&id, &v, &t); err != nil {
			return fmt.Errorf("stats: scan aggregate: %w", err)
		}
		at := time.UnixMilli(t).UTC()
		if hourly {
			at = time.Unix(t*3600, 0).UTC()
		}
		fn(id, v, at)
	}
	return rows.Err()
}
