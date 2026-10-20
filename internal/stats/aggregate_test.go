package stats

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// t0 is one hour before the query clock; every sample stays inside the
// default 24 h window (base is anchored near time.Now for read-only stores).
var t0 = base.Add(-time.Hour)

func probe(at time.Time, label string, ok bool, ms int) Sample {
	return Sample{At: at, Label: label, Kind: KindProbe, OK: ok, Delay: time.Duration(ms) * time.Millisecond}
}

func dial(at time.Time, label string, ok bool, ms int) Sample {
	return Sample{At: at, Label: label, Kind: KindDial, OK: ok, Delay: time.Duration(ms) * time.Millisecond}
}

func throughput(at time.Time, label string, bytes int64, ms int) Sample {
	return Sample{At: at, Label: label, Kind: KindThroughput, OK: true,
		Delay: time.Duration(ms) * time.Millisecond, Bytes: bytes}
}

func TestNodesExactAggregates(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
		{Label: "p2/b", Provider: "prov2", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	// Node a: 3 OK probes (100, 200, 300 ms), 1 failed probe, 1 OK dial
	// (400 ms), 1 failed dial, two throughput windows of 1 s.
	for _, sm := range []Sample{
		probe(t0, "p1/a", true, 100),
		probe(t0.Add(time.Second), "p1/a", true, 200),
		probe(t0.Add(2*time.Second), "p1/a", true, 300),
		probe(t0.Add(3*time.Second), "p1/a", false, 50),
		dial(t0.Add(4*time.Second), "p1/a", true, 400),
		dial(t0.Add(5*time.Second), "p1/a", false, 10),
		throughput(t0.Add(6*time.Second), "p1/a", 20480, 1000), // 20 KiB/s
		throughput(t0.Add(7*time.Second), "p1/a", 40960, 1000), // 40 KiB/s
		probe(t0, "p2/b", false, 20),
	} {
		s.Record(sm)
	}
	mustFlush(t, s)

	got, err := s.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2: %+v", len(got), got)
	}
	if got[0].Label != "p1/a" || got[1].Label != "p2/b" {
		t.Fatalf("Nodes not sorted by label: %+v", got)
	}
	a := got[0]
	if a.Provider != "prov1" || a.Country != "DE" {
		t.Fatalf("node metadata = %+v", a)
	}
	if a.Probes != 4 || a.ProbesOK != 3 {
		t.Fatalf("probes = %d/%d, want 4/3", a.Probes, a.ProbesOK)
	}
	if a.DelayAvgMS != 200 || a.DelayP50MS != 200 || a.DelayP95MS != 300 {
		t.Fatalf("delays = %d/%d/%d, want 200/200/300", a.DelayAvgMS, a.DelayP50MS, a.DelayP95MS)
	}
	if a.Dials != 2 || a.DialsOK != 1 || a.DialAvgMS != 400 {
		t.Fatalf("dials = %d/%d avg %d, want 2/1 avg 400", a.Dials, a.DialsOK, a.DialAvgMS)
	}
	// Nearest rank on [20, 40]: p50 = ceil(0.5*2) -> index 0 = 20, max = 40.
	if a.ThroughputP50KBps != 20 || a.ThroughputMaxKBps != 40 {
		t.Fatalf("throughput = %d/%d, want 20/40", a.ThroughputP50KBps, a.ThroughputMaxKBps)
	}
	if a.Bytes != 20480+40960 {
		t.Fatalf("bytes = %d, want %d", a.Bytes, 20480+40960)
	}
	if !a.LastSeen.Equal(t0.Add(7 * time.Second)) {
		t.Fatalf("LastSeen = %v, want %v", a.LastSeen, t0.Add(7*time.Second))
	}
	// LastOK counts only probes and dials: the OK dial at t0+4s.
	if !a.LastOK.Equal(t0.Add(4 * time.Second)) {
		t.Fatalf("LastOK = %v, want %v (latest OK probe or dial)", a.LastOK, t0.Add(4*time.Second))
	}
	b := got[1]
	if b.Probes != 1 || b.ProbesOK != 0 || b.DelayAvgMS != 0 || b.DelayP50MS != 0 {
		t.Fatalf("node b = %+v", b)
	}
	if b.LastSeen.IsZero() {
		t.Fatalf("node b LastSeen zero: %+v", b)
	}
	if !b.LastOK.IsZero() {
		t.Fatalf("node b LastOK = %v, want zero (no OK probe or dial)", b.LastOK)
	}
}

