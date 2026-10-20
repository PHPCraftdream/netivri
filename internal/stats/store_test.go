package stats

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// base anchors the fixtures inside the default 24 h window. Read-only
// stores always use the real clock, so "now" must be recent.
var base = time.Now().Add(-time.Minute).Truncate(time.Second).UTC()

// clock returns a Now function pinned to base plus d.
func clock(d time.Duration) func() time.Time {
	return func() time.Time { return base.Add(d) }
}

// newTestStore opens a store on a fresh path and registers Close before the
// temp directory is removed.
func newTestStore(t *testing.T, mutate func(*Options)) *Store {
	t.Helper()
	opts := Options{
		Path: filepath.Join(t.TempDir(), "stats.db"),
		Now:  clock(0),
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// openRaw opens a second raw connection to the same file.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := dsn(path, false)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	db, err := sql.Open(driverName, d)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("raw ping: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// countRows returns the number of rows of the given table.
func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func mustFlush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestOpenCreatesSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "stats.db")
	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	raw := openRaw(t, path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestReopenKeepsData(t *testing.T) {
	opts := Options{Path: filepath.Join(t.TempDir(), "stats.db"), Now: clock(0)}
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Record(Sample{At: base, Label: "p/n1", Kind: KindProbe, OK: true, Delay: 100 * time.Millisecond})
	mustFlush(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenReadOnly(opts.Path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	got, err := ro.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p/n1" || got[0].Probes != 1 || got[0].ProbesOK != 1 {
		t.Fatalf("Nodes after reopen = %+v", got)
	}

	// A read-write reopen keeps the row too.
	s2, err := Open(Options{Path: opts.Path, Now: clock(0)})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	got, err = s2.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 || got[0].Probes != 1 {
		t.Fatalf("Nodes after reopen = %+v", got)
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	opts := Options{Path: filepath.Join(t.TempDir(), "stats.db"), Now: clock(0)}
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw := openRaw(t, opts.Path)
	if _, err := raw.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if _, err := Open(Options{Path: opts.Path, Now: clock(0)}); err == nil {
		t.Fatal("Open accepted a newer schema version")
	}
	if _, err := OpenReadOnly(opts.Path); err == nil {
		t.Fatal("OpenReadOnly accepted a newer schema version")
	}
}

func TestRecordAfterCloseIsNoop(t *testing.T) {
	s := newTestStore(t, nil)
	mustFlush(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	s.Record(Sample{At: base, Label: "p/n1", Kind: KindProbe, OK: true})
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Close: %v", err)
	}
	if err := s.SetNodes([]NodeInfo{{Label: "p/n1"}}); err != nil {
		t.Fatalf("SetNodes after Close: %v", err)
	}
	if s.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", s.Dropped())
	}
}

func TestFileURI(t *testing.T) {
	for _, tc := range []struct {
		slash    string
		ro       bool
		wantHead string // URI up to and including the '?'
		wantPath string // decoded URI path
		wantMode string
		wantP    []string
	}{
		{
			slash:    "/tmp/x/stats.db",
			wantHead: "file:///tmp/x/stats.db?",
			wantPath: "/tmp/x/stats.db",
			wantMode: "",
			wantP:    []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)"},
		},
		{
			slash:    "C:/Users/a b/stats.db",
			wantHead: "file:///C:/Users/a%20b/stats.db?",
			wantPath: "/C:/Users/a b/stats.db",
			wantMode: "",
			wantP:    []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)"},
		},
		{
			slash:    "C:/Users/a b/stats.db",
			ro:       true,
			wantHead: "file:///C:/Users/a%20b/stats.db?",
			wantPath: "/C:/Users/a b/stats.db",
			wantMode: "ro",
			wantP:    []string{"busy_timeout(5000)"},
		},
	} {
		got := fileURI(tc.slash, tc.ro)
		if len(got) < len(tc.wantHead) || got[:len(tc.wantHead)] != tc.wantHead {
			t.Fatalf("fileURI(%q, ro=%v) = %q, want prefix %q", tc.slash, tc.ro, got, tc.wantHead)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		if u.Path != tc.wantPath {
			t.Fatalf("fileURI(%q) path = %q, want %q", tc.slash, u.Path, tc.wantPath)
		}
		if u.Scheme != "file" {
			t.Fatalf("fileURI(%q) scheme = %q", tc.slash, u.Scheme)
		}
		if gotMode := u.Query().Get("mode"); gotMode != tc.wantMode {
			t.Fatalf("fileURI(%q, ro=%v) mode = %q, want %q", tc.slash, tc.ro, gotMode, tc.wantMode)
		}
		p := u.Query()["_pragma"]
		if len(p) != len(tc.wantP) {
			t.Fatalf("fileURI(%q) pragmas = %v, want %v", tc.slash, p, tc.wantP)
		}
		set := map[string]bool{}
		for _, v := range p {
			set[v] = true
		}
		for _, v := range tc.wantP {
			if !set[v] {
				t.Fatalf("fileURI(%q) pragmas %v miss %v", tc.slash, p, v)
			}
		}
	}
}
