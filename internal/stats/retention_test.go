package stats

import (
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
