package stats

import (
	"path/filepath"
	"testing"
	"time"
)

// acct builds an account sample.
func acct(at time.Time, login, label string, kind Kind, ok bool, ms int) AccountSample {
	return AccountSample{At: at, Login: login, Label: label, Kind: kind, OK: ok,
		Delay: time.Duration(ms) * time.Millisecond}
}

// dbPath returns a fresh database file path.
func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "stats.db")
}

// makeV1DB creates a schema version 1 database with one node row, two raw
// samples and user_version = 1.
func makeV1DB(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	for _, stmt := range []string{schemaV1, "PRAGMA user_version = 1"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO node (id, label, provider, country, first_seen, last_seen) VALUES (1, 'p1/a', 'prov1', 'DE', 100, 100)"); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(
			"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, 1, 1, 1, 100, 0)", t0.UnixMilli()+int64(i)); err != nil {
			t.Fatalf("insert sample: %v", err)
		}
	}
}

// makeV2DB creates a schema version 2 database: the v1 layout plus the
// account tables, with one node row, two raw samples, one account sample and
// user_version = 2.
func makeV2DB(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	for _, stmt := range []string{schemaV1, schemaV2, "PRAGMA user_version = 2"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO node (id, label, provider, country, first_seen, last_seen) VALUES (1, 'p1/a', 'prov1', 'DE', 100, 100)"); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(
			"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, 1, 1, 1, 100, 0)", t0.UnixMilli()+int64(i)); err != nil {
			t.Fatalf("insert sample: %v", err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO account (id, login, first_seen, last_seen) VALUES (1, 'alice', 100, 100)"); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO account_sample (ts, account, node, kind, ok, ms, bytes, up, down) VALUES (?, 1, 1, 2, 1, 100, 0, 0, 0)",
		t0.UnixMilli()); err != nil {
		t.Fatalf("insert account_sample: %v", err)
	}
}

func TestMigrateV2ToV3KeepsData(t *testing.T) {
	path := dbPath(t)
	makeV2DB(t, path)

	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if s.version != 4 {
		t.Fatalf("version = %d, want 4", s.version)
	}
	raw := openRaw(t, path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 4 {
		t.Fatalf("user_version = %d, want 4", version)
	}
	if n := countRows(t, raw, "node"); n != 1 {
		t.Fatalf("node rows = %d, want 1 (v2 data kept)", n)
	}
	if n := countRows(t, raw, "sample"); n != 2 {
		t.Fatalf("sample rows = %d, want 2 (v2 data kept)", n)
	}
	if n := countRows(t, raw, "account_sample"); n != 1 {
		t.Fatalf("account_sample rows = %d, want 1 (v2 data kept)", n)
	}
	// The migrated node row answers with an empty name.
	nodes, err := s.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Label != "p1/a" || nodes[0].Name != "" {
		t.Fatalf("Nodes on migrated db = %+v", nodes)
	}
}

func TestReadOnlyV2NoNameColumn(t *testing.T) {
	path := dbPath(t)
	makeV2DB(t, path)
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	if ro.version != 2 {
		t.Fatalf("version = %d, want 2", ro.version)
	}
	nodes, err := ro.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "" {
		t.Fatalf("Nodes on v2 = %+v", nodes)
	}
	an, err := ro.AccountNodes(Query{})
	if err != nil {
		t.Fatalf("AccountNodes: %v", err)
	}
	if len(an) != 1 || an[0].Label != "p1/a" || an[0].Name != "" || an[0].Dials != 1 {
		t.Fatalf("AccountNodes on v2 = %+v", an)
	}
}

func TestMigrateV1KeepsData(t *testing.T) {
	path := dbPath(t)
	makeV1DB(t, path)

	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if s.version != 4 {
		t.Fatalf("version = %d, want 4", s.version)
	}
	raw := openRaw(t, path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 4 {
		t.Fatalf("user_version = %d, want 4", version)
	}
	if n := countRows(t, raw, "node"); n != 1 {
		t.Fatalf("node rows = %d, want 1 (v1 data kept)", n)
	}
	if n := countRows(t, raw, "sample"); n != 3 {
		t.Fatalf("sample rows = %d, want 3 (v1 data kept)", n)
	}
	if n := countRows(t, raw, "account"); n != 0 {
		t.Fatalf("account rows = %d, want 0", n)
	}
	if n := countRows(t, raw, "account_sample"); n != 0 {
		t.Fatalf("account_sample rows = %d, want 0", n)
	}
	// The old samples still aggregate.
	got, err := s.Nodes(Query{Label: "p1/a"})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Probes != 3 {
		t.Fatalf("Nodes on migrated db = %+v", got)
	}
}

func TestReadOnlyV1AccountsEmpty(t *testing.T) {
	path := dbPath(t)
	makeV1DB(t, path)
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	if ro.version != 1 {
		t.Fatalf("version = %d, want 1", ro.version)
	}
	got, err := ro.Accounts(Query{})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Accounts on v1 = %+v, want empty non-nil", got)
	}
	nodes, err := ro.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Probes != 3 {
		t.Fatalf("Nodes on v1 = %+v", nodes)
	}
}

func TestAccountsRawAggregates(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
		{Label: "p2/b", Provider: "prov2", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	// alice: dials 2 ok (100, 300 ms) + 1 failed, throughput 100 up/200 down
	// on p1/a; her failed dial sits on p2/b, so she spans two nodes.
	for _, sm := range []AccountSample{
		acct(t0, "alice", "p1/a", KindDial, true, 100),
		acct(t0.Add(time.Second), "alice", "p1/a", KindDial, true, 300),
		acct(t0.Add(2*time.Second), "alice", "p2/b", KindDial, false, 10),
		acctSampleBytes(t0.Add(3*time.Second), "alice", "p1/a", 100, 200),
		acctSampleBytes(t0.Add(4*time.Second), "bob", "p2/b", 500, 0),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)

	got, err := s.Accounts(Query{})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 2 || got[0].Login != "alice" || got[1].Login != "bob" {
		t.Fatalf("Accounts = %+v, want alice then bob", got)
	}
	a := got[0]
	if a.Dials != 3 || a.DialsOK != 2 || a.DialAvgMS != 200 {
		t.Fatalf("alice dials = %d/%d avg %d, want 3/2 avg 200", a.Dials, a.DialsOK, a.DialAvgMS)
	}
	if a.BytesUp != 100 || a.BytesDown != 200 {
		t.Fatalf("alice bytes = %d/%d, want 100/200", a.BytesUp, a.BytesDown)
	}
	if a.Nodes != 2 || len(a.Countries) != 2 || a.Countries[0] != "DE" || a.Countries[1] != "US" ||
		len(a.Providers) != 2 || a.Providers[0] != "prov1" || a.Providers[1] != "prov2" {
		t.Fatalf("alice nodes = %+v", a)
	}
	if !a.LastSeen.Equal(t0.Add(3 * time.Second)) {
		t.Fatalf("alice LastSeen = %v, want %v", a.LastSeen, t0.Add(3*time.Second))
	}
	b := got[1]
	if b.Dials != 0 || b.BytesUp != 500 || b.BytesDown != 0 || b.Nodes != 1 ||
		b.Countries[0] != "US" || b.Providers[0] != "prov2" {
		t.Fatalf("bob = %+v", b)
	}

	// The Account filter restricts the result to one login.
	filtered, err := s.Accounts(Query{Account: "bob"})
	if err != nil {
		t.Fatalf("filtered Accounts: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Login != "bob" || filtered[0].BytesUp != 500 {
		t.Fatalf("filtered Accounts = %+v, want only bob", filtered)
	}
}

// acctSampleBytes is a throughput account sample.
func acctSampleBytes(at time.Time, login, label string, up, down int64) AccountSample {
	return AccountSample{At: at, Login: login, Label: label, Kind: KindThroughput,
		OK: true, Bytes: up + down, Up: up, Down: down}
}

func TestAccountsHourlyPath(t *testing.T) {
	// Retention of an hour: the samples (2 h old) are folded into
	// account_hourly at open and a 24 h window reads the hourly table.
	s := newTestStore(t, func(o *Options) { o.Retention = time.Hour })
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
		{Label: "p2/b", Provider: "prov2", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	at := base.Add(-2 * time.Hour).Truncate(time.Hour)
	for _, sm := range []AccountSample{
		acct(at, "carol", "p1/a", KindDial, true, 100),
		acct(at.Add(time.Second), "carol", "p1/a", KindDial, false, 500),
		acctSampleBytes(at.Add(2*time.Second), "carol", "p1/a", 1000, 2000),
		acctSampleBytes(at.Add(3*time.Second), "carol", "p2/b", 3000, 0),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)
	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}

	got, err := s.Accounts(Query{})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 1 || got[0].Login != "carol" {
		t.Fatalf("Accounts = %+v, want carol", got)
	}
	a := got[0]
	if a.Dials != 2 || a.DialsOK != 1 || a.DialAvgMS != 100 {
		t.Fatalf("hourly dials = %d/%d avg %d, want 2/1 avg 100", a.Dials, a.DialsOK, a.DialAvgMS)
	}
	if a.BytesUp != 4000 || a.BytesDown != 2000 {
		t.Fatalf("hourly bytes = %d/%d, want 4000/2000", a.BytesUp, a.BytesDown)
	}
	if a.Nodes != 2 {
		t.Fatalf("hourly nodes = %d, want 2", a.Nodes)
	}
	if a.LastSeen.IsZero() {
		t.Fatalf("hourly LastSeen zero: %+v", a)
	}
}

func TestPruneFoldsAccountSamples(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.Retention = 48 * time.Hour })
	old := base.Add(-72 * time.Hour).Truncate(time.Hour)
	oldHour := old.Unix() / 3600
	// The fold groups by kind, so the dials and the throughput sample land
	// in separate account_hourly rows.
	for _, sm := range []AccountSample{
		acct(old, "dave", "p1/a", KindDial, true, 100),
		acct(old.Add(time.Second), "dave", "p1/a", KindDial, true, 300),
		acct(old.Add(2*time.Second), "dave", "p1/a", KindDial, false, 10),
		acctSampleBytes(old.Add(3*time.Second), "dave", "p1/a", 10, 20),
		acct(t0, "dave", "p1/a", KindDial, true, 200), // inside retention
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)
	// An account_hourly row already past the 90 day horizon.
	if _, err := s.db.Exec(
		"INSERT INTO account_hourly (hour, account, node, kind, n, ok, ms_sum, ms_max, bytes) VALUES (?, 1, 1, 1, 1, 1, 0, 0, 0)",
		oldHour-90*24); err != nil {
		t.Fatalf("insert stale hourly: %v", err)
	}

	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}
	raw := openRaw(t, s.opts.Path)
	if n := countRows(t, raw, "account_sample"); n != 1 {
		t.Fatalf("account_sample rows after prune = %d, want 1", n)
	}
	var n, ok, msSum, msMax, bytes, up, down int64
	if err := raw.QueryRow(
		"SELECT n, ok, ms_sum, ms_max, bytes, up, down FROM account_hourly WHERE hour = ? AND kind = 2", oldHour,
	).Scan(&n, &ok, &msSum, &msMax, &bytes, &up, &down); err != nil {
		t.Fatalf("folded account_hourly dial row: %v", err)
	}
	if n != 3 || ok != 2 || msSum != 400 || msMax != 300 || bytes != 0 || up != 0 || down != 0 {
		t.Fatalf("folded dial row = %d/%d/%d/%d/%d/%d/%d, want 3/2/400/300/0/0/0",
			n, ok, msSum, msMax, bytes, up, down)
	}
	if err := raw.QueryRow(
		"SELECT n, ok, ms_sum, ms_max, bytes, up, down FROM account_hourly WHERE hour = ? AND kind = 3", oldHour,
	).Scan(&n, &ok, &msSum, &msMax, &bytes, &up, &down); err != nil {
		t.Fatalf("folded account_hourly throughput row: %v", err)
	}
	if n != 1 || ok != 1 || msSum != 0 || msMax != 0 || bytes != 30 || up != 10 || down != 20 {
		t.Fatalf("folded throughput row = %d/%d/%d/%d/%d/%d/%d, want 1/1/0/0/30/10/20",
			n, ok, msSum, msMax, bytes, up, down)
	}
	var stale int64
	if err := raw.QueryRow("SELECT COUNT(*) FROM account_hourly WHERE hour < ?",
		base.Unix()/3600-90*24).Scan(&stale); err != nil {
		t.Fatalf("count stale account_hourly: %v", err)
	}
	if stale != 0 {
		t.Fatalf("stale account_hourly rows = %d, want 0", stale)
	}
}

// trimPendingAcct must bound the retry buffer to the newest
// maxPendingSamples samples and count the dropped ones.
func TestTrimPendingAcct(t *testing.T) {
	s := newTestStore(t, nil)
	var batch []AccountSample
	for i := 0; i < maxPendingSamples+10; i++ {
		batch = append(batch, acct(base.Add(time.Duration(i)*time.Millisecond),
			"erin", "p1/a", KindDial, true, i))
	}
	got := s.trimPendingAcct(batch)
	if len(got) != maxPendingSamples {
		t.Fatalf("len = %d, want %d", len(got), maxPendingSamples)
	}
	if last := got[len(got)-1]; last != batch[len(batch)-1] {
		t.Fatalf("kept the wrong end: last = %+v, want %+v", last, batch[len(batch)-1])
	}
	if want := uint64(len(batch) - maxPendingSamples); s.Dropped() != want {
		t.Fatalf("Dropped = %d, want %d", s.Dropped(), want)
	}
}

// A long window must sum the raw samples newer than the retention boundary
// with the hourly rows covering the folded past, without double counting.
func TestAccountsMixedWindow(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	// Old set: written first and folded into account_hourly by pruning with
	// a clock past the retention.
	old := base.Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	for _, sm := range []AccountSample{
		acct(old, "fen", "p1/a", KindDial, true, 50),
		acctSampleBytes(old.Add(time.Second), "fen", "p1/a", 30, 70),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)
	if err := s.prune(base.Add(8 * 24 * time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	// Recent set: written after the prune so it stays in account_sample.
	for _, sm := range []AccountSample{
		acct(base.Add(-time.Hour), "fen", "p1/a", KindDial, true, 100),
		acct(base.Add(-time.Hour).Add(time.Second), "fen", "p1/a", KindDial, true, 300),
		acctSampleBytes(base.Add(-time.Hour).Add(2*time.Second), "fen", "p1/a", 100, 200),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)

	got, err := s.Accounts(Query{Since: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 1 || got[0].Login != "fen" {
		t.Fatalf("Accounts = %+v, want fen", got)
	}
	a := got[0]
	if a.Dials != 3 || a.DialsOK != 3 || a.DialAvgMS != 150 {
		t.Fatalf("mixed dials = %d/%d avg %d, want 3/3 avg 150", a.Dials, a.DialsOK, a.DialAvgMS)
	}
	if a.BytesUp != 130 || a.BytesDown != 270 {
		t.Fatalf("mixed bytes = %d/%d, want 130/270 (exactly set1 + set2)", a.BytesUp, a.BytesDown)
	}
	if a.Nodes != 1 {
		t.Fatalf("mixed nodes = %d, want 1", a.Nodes)
	}
}

func TestAccountNodes(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
		{Label: "p2/b", Provider: "prov2", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	// p1/a: 2 ok dials (100, 300 ms) and 100 up/200 down; p2/b: 1 failed
	// dial and 500 up only.
	for _, sm := range []AccountSample{
		acct(t0, "alice", "p1/a", KindDial, true, 100),
		acct(t0.Add(time.Second), "alice", "p1/a", KindDial, true, 300),
		acct(t0.Add(2*time.Second), "bob", "p2/b", KindDial, false, 10),
		acctSampleBytes(t0.Add(3*time.Second), "alice", "p1/a", 100, 200),
		acctSampleBytes(t0.Add(4*time.Second), "bob", "p2/b", 500, 0),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)

	got, err := s.AccountNodes(Query{})
	if err != nil {
		t.Fatalf("AccountNodes: %v", err)
	}
	if len(got) != 2 || got[0].Label != "p1/a" || got[1].Label != "p2/b" {
		t.Fatalf("AccountNodes = %+v, want p1/a then p2/b", got)
	}
	a := got[0]
	if a.Provider != "prov1" || a.Country != "DE" {
		t.Fatalf("p1/a meta = %s/%s, want prov1/DE", a.Provider, a.Country)
	}
	if a.Dials != 2 || a.DialsOK != 2 || a.DialAvgMS != 200 {
		t.Fatalf("p1/a dials = %d/%d avg %d, want 2/2 avg 200", a.Dials, a.DialsOK, a.DialAvgMS)
	}
	if a.Bytes != 300 {
		t.Fatalf("p1/a bytes = %d, want 300 (up+down)", a.Bytes)
	}
	if a.Probes != 0 || a.ProbesOK != 0 || a.DelayAvgMS != 0 ||
		a.ThroughputP50KBps != 0 || a.ThroughputMaxKBps != 0 {
		t.Fatalf("p1/a probe/percentile fields not zero: %+v", a)
	}
	if !a.LastSeen.Equal(t0.Add(3 * time.Second)) {
		t.Fatalf("p1/a LastSeen = %v, want %v", a.LastSeen, t0.Add(3*time.Second))
	}
	b := got[1]
	if b.Dials != 1 || b.DialsOK != 0 || b.Bytes != 500 {
		t.Fatalf("p2/b = %+v, want 1/0 dials, 500 bytes", b)
	}

	// The Account filter restricts the rows to one login's nodes.
	filtered, err := s.AccountNodes(Query{Account: "bob"})
	if err != nil {
		t.Fatalf("filtered AccountNodes: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Label != "p2/b" {
		t.Fatalf("bob nodes = %+v, want only p2/b", filtered)
	}

	// The Country filter restricts the rows to matching nodes.
	de, err := s.AccountNodes(Query{Country: "DE"})
	if err != nil {
		t.Fatalf("country AccountNodes: %v", err)
	}
	if len(de) != 1 || de[0].Label != "p1/a" || de[0].Country != "DE" {
		t.Fatalf("DE nodes = %+v, want only p1/a", de)
	}
}

func TestAccountNodesHourlyPath(t *testing.T) {
	// Retention of an hour: the samples (2 h old) are folded into
	// account_hourly at open and a 24 h window reads the hourly table.
	s := newTestStore(t, func(o *Options) { o.Retention = time.Hour })
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	at := base.Add(-2 * time.Hour).Truncate(time.Hour)
	for _, sm := range []AccountSample{
		acct(at, "carol", "p1/a", KindDial, true, 100),
		acct(at.Add(time.Second), "carol", "p1/a", KindDial, false, 500),
		acctSampleBytes(at.Add(2*time.Second), "carol", "p1/a", 1000, 2000),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)
	if err := s.prune(base); err != nil {
		t.Fatalf("prune: %v", err)
	}

	got, err := s.AccountNodes(Query{})
	if err != nil {
		t.Fatalf("AccountNodes: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p1/a" {
		t.Fatalf("AccountNodes = %+v, want p1/a", got)
	}
	a := got[0]
	if a.Dials != 2 || a.DialsOK != 1 || a.DialAvgMS != 100 || a.Bytes != 3000 {
		t.Fatalf("hourly node = %+v, want 2/1 dials avg 100, 3000 bytes", a)
	}
	if a.LastSeen.IsZero() {
		t.Fatalf("hourly LastSeen zero: %+v", a)
	}
}

// A writable store runs on a single connection: AccountNodes and Accounts
// begin a read transaction and must answer every nested query through it or
// from the cached name-column flag, never through the busy connection.
func TestAccountQueriesOnWritableStoreNoDeadlock(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE", Name: "Fin land 1"},
		{Label: "p2/b", Provider: "prov2", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	for _, sm := range []AccountSample{
		acct(t0, "alice", "p1/a", KindDial, true, 100),
		acct(t0.Add(time.Second), "bob", "p2/b", KindDial, true, 200),
	} {
		s.RecordAccount(sm)
	}
	mustFlush(t, s)

	// runWithTimeout fails loudly instead of hanging the test run.
	runWithTimeout := func(name string, fn func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fn() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: deadlock: no result within 5s on a writable store", name)
		}
	}
	runWithTimeout("AccountNodes", func() error { _, err := s.AccountNodes(Query{}); return err })
	runWithTimeout("Accounts", func() error { _, err := s.Accounts(Query{}); return err })

	got, err := s.AccountNodes(Query{})
	if err != nil {
		t.Fatalf("AccountNodes: %v", err)
	}
	if len(got) != 2 || got[0].Label != "p1/a" || got[1].Label != "p2/b" {
		t.Fatalf("AccountNodes = %+v, want p1/a then p2/b", got)
	}
	if got[0].Name != "Fin land 1" || got[1].Name != "" {
		t.Fatalf("AccountNodes names = %q/%q, want \"Fin land 1\"/\"\"", got[0].Name, got[1].Name)
	}
	accounts, err := s.Accounts(Query{})
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].Login != "alice" || accounts[1].Login != "bob" {
		t.Fatalf("Accounts = %+v, want alice then bob", accounts)
	}
}
