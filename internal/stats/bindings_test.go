package stats

import (
	"fmt"
	"testing"
	"time"
)

// bindingBase anchors the fixture times inside the TTL.
var bindingBase = time.Now().Add(-time.Hour).Truncate(time.Second).UTC()

// v3NodeName is the display name the v3 fixture stores and the migration
// tests expect back unchanged.
const v3NodeName = "Finland 1"

// makeV3DB creates a real schema version 3 database: the v3 DDL applied
// exactly as the v3 code did (v1 and v2 tables plus the node.name column),
// data in the node, sample, account and account_sample tables, and
// user_version = 3.
func makeV3DB(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	for _, stmt := range []string{schemaV1, schemaV2, schemaV3, "PRAGMA user_version = 3"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO node (id, label, provider, country, name, first_seen, last_seen) VALUES (1, 'p1/a', 'prov1', 'DE', '" + v3NodeName + "', 100, 100)"); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO sample (ts, node, kind, ok, ms, bytes) VALUES (?, 1, 1, 1, 100, 0)", bindingBase.UnixMilli()); err != nil {
		t.Fatalf("insert sample: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO account (id, login, first_seen, last_seen) VALUES (1, 'alice', 100, 100)"); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO account_sample (ts, account, node, kind, ok, ms, bytes, up, down) VALUES (?, 1, 1, 2, 1, 100, 0, 0, 0)",
		bindingBase.UnixMilli()); err != nil {
		t.Fatalf("insert account_sample: %v", err)
	}
}

func TestMigrateV3ToV4KeepsData(t *testing.T) {
	path := t.TempDir() + "/stats.db"
	makeV3DB(t, path)

	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open on a v3 database: %v", err)
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
		t.Fatalf("node rows = %d, want 1 (v3 data kept)", n)
	}
	if n := countRows(t, raw, "sample"); n != 1 {
		t.Fatalf("sample rows = %d, want 1 (v3 data kept)", n)
	}
	if n := countRows(t, raw, "account_sample"); n != 1 {
		t.Fatalf("account_sample rows = %d, want 1 (v3 data kept)", n)
	}
	// The display name survived and the new table is usable.
	nodes, err := s.Nodes(Query{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != v3NodeName {
		t.Fatalf("Nodes on migrated v3 = %+v", nodes)
	}
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'example.com', 'p1/a', 1, 2)"); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
}

func TestMigrateV2ToV4KeepsData(t *testing.T) {
	path := t.TempDir() + "/stats.db"
	makeV2DB(t, path)
	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open on a v2 database: %v", err)
	}
	defer s.Close()
	raw := openRaw(t, path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 4 || s.version != 4 {
		t.Fatalf("version = %d/%d, want 4", s.version, version)
	}
	if n := countRows(t, raw, "node"); n != 1 {
		t.Fatalf("node rows = %d, want 1 (v2 data kept)", n)
	}
	if n := countRows(t, raw, "account_sample"); n != 1 {
		t.Fatalf("account_sample rows = %d, want 1 (v2 data kept)", n)
	}
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'example.com', 'p1/a', 1, 2)"); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
}

func TestMigrateV1ToV4KeepsData(t *testing.T) {
	path := t.TempDir() + "/stats.db"
	makeV1DB(t, path)
	s, err := Open(Options{Path: path, Now: clock(0)})
	if err != nil {
		t.Fatalf("Open on a v1 database: %v", err)
	}
	defer s.Close()
	raw := openRaw(t, path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 4 || s.version != 4 {
		t.Fatalf("version = %d/%d, want 4", s.version, version)
	}
	if n := countRows(t, raw, "node"); n != 1 {
		t.Fatalf("node rows = %d, want 1 (v1 data kept)", n)
	}
	if n := countRows(t, raw, "sample"); n != 3 {
		t.Fatalf("sample rows = %d, want 3 (v1 data kept)", n)
	}
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'example.com', 'p1/a', 1, 2)"); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
}

func TestFreshOpenCreatesSchemaV4(t *testing.T) {
	s := newTestStore(t, nil)
	raw := openRaw(t, s.opts.Path)
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 4 {
		t.Fatalf("user_version = %d, want 4", version)
	}
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'example.com', 'p1/a', 1, 2)"); err != nil {
		t.Fatalf("insert binding on a fresh database: %v", err)
	}
}

