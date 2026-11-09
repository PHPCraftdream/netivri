package stats

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPruneFoldsAndDeletes(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.Retention = 48 * time.Hour })
	// Hour-aligned: old and old+1s must fall into the same hourly row.
	old := base.Add(-72 * time.Hour).Truncate(time.Hour)
	oldHour := old.Unix() / 3600
	for _, sm := range []Sample{
		probe(old, "p1/a", true, 100),
		probe(old.Add(time.Second), "p1/a", true, 300),
		probe(base.Add(-70*time.Hour), "p1/a", false, 50),
		probe(base.Add(-time.Hour), "p1/a", true, 200),
	} {
		s.Record(sm)
	}
	mustFlush(t, s)
	// An hourly row that is already past the 90 day horizon.
	if _, err := s.db.Exec(
		"INSERT INTO hourly (hour, node, kind, n, ok, ms_sum, ms_max, bytes) VALUES (?, 1, 1, 1, 1, 0, 0, 0)",
		oldHour-90*24); err != nil {
		t.Fatalf("insert stale hourly: %v", err)
	}

	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}

	raw := openRaw(t, s.opts.Path)
	if n := countRows(t, raw, "sample"); n != 1 {
		t.Fatalf("sample rows after prune = %d, want 1 (the recent one)", n)
	}
	// Counts preserved: 3 probes folded (2 ok), sum 400, max 300, split by
	// hour: the failed probe sits in its own hour row.
	var n, ok, msSum, msMax int64
	if err := raw.QueryRow(
		"SELECT n, ok, ms_sum, ms_max FROM hourly WHERE hour = ? AND kind = 1", oldHour,
	).Scan(&n, &ok, &msSum, &msMax); err != nil {
		t.Fatalf("folded hourly row: %v", err)
	}
	if n != 2 || ok != 2 || msSum != 400 || msMax != 300 {
		t.Fatalf("folded row = n %d ok %d ms_sum %d ms_max %d, want 2/2/400/300", n, ok, msSum, msMax)
	}
	var stale int64
	if err := raw.QueryRow("SELECT COUNT(*) FROM hourly WHERE hour < ?",
		base.Unix()/3600-90*24).Scan(&stale); err != nil {
		t.Fatalf("count stale hourly: %v", err)
	}
	if stale != 0 {
		t.Fatalf("stale hourly rows = %d, want 0", stale)
	}
}