func TestNodesFiltersThreeNodes(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{
		{Label: "p1/a", Provider: "prov1", Country: "DE"},
		{Label: "p1/b", Provider: "prov2", Country: "DE"},
		{Label: "p2/c", Provider: "prov1", Country: "US"},
	}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	s.Record(probe(t0, "p1/a", true, 100))
	s.Record(probe(t0, "p1/b", true, 200))
	s.Record(probe(t0, "p2/c", true, 300))
	mustFlush(t, s)

	for _, tc := range []struct {
		q    Query
		want []string
	}{
		{Query{}, []string{"p1/a", "p1/b", "p2/c"}},
		{Query{Provider: "prov1"}, []string{"p1/a", "p2/c"}},
		{Query{Provider: "prov2"}, []string{"p1/b"}},
		{Query{Country: "DE"}, []string{"p1/a", "p1/b"}},
		{Query{Country: "US"}, []string{"p2/c"}},
		{Query{Label: "p1/b"}, []string{"p1/b"}},
		{Query{Provider: "prov1", Country: "US"}, []string{"p2/c"}},
		{Query{Label: "p1/b", Country: "US"}, nil},
	} {
		got, err := s.Nodes(tc.q)
		if err != nil {
			t.Fatalf("Nodes(%+v): %v", tc.q, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("Nodes(%+v) = %+v, want %v", tc.q, got, tc.want)
		}
		for i, want := range tc.want {
			if got[i].Label != want {
				t.Fatalf("Nodes(%+v)[%d] = %s, want %s", tc.q, i, got[i].Label, want)
			}
		}
	}
}

func TestFlushHonorsCanceledContext(t *testing.T) {
	s := newTestStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Flush with canceled ctx = %v, want context.Canceled", err)
	}
}

func TestQueueFullDropsAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	// The writer is not running yet, so the bounded queue fills exactly.
	s, err := openStore(Options{Path: path, QueueSize: 2, Now: clock(0)})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 0; i < 5; i++ {
		s.Record(probe(t0, "p1/a", true, 100))
	}
	if got := s.Dropped(); got != 3 {
		t.Fatalf("Dropped = %d, want 3", got)
	}
	s.start()
	mustFlush(t, s)
	raw := openRaw(t, path)
	if got := countRows(t, raw, "sample"); got != 2 {
		t.Fatalf("sample rows = %d, want 2", got)
	}
}

func TestConcurrentRecordExactTotal(t *testing.T) {
	s := newTestStore(t, nil)
	const goroutines, per = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				s.Record(probe(t0, "p1/a", true, 100))
			}
		}(g)
	}
	wg.Wait()
	mustFlush(t, s)

	raw := openRaw(t, s.opts.Path)
	if got := countRows(t, raw, "sample"); got != goroutines*per {
		t.Fatalf("sample rows = %d, want %d", got, goroutines*per)
	}
	if got := s.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d, want 0", got)
	}
	got, err := s.Nodes(Query{Label: "p1/a"})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Probes != goroutines*per || got[0].ProbesOK != goroutines*per {
		t.Fatalf("aggregates = %+v, want %d probes", got, goroutines*per)
	}
}

func TestSetNodesUpsert(t *testing.T) {
	s := newTestStore(t, nil)
	if err := s.SetNodes([]NodeInfo{{Label: "p1/a", Provider: "prov1", Country: "DE"}}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	// A second call updates the metadata without duplicating the row.
	if err := s.SetNodes([]NodeInfo{{Label: "p1/a", Provider: "prov2", Country: "US"}}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	raw := openRaw(t, s.opts.Path)
	if n := countRows(t, raw, "node"); n != 1 {
		t.Fatalf("node rows = %d, want 1", n)
	}
	got, err := s.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	// The node was created by SetNodes alone and has no samples yet.
	if len(got) != 0 {
		t.Fatalf("Nodes = %+v, want empty (no samples)", got)
	}
	s.Record(probe(t0, "p1/a", true, 100))
	mustFlush(t, s)
	got, err = s.Nodes(Query{Provider: "prov2"})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p1/a" || got[0].Provider != "prov2" || got[0].Country != "US" {
		t.Fatalf("Nodes = %+v, want updated metadata", got)
	}
}

func TestOpenReadOnlyMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "stats.db")
	_, err := OpenReadOnly(missing)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly missing = %v, want os.ErrNotExist", err)
	}
}

func TestReadOnlyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Record(probe(t0, "p1/a", true, 100))
	mustFlush(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	// Writes are silently ignored, reads work (WAL, same process).
	ro.Record(probe(t0, "p1/a", true, 500))
	if err := ro.SetNodes([]NodeInfo{{Label: "p1/a", Provider: "x", Country: "DE"}}); err != nil {
		t.Fatalf("SetNodes on read-only: %v", err)
	}
	if err := ro.Flush(context.Background()); err != nil {
		t.Fatalf("Flush on read-only: %v", err)
	}
	if ro.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", ro.Dropped())
	}
	got, err := ro.Nodes(Query{Label: "p1/a"})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Probes != 1 || got[0].DelayAvgMS != 100 {
		t.Fatalf("Nodes on read-only = %+v", got)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("Close read-only: %v", err)
	}
	// Second Close is idempotent.
	if err := ro.Close(); err != nil {
		t.Fatalf("second Close read-only: %v", err)
	}
}

func TestFlushErrorKeepsSampleAndCloseReportsIt(t *testing.T) {
	s := newTestStore(t, nil)
	// Break the database underneath the store.
	if err := s.db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}
	s.Record(probe(t0, "p1/a", true, 100))
	flush := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.Flush(ctx)
	}
	if err := flush(); err == nil {
		t.Fatal("Flush on a broken database = nil, want error")
	}
	// The sample stayed queued, so the retry fails again.
	if err := flush(); err == nil {
		t.Fatal("second Flush on a broken database = nil, want error (sample kept)")
	}
	// Close neither hangs with a pending batch nor hides the lost flush.
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Close = nil, want the final flush error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return within 10s")
	}
}

// waitState polls an internal condition (no sleeps, a deadline) until it
// holds.
func waitState(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// TestCloseWhileSetNodesBlockedOnFullQueue reproduces the lock cycle: a
// sender holds the read lock and blocks on a full queue, Close waits for the
// write lock, and the writer must still be able to drain. The writer starts
// only after both are in place, so a writer that needed the mutex would
// deadlock here.
func TestCloseWhileSetNodesBlockedOnFullQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	s, err := openStore(Options{Path: path, Now: clock(0), QueueSize: 1})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	startWriter := sync.OnceFunc(s.start)
	// Registered after nothing else: runs first, so the store is closable.
	t.Cleanup(func() {
		startWriter()
		s.Close()
	})

	// The queue (capacity 1) is full: the next send blocks.
	s.Record(probe(t0, "p1/a", true, 100))

	setNodesDone := make(chan error, 1)
	go func() {
		setNodesDone <- s.SetNodes([]NodeInfo{{Label: "p1/a", Provider: "prov1", Country: "DE"}})
	}()
	// SetNodes holds the read lock exactly while it is blocked on the queue.
	waitState(t, "SetNodes to hold the read lock", func() bool {
		if s.mu.TryLock() {
			s.mu.Unlock()
			return false
		}
		return true
	})

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	// A pending writer lock makes new readers fail: Close is waiting.
	waitState(t, "Close to wait for the write lock", func() bool {
		if s.mu.TryRLock() {
			s.mu.RUnlock()
			return false
		}
		return true
	})

	startWriter()
	select {
	case err := <-setNodesDone:
		if err != nil {
			t.Fatalf("SetNodes: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SetNodes did not complete: the writer could not drain")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not complete")
	}
}

func TestCloseRacesWithWriters(t *testing.T) {
	s := newTestStore(t, func(o *Options) { o.QueueSize = 1 })
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				s.Record(probe(t0, "p1/a", true, 100))
				// After Close this is a no-op without error.
				if err := s.SetNodes([]NodeInfo{{Label: "p1/a", Provider: "prov1", Country: "DE"}}); err != nil {
					t.Errorf("SetNodes: %v", err)
					return
				}
			}
		}()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()
	for pending := 2; pending > 0; pending-- {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
			closeDone = nil
		case <-workersDone:
			workersDone = nil
		case <-time.After(10 * time.Second):
			t.Fatal("Close or the workers did not finish within 10s")
		}
	}
}