func TestBindingsRoundTrip(t *testing.T) {
	s := newTestStore(t, nil)
	rows := []Binding{
		{Login: "alice", Site: "example.com", Label: "p1/a", BoundAt: bindingBase, LastUsed: bindingBase},
		{Login: "alice", Site: "example.org", Label: "p1/b", BoundAt: bindingBase, LastUsed: bindingBase},
	}
	if err := s.ReplaceBindings(rows); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err := s.LoadBindings(bindingBase.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	// ORDER BY last_used DESC; both rows share one last_used, so the order
	// within it is unspecified: index by site.
	bySite := map[string]Binding{}
	for _, b := range got {
		bySite[b.Site] = b
	}
	if len(got) != 2 || bySite["example.com"].Label != "p1/a" ||
		!bySite["example.com"].BoundAt.Equal(bindingBase) || !bySite["example.com"].LastUsed.Equal(bindingBase) ||
		bySite["example.org"].Label != "p1/b" {
		t.Fatalf("LoadBindings = %+v", got)
	}

	// A replace without the second row deletes it and upserts the first.
	rows2 := []Binding{
		{Login: "alice", Site: "example.com", Label: "p1/c", BoundAt: bindingBase, LastUsed: bindingBase.Add(time.Minute)},
	}
	if err := s.ReplaceBindings(rows2); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err = s.LoadBindings(bindingBase.Add(2*time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p1/c" || !got[0].LastUsed.Equal(bindingBase.Add(time.Minute)) {
		t.Fatalf("LoadBindings after replace = %+v", got)
	}

	// An empty set clears the table.
	if err := s.ReplaceBindings(nil); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err = s.LoadBindings(bindingBase.Add(3*time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadBindings after clear = %+v", got)
	}
}

func TestLoadBindingsDeletesStaleRows(t *testing.T) {
	s := newTestStore(t, nil)
	// The write keeps only the fresh row (the store clock cuts at
	// bindingBase - BindingTTL, so a directly inserted older row is needed to
	// exercise the delete on load).
	if err := s.ReplaceBindings([]Binding{
		{Login: "alice", Site: "fresh.example", Label: "p1/a", BoundAt: bindingBase, LastUsed: bindingBase},
	}); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	raw := openRaw(t, s.opts.Path)
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'stale.example', 'p1/b', ?, ?)",
		bindingBase.Add(-2*BindingTTL).UnixMilli(), bindingBase.Add(-2*BindingTTL).UnixMilli()); err != nil {
		t.Fatalf("insert stale row: %v", err)
	}
	got, err := s.LoadBindings(bindingBase.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Site != "fresh.example" {
		t.Fatalf("LoadBindings = %+v, want only the fresh row", got)
	}
	if n := countRows(t, raw, "binding"); n != 1 {
		t.Fatalf("binding rows = %d, want 1 (stale row deleted on load)", n)
	}
}

func TestReplaceBindingsSkipsExpiredRows(t *testing.T) {
	// The store clock sits 48 h after the fixture times, so an incoming row
	// with an old last_used is expired and must not be written.
	s := newTestStore(t, func(o *Options) { o.Now = clock(48 * time.Hour) })
	old := bindingBase
	fresh := base.Add(48 * time.Hour)
	if err := s.ReplaceBindings([]Binding{
		{Login: "alice", Site: "fresh.example", Label: "p1/a", BoundAt: fresh, LastUsed: fresh},
		{Login: "alice", Site: "expired.example", Label: "p1/b", BoundAt: old, LastUsed: old},
	}); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	// The table is observed directly, before any load: the expired incoming
	// row was never written. (A load could not prove this, because it
	// deletes expired rows itself.)
	raw := openRaw(t, s.opts.Path)
	if n := countRows(t, raw, "binding"); n != 1 {
		t.Fatalf("binding rows right after the write = %d, want 1 (the expired row is never written)", n)
	}
	got, err := s.LoadBindings(fresh.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Site != "fresh.example" {
		t.Fatalf("LoadBindings = %+v, want only the fresh row", got)
	}
}

func TestBindingsSurviveReopen(t *testing.T) {
	opts := Options{Path: t.TempDir() + "/stats.db", Now: clock(0)}
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ReplaceBindings([]Binding{
		{Login: "alice", Site: "example.com", Label: "p1/a", BoundAt: base, LastUsed: base},
	}); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenReadOnly(opts.Path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	got, err := ro.LoadBindings(base.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 1 || got[0].Label != "p1/a" {
		t.Fatalf("read-only LoadBindings = %+v", got)
	}
	// A stale row on a read-only store is filtered, not deleted.
	raw := openRaw(t, opts.Path)
	if _, err := raw.Exec(
		"INSERT INTO binding (login, site, label, bound_at, last_used) VALUES ('alice', 'stale.example', 'p1/b', ?, ?)",
		base.Add(-2*BindingTTL).UnixMilli(), base.Add(-2*BindingTTL).UnixMilli()); err != nil {
		t.Fatalf("insert stale row: %v", err)
	}
	got, err = ro.LoadBindings(base.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings with a stale row: %v", err)
	}
	if len(got) != 1 || got[0].Site != "example.com" {
		t.Fatalf("read-only LoadBindings = %+v, want the fresh row only", got)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close ro: %v", err)
	}

	// A writable reopen with a clock past the TTL prunes the rows on write.
	s2, err := Open(Options{Path: opts.Path, Now: clock(48 * time.Hour)})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.ReplaceBindings(nil); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err = s2.LoadBindings(base.Add(48*time.Hour), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expired rows survived = %+v", got)
	}
}

func TestBindingsOlderSchemaIsEmpty(t *testing.T) {
	path := t.TempDir() + "/stats.db"
	makeV3DB(t, path)

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	got, err := ro.LoadBindings(bindingBase, BindingTTL, 0)
	if err != nil {
		t.Fatalf("read-only LoadBindings on v3: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadBindings on v3 = %+v, want none", got)
	}
	if err := ro.ReplaceBindings([]Binding{{Login: "a", Site: "b", Label: "c", BoundAt: bindingBase, LastUsed: bindingBase}}); err != nil {
		t.Fatalf("ReplaceBindings on v3 read-only: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A v1 database opens read-only too and has no bindings.
	path1 := t.TempDir() + "/stats.db"
	db := openRaw(t, path1)
	for _, stmt := range []string{schemaV1, "PRAGMA user_version = 1"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	db.Close()
	ro1, err := OpenReadOnly(path1)
	if err != nil {
		t.Fatalf("OpenReadOnly v1: %v", err)
	}
	t.Cleanup(func() { ro1.Close() })
	if got, err := ro1.LoadBindings(bindingBase, BindingTTL, 0); err != nil || len(got) != 0 {
		t.Fatalf("LoadBindings on v1 = %+v, %v", got, err)
	}
}

func TestLoadBindingsKeepsTheFreshestWithinTheLimit(t *testing.T) {
	s := newTestStore(t, nil)
	rows := make([]Binding, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, Binding{
			Login:    "alice",
			Site:     fmt.Sprintf("site-%d.example", i),
			Label:    "p1/a",
			BoundAt:  base,
			LastUsed: base.Add(time.Duration(i) * time.Minute), // site-4 is the freshest
		})
	}
	if err := s.ReplaceBindings(rows); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err := s.LoadBindings(base.Add(time.Hour), BindingTTL, 2)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadBindings = %d rows, want the limit 2", len(got))
	}
	if got[0].Site != "site-4.example" || got[1].Site != "site-3.example" {
		t.Fatalf("LoadBindings = %+v, want the freshest rows first", got)
	}
}

func TestBindingsBoundedCount(t *testing.T) {
	s := newTestStore(t, nil)
	rows := make([]Binding, 0, maxBindingRows+1)
	for i := 0; i <= maxBindingRows; i++ {
		rows = append(rows, Binding{
			Login:    "alice",
			Site:     fmt.Sprintf("site-%d.example", i),
			Label:    "p1/a",
			BoundAt:  base,
			LastUsed: base,
		})
	}
	if err := s.ReplaceBindings(rows); err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	got, err := s.LoadBindings(base.Add(time.Minute), BindingTTL, 0)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got) != maxBindingRows {
		t.Fatalf("LoadBindings = %d rows, want %d", len(got), maxBindingRows)
	}
}