func TestLongWindowReadsHourly(t *testing.T) {
	s := newTestStore(t, nil)
	// Hour-aligned: the five samples span 4 s and must stay in one hour.
	at := base.Add(-time.Hour).Truncate(time.Hour)
	for _, sm := range []Sample{
		probe(at, "p1/a", true, 100),
		probe(at.Add(time.Second), "p1/a", true, 300),
		probe(at.Add(2*time.Second), "p1/a", false, 50),
		dial(at.Add(3*time.Second), "p1/a", true, 200),
		throughput(at.Add(4*time.Second), "p1/a", 20480, 1000),
	} {
		s.Record(sm)
	}
	mustFlush(t, s)
	// Move the clock past the retention and prune: every sample is folded
	// into hourly and the raw rows are deleted.
	if err := s.prune(base.Add(8 * 24 * time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	raw := openRaw(t, s.opts.Path)
	if n := countRows(t, raw, "sample"); n != 0 {
		t.Fatalf("sample rows after prune = %d, want 0", n)
	}

	got, err := s.Nodes(Query{Since: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Probes != 3 || a.ProbesOK != 2 || a.DelayAvgMS != 200 {
		t.Fatalf("hourly probes = %d/%d avg %d, want 3/2 avg 200", a.Probes, a.ProbesOK, a.DelayAvgMS)
	}
	if a.DelayP50MS != 0 || a.DelayP95MS != 0 {
		t.Fatalf("percentiles on the hourly path = %d/%d, want 0/0", a.DelayP50MS, a.DelayP95MS)
	}
	if a.Dials != 1 || a.DialsOK != 1 || a.DialAvgMS != 200 {
		t.Fatalf("hourly dials = %+v", a)
	}
	if a.ThroughputP50KBps != 0 || a.ThroughputMaxKBps != 0 {
		t.Fatalf("throughput on the hourly path = %+v", a)
	}
	if a.Bytes != 20480 {
		t.Fatalf("bytes = %d, want 20480", a.Bytes)
	}
	if !a.LastSeen.Equal(at.Add(4 * time.Second)) {
		t.Fatalf("LastSeen = %v, want %v (node.last_seen)", a.LastSeen, at.Add(4*time.Second))
	}
	if !a.LastOK.Equal(time.Unix((at.Add(3*time.Second)).Unix()/3600*3600, 0).UTC()) {
		t.Fatalf("LastOK = %v, want the latest hourly hour", a.LastOK)
	}
}

func TestLongWindowIncludesRawSamplesOnFreshDB(t *testing.T) {
	s := newTestStore(t, nil)
	at := base.Add(-time.Hour)
	for _, sm := range []Sample{
		probe(at, "p1/a", true, 100),
		probe(at.Add(time.Second), "p1/a", true, 300),
		dial(at.Add(2*time.Second), "p1/a", true, 400),
		throughput(at.Add(3*time.Second), "p1/a", 4096, 1000),
	} {
		s.Record(sm)
	}
	mustFlush(t, s)

	got, err := s.Nodes(Query{Since: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1: %+v", len(got), got)
	}
	n := got[0]
	if n.Probes != 2 || n.ProbesOK != 2 || n.DelayAvgMS != 200 {
		t.Fatalf("probes = %d/%d avg %d, want 2/2 avg 200", n.Probes, n.ProbesOK, n.DelayAvgMS)
	}
	if n.Dials != 1 || n.DialsOK != 1 || n.DialAvgMS != 400 {
		t.Fatalf("dials = %d/%d avg %d, want 1/1 avg 400", n.Dials, n.DialsOK, n.DialAvgMS)
	}
	if n.Bytes != 4096 || !n.LastSeen.Equal(at.Add(3*time.Second)) || !n.LastOK.Equal(at.Add(2*time.Second)) {
		t.Fatalf("recent aggregates = %+v, want bytes 4096 and recent sample times", n)
	}
	if n.DelayP50MS != 0 || n.DelayP95MS != 0 || n.ThroughputP50KBps != 0 || n.ThroughputMaxKBps != 0 {
		t.Fatalf("long-window percentiles/rates = %+v, want unavailable", n)
	}
}

func TestLongWindowCombinesHourlyAndRawSamples(t *testing.T) {
	s := newTestStore(t, nil)
	old := base.Add(-20 * 24 * time.Hour).Truncate(time.Hour)
	recent := base.Add(-time.Hour)
	for _, sm := range []Sample{
		probe(old, "p1/a", true, 100),
		dial(old.Add(time.Second), "p1/a", true, 100),
		throughput(old.Add(2*time.Second), "p1/a", 1024, 1000),
		probe(recent, "p1/a", true, 300),
		dial(recent.Add(time.Second), "p1/a", true, 400),
		throughput(recent.Add(2*time.Second), "p1/a", 2048, 1000),
	} {
		s.Record(sm)
	}
	mustFlush(t, s)
	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got := countRows(t, s.db, "sample"); got != 3 {
		t.Fatalf("raw sample rows = %d, want 3", got)
	}
	if got := countRows(t, s.db, "hourly"); got != 3 {
		t.Fatalf("hourly rows = %d, want 3", got)
	}

	got, err := s.Nodes(Query{Since: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1: %+v", len(got), got)
	}
	n := got[0]
	if n.Probes != 2 || n.ProbesOK != 2 || n.DelayAvgMS != 200 {
		t.Fatalf("probes = %d/%d avg %d, want combined 2/2 avg 200", n.Probes, n.ProbesOK, n.DelayAvgMS)
	}
	if n.Dials != 2 || n.DialsOK != 2 || n.DialAvgMS != 250 {
		t.Fatalf("dials = %d/%d avg %d, want combined 2/2 avg 250", n.Dials, n.DialsOK, n.DialAvgMS)
	}
	if n.Bytes != 3072 || !n.LastSeen.Equal(recent.Add(2*time.Second)) || !n.LastOK.Equal(recent.Add(time.Second)) {
		t.Fatalf("combined aggregates = %+v, want bytes 3072 and latest raw sample times", n)
	}
	if n.DelayP50MS != 0 || n.DelayP95MS != 0 || n.ThroughputP50KBps != 0 || n.ThroughputMaxKBps != 0 {
		t.Fatalf("long-window percentiles/rates = %+v, want unavailable", n)
	}
}

// A node that has only throughput rows in hourly (no probes, no dials) is
// reported with its bytes on a long window and does not break the query.
func TestLongWindowThroughputOnlyNode(t *testing.T) {
	s := newTestStore(t, nil)
	at := base.Add(-time.Hour).Truncate(time.Hour)
	s.Record(probe(at, "p1/a", true, 100))
	s.Record(throughput(at, "p1/only", 4096, 1000))
	mustFlush(t, s)
	if err := s.prune(base.Add(8 * 24 * time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	got, err := s.Nodes(Query{Since: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 2 || got[0].Label != "p1/a" || got[1].Label != "p1/only" {
		t.Fatalf("Nodes = %+v, want p1/a and p1/only", got)
	}
	only := got[1]
	if only.Probes != 0 || only.Dials != 0 || only.Bytes != 4096 {
		t.Fatalf("throughput-only node = %+v, want 0 probes, 0 dials, 4096 bytes", only)
	}
	if only.LastOK.IsZero() || only.LastSeen.IsZero() {
		t.Fatalf("throughput-only node lost its times: %+v", only)
	}

	// A label filter must not touch the other node's rows.
	filtered, err := s.Nodes(Query{Since: 30 * 24 * time.Hour, Label: "p1/a"})
	if err != nil {
		t.Fatalf("filtered Nodes: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Label != "p1/a" {
		t.Fatalf("filtered Nodes = %+v, want only p1/a", filtered)
	}
}

func TestPruneKeepsPendingBatches(t *testing.T) {
	s, err := openStore(Options{
		Path: filepath.Join(t.TempDir(), "stats.db"), QueueSize: 4, Now: clock(0),
	})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	started := false
	t.Cleanup(func() {
		if !started {
			s.start()
		}
		_ = s.Close()
	})
	s.Record(probe(base, "pending-node", true, 100))
	s.RecordAccount(acct(base, "pending-login", "pending-account-node", KindDial, true, 200))
	pruneDone := make(chan error, 1)
	go func() { pruneDone <- s.prune(base) }()
	waitState(t, "queued prune after pending samples", func() bool { return len(s.queue) == 3 })
	s.start()
	started = true
	if err := <-pruneDone; err != nil {
		t.Fatalf("prune: %v", err)
	}
	mustFlush(t, s)
	if got := countRows(t, s.db, "sample"); got != 1 {
		t.Fatalf("sample rows after pruning with a pending node batch = %d, want 1", got)
	}
	if got := countRows(t, s.db, "account_sample"); got != 1 {
		t.Fatalf("account_sample rows after pruning with a pending account batch = %d, want 1", got)
	}
	nodes, err := s.Nodes(Query{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Label != "pending-node" || nodes[0].Probes != 1 {
		t.Fatalf("Nodes after pruning pending batch = %+v, want pending-node with one probe", nodes)
	}
	accounts, err := s.Accounts(Query{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Login != "pending-login" || accounts[0].Dials != 1 {
		t.Fatalf("Accounts after pruning pending batch = %+v, want pending-login with one dial", accounts)
	}
}

func TestPruneEvictsBothIDCaches(t *testing.T) {
	s := newTestStore(t, nil)
	s.Record(probe(base, "cache-node", true, 100))
	s.RecordAccount(acct(base, "cache-login", "cache-account-node", KindDial, true, 200))
	mustFlush(t, s)
	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close joins the writer before reading either writer-owned map.
	if len(s.ids) != 0 {
		t.Fatalf("node ID cache after prune = %v, want empty", s.ids)
	}
	if len(s.accIDs) != 0 {
		t.Fatalf("account ID cache after prune = %v, want empty", s.accIDs)
	}
}

func TestPruneDeletesUnusedIdentitiesAndRefreshesCaches(t *testing.T) {
	s := newTestStore(t, nil)
	old := base.Add(-100 * 24 * time.Hour)
	s.Record(probe(old, "stale-node", true, 100))
	s.RecordAccount(acct(old, "stale-login", "stale-account-node", KindDial, true, 200))
	if err := s.SetNodes([]NodeInfo{{Label: "metadata-only-node"}}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO account (login, first_seen, last_seen) VALUES (?, ?, ?)",
		"metadata-only-login", base.UnixMilli(), base.UnixMilli()); err != nil {
		t.Fatalf("insert metadata-only account: %v", err)
	}
	mustFlush(t, s)

	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, table := range []string{"sample", "hourly", "account_sample", "account_hourly", "node", "account"} {
		if got := countRows(t, s.db, table); got != 0 {
			t.Fatalf("%s rows after pruning expired measurements and identities = %d, want 0", table, got)
		}
	}

	// Reuse the deleted IDs with other identities before the old labels and
	// login return. A stale cache would attribute their measurements to these
	// replacements instead of recreating the old identities.
	s.Record(probe(base, "replacement-node", true, 300))
	s.RecordAccount(acct(base, "replacement-login", "replacement-account-node", KindDial, true, 400))
	mustFlush(t, s)
	s.Record(probe(base, "stale-node", true, 500))
	s.RecordAccount(acct(base, "stale-login", "stale-account-node", KindDial, true, 600))
	mustFlush(t, s)

	nodes, err := s.Nodes(Query{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Label != "replacement-node" || nodes[0].Probes != 1 ||
		nodes[1].Label != "stale-node" || nodes[1].Probes != 1 {
		t.Fatalf("Nodes after identity recreation = %+v, want one probe for each label", nodes)
	}
	accounts, err := s.Accounts(Query{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].Login != "replacement-login" || accounts[0].Dials != 1 ||
		accounts[1].Login != "stale-login" || accounts[1].Dials != 1 {
		t.Fatalf("Accounts after identity recreation = %+v, want one dial for each login", accounts)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close joins the writer before these cache reads.
	if s.ids["replacement-node"] == s.ids["stale-node"] ||
		s.ids["replacement-account-node"] == s.ids["stale-account-node"] {
		t.Fatalf("node cache IDs were reused across identities: %v", s.ids)
	}
	if s.accIDs["replacement-login"] == s.accIDs["stale-login"] {
		t.Fatalf("account cache IDs were reused across logins: %v", s.accIDs)
	}
}

func TestPruneKeepsEveryMeasurementAndBindingReference(t *testing.T) {
	s := newTestStore(t, nil)
	old := base.Add(-100 * 24 * time.Hour).UnixMilli()
	for i, label := range []string{"sample-node", "hourly-node", "account-sample-node", "account-hourly-node", "binding-node", "metadata-only-node"} {
		if _, err := s.db.Exec(`
INSERT INTO node (id, label, provider, country, name, first_seen, last_seen)
VALUES (?, ?, '', '', '', ?, ?)`, i+1, label, old, old); err != nil {
			t.Fatalf("insert node %s: %v", label, err)
		}
	}
	for i, login := range []string{"account-sample-login", "account-hourly-login", "binding-login", "metadata-only-login"} {
		if _, err := s.db.Exec(
			"INSERT INTO account (id, login, first_seen, last_seen) VALUES (?, ?, ?, ?)",
			11+i, login, old, old); err != nil {
			t.Fatalf("insert account %s: %v", login, err)
		}
	}
	if _, err := s.db.Exec(
		"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, 1, 1, 1, 100, 0)",
		base.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("insert raw sample: %v", err)
	}
	hour := base.Unix() / 3600
	if _, err := s.db.Exec(
		"INSERT INTO hourly (hour, node, kind, n, ok, ms_sum, ms_max, bytes) VALUES (?, 2, 1, 1, 1, 100, 100, 0)", hour); err != nil {
		t.Fatalf("insert hourly row: %v", err)
	}
	if _, err := s.db.Exec(`
INSERT INTO account_sample (ts, account, node, kind, ok, ms, bytes, up, down)
VALUES (?, 11, 3, 2, 1, 100, 0, 0, 0)`, base.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("insert account sample: %v", err)
	}
	if _, err := s.db.Exec(`
INSERT INTO account_hourly (hour, account, node, kind, n, ok, ms_sum, ms_max, bytes, up, down)
VALUES (?, 12, 4, 2, 1, 1, 100, 100, 0, 0, 0)`, hour); err != nil {
		t.Fatalf("insert account hourly row: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES (?, ?, ?, ?, ?)",
		"binding-login", "example.com", "binding-node", old, old); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, tc := range []struct {
		table, column, value string
	}{
		{"node", "label", "sample-node"},
		{"node", "label", "hourly-node"},
		{"node", "label", "account-sample-node"},
		{"node", "label", "account-hourly-node"},
		{"node", "label", "binding-node"},
		{"account", "login", "account-sample-login"},
		{"account", "login", "account-hourly-login"},
		{"account", "login", "binding-login"},
	} {
		var got int
		query := "SELECT COUNT(*) FROM " + tc.table + " WHERE " + tc.column + " = ?"
		if err := s.db.QueryRow(query, tc.value).Scan(&got); err != nil {
			t.Fatalf("count %s %s: %v", tc.table, tc.value, err)
		}
		if got != 1 {
			t.Fatalf("%s %s rows = %d, want 1 (referenced identity retained)", tc.table, tc.value, got)
		}
	}
	for _, tc := range []struct{ table, name string }{
		{"node", "metadata-only-node"},
		{"account", "metadata-only-login"},
	} {
		column := "label"
		if tc.table == "account" {
			column = "login"
		}
		var got int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM "+tc.table+" WHERE "+column+" = ?", tc.name).Scan(&got); err != nil {
			t.Fatalf("count unreferenced %s %s: %v", tc.table, tc.name, err)
		}
		if got != 0 {
			t.Fatalf("unreferenced %s %s rows = %d, want 0", tc.table, tc.name, got)
		}
	}
	if got := countRows(t, s.db, "sample"); got != 1 {
		t.Fatalf("sample rows = %d, want 1", got)
	}
	if got := countRows(t, s.db, "hourly"); got != 1 {
		t.Fatalf("hourly rows = %d, want 1", got)
	}
	if got := countRows(t, s.db, "account_sample"); got != 1 {
		t.Fatalf("account_sample rows = %d, want 1", got)
	}
	if got := countRows(t, s.db, "account_hourly"); got != 1 {
		t.Fatalf("account_hourly rows = %d, want 1", got)
	}
	if got := countRows(t, s.db, "binding"); got != 1 {
		t.Fatalf("binding rows = %d, want 1 (prune must not expire bindings)", got)
	}
}

func TestOpenPrunesExpiredIdentitiesSynchronously(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	raw := openRaw(t, path)
	for _, ddl := range []string{schemaV1, schemaV2, schemaV3, schemaV4, "PRAGMA user_version = 4"} {
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	old := base.Add(-100 * 24 * time.Hour).UnixMilli()
	if _, err := raw.Exec(`
INSERT INTO node (id, label, provider, country, name, first_seen, last_seen)
VALUES (1, 'expired-node', '', '', '', ?, ?), (2, 'metadata-only-node', '', '', '', ?, ?)`,
		old, old, base.UnixMilli(), base.UnixMilli()); err != nil {
		t.Fatalf("insert nodes: %v", err)
	}
	if _, err := raw.Exec(
		"INSERT INTO account (id, login, first_seen, last_seen) VALUES (1, 'expired-login', ?, ?), (2, 'metadata-only-login', ?, ?)",
		old, old, base.UnixMilli(), base.UnixMilli()); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	if _, err := raw.Exec(
		"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, 1, 1, 1, 100, 0)", old); err != nil {
		t.Fatalf("insert old sample: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO account_sample (ts, account, node, kind, ok, ms, bytes, up, down)
VALUES (?, 1, 1, 2, 1, 100, 0, 0, 0)`, old); err != nil {
		t.Fatalf("insert old account sample: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	s, err := openStore(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// openStore has not started the writer yet, so the rows below prove the
	// retention pass completed synchronously before any goroutine can prune.
	for _, table := range []string{"sample", "hourly", "account_sample", "account_hourly", "node", "account"} {
		if got := countRows(t, s.db, table); got != 0 {
			t.Fatalf("%s rows immediately after openStore = %d, want 0", table, got)
		}
	}
}
