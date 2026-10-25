package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// Pure-Go SQLite driver (registers "sqlite"); release builds use CGO_ENABLED=0.
	_ "modernc.org/sqlite"
)

// Kind classifies a sample.
type Kind uint8

// Sample kinds.
const (
	KindProbe      Kind = 1 // ms = probe delay
	KindDial       Kind = 2 // ms = dial time
	KindThroughput Kind = 3 // bytes moved in a window of ms milliseconds
)

// Sample is one measurement for a node label.
type Sample struct {
	At    time.Time
	Label string
	Kind  Kind
	OK    bool          // always true for KindThroughput
	Delay time.Duration // probe delay, dial time or throughput window
	Bytes int64         // KindThroughput only
}

// NodeInfo carries the provider/country metadata of a node label. Name is
// the display name from the subscription, sanitized and optional.
type NodeInfo struct{ Label, Provider, Country, Name string }

// Options configures Open. Zero values select the defaults.
type Options struct {
	Path      string
	Retention time.Duration    // raw samples; 0 = 7 days
	QueueSize int              // 0 = 4096
	Now       func() time.Time // nil = time.Now (tests)
}

const (
	driverName        = "sqlite"
	schemaVersion     = 4
	flushBatchSize    = 256
	flushInterval     = time.Second
	pruneInterval     = time.Hour
	defaultRetention  = 7 * 24 * time.Hour
	defaultQueueSize  = 4096
	hourlyTTL         = 90 * 24 * time.Hour
	maxPendingSamples = 16384 // retry buffer bound; the oldest excess is dropped
)

// schemaV1 is the version 1 DDL.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS node (
  id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL DEFAULT '', country TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sample (
  ts INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  ok INTEGER NOT NULL, ms INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS sample_node_ts ON sample(node, ts);
CREATE INDEX IF NOT EXISTS sample_ts ON sample(ts);
CREATE TABLE IF NOT EXISTS hourly (
  hour INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  n INTEGER NOT NULL, ok INTEGER NOT NULL, ms_sum INTEGER NOT NULL,
  ms_max INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (hour, node, kind)) WITHOUT ROWID;
`

// schemaV2 is the version 2 DDL. It is purely additive: the version 1
// tables are untouched, so a version 1 database keeps all of its data on
// migration.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS account (
  id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS account_sample (
  ts INTEGER NOT NULL, account INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  ok INTEGER NOT NULL, ms INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  up INTEGER NOT NULL DEFAULT 0, down INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS account_sample_account_ts ON account_sample(account, ts);
CREATE INDEX IF NOT EXISTS account_sample_node_ts ON account_sample(node, ts);
CREATE INDEX IF NOT EXISTS account_sample_ts ON account_sample(ts);
CREATE TABLE IF NOT EXISTS account_hourly (
  hour INTEGER NOT NULL, account INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  n INTEGER NOT NULL, ok INTEGER NOT NULL, ms_sum INTEGER NOT NULL, ms_max INTEGER NOT NULL,
  bytes INTEGER NOT NULL, up INTEGER NOT NULL DEFAULT 0, down INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (hour, account, node, kind)) WITHOUT ROWID;
`

// schemaV3 is the version 3 migration. It is purely additive: the version
// 2 tables are untouched, so a version 1 or 2 database keeps all of its
// data on migration. A read-only open of a v1 or v2 database has no name
// column; the read queries handle that.
const schemaV3 = `
ALTER TABLE node ADD COLUMN name TEXT NOT NULL DEFAULT '';
`

// schemaV4 is the version 4 migration. It is purely additive: the version 3
// tables are untouched, so a version 1, 2 or 3 database keeps all of its
// data on migration. A read-only open of an older database (which does not
// migrate) simply has no bindings; LoadBindings and ReplaceBindings treat
// such a database as empty.
const schemaV4 = `
CREATE TABLE IF NOT EXISTS binding (
  login TEXT NOT NULL, site TEXT NOT NULL, label TEXT NOT NULL,
  bound_at INTEGER NOT NULL, last_used INTEGER NOT NULL,
  PRIMARY KEY (login, site));
`

// Store is a SQLite-backed sample store. Create with Open or OpenReadOnly.
type Store struct {
	db   *sql.DB
	ro   bool
	opts Options
	now  func() time.Time
	// version is the schema version of the opened database.
	version int
	// hasName reports whether the node table has the schema v3 name column.
	// Probed once at open, immutable afterwards: read-only opens of a v1 or
	// v2 database see false, fresh or migrated stores true.
	hasName bool

	// queue carries samples, flush requests and SetNodes requests to the
	// writer goroutine, in order.
	queue   chan item
	ids     map[string]int64 // label -> node id, writer goroutine only
	accIDs  map[string]int64 // login -> account id, writer goroutine only
	dropped atomic.Uint64
	done    chan struct{}
	wg      sync.WaitGroup

	// mu guards closed against the queue senders. Senders hold RLock while
	// sending; Close takes Lock, sets closed and only then closes the queue.
	// The writer goroutine never takes it, so it can always drain.
	mu     sync.RWMutex
	closed bool

	// writerErr is the error of the final flush, set by the writer
	// goroutine before wg.Done and read by Close after wg.Wait.
	writerErr error
}

// item is one unit of work for the writer goroutine.
type item struct {
	sample   Sample
	acct     *AccountSample
	nodes    []NodeInfo
	bindings *bindingWrite
	ack      chan error // flush, SetNodes and ReplaceBindings requests carry an ack
}

// Open prepares the store and starts the writer and retention goroutines.
func Open(o Options) (*Store, error) {
	s, err := openStore(o)
	if err != nil {
		return nil, err
	}
	s.start()
	return s, nil
}

// openStore does everything Open does except starting the goroutines; tests
// use it to record into the queue before the writer runs.
func openStore(o Options) (*Store, error) {
	if o.Path == "" {
		return nil, errors.New("stats: empty path")
	}
	if o.Retention <= 0 {
		o.Retention = defaultRetention
	}
	if o.QueueSize <= 0 {
		o.QueueSize = defaultQueueSize
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
		return nil, fmt.Errorf("stats: create directory: %w", err)
	}
	d, err := dsn(o.Path, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, d)
	if err != nil {
		return nil, fmt.Errorf("stats: open: %w", err)
	}
	// One connection: SQLite does not benefit from more writers and it
	// serialises the writer and the retention loop without lock waits.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("stats: open %s: %w", o.Path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	s := newStore(db, o, false)
	s.version = schemaVersion
	s.hasName = probeHasName(db)
	// The contract prunes at open. A failure here (for example a busy
	// database) is not fatal: the data stays and the hourly ticker retries.
	_ = s.prune(s.now())
	return s, nil
}

// OpenReadOnly opens an existing database for reading. It returns an error
// that satisfies errors.Is(err, os.ErrNotExist) when the file does not
// exist, and never creates or migrates anything.
func OpenReadOnly(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("stats: empty path")
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stats: open %s: %w", path, err)
		}
		return nil, fmt.Errorf("stats: stat %s: %w", path, err)
	}
	d, err := dsn(path, true)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, d)
	if err != nil {
		return nil, fmt.Errorf("stats: open: %w", err)
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("stats: open %s: %w", path, err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("stats: read schema version: %w", err)
	}
	if version > schemaVersion {
		db.Close()
		return nil, fmt.Errorf("stats: schema version %d is newer than supported %d", version, schemaVersion)
	}
	s := newStore(db, Options{Path: path, Retention: defaultRetention}, true)
	s.version = version
	s.hasName = version >= 3 && probeHasName(db)
	return s, nil
}

func newStore(db *sql.DB, o Options, ro bool) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Store{db: db, ro: ro, opts: o, now: o.Now, ids: map[string]int64{}, accIDs: map[string]int64{}}
	if !ro {
		s.queue = make(chan item, o.QueueSize)
		s.done = make(chan struct{})
	}
	return s
}

func (s *Store) start() {
	s.wg.Add(2)
	go s.writerLoop()
	go s.retentionLoop()
}

// dsn resolves the path and builds its file: URI.
func dsn(path string, readOnly bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("stats: resolve path %s: %w", path, err)
	}
	return fileURI(filepath.ToSlash(abs), readOnly), nil
}

// fileURI builds a file: URI DSN from an absolute slash path. The file:
// scheme keeps the query for SQLite's own URI parser, whose path form
// (forward slashes, percent-encoding, the Windows drive letter after the
// empty authority) is portable across operating systems. The slash prefix is
// added only when missing: POSIX paths are already absolute, a Windows drive
// path C:/a/b becomes /C:/a/b. The pragmas run on every new connection; a
// read-only connection does not touch the journal mode.
func fileURI(slashAbs string, readOnly bool) string {
	p := slashAbs
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	if readOnly {
		q.Set("mode", "ro")
		q["_pragma"] = []string{"busy_timeout(5000)"}
	} else {
		q["_pragma"] = []string{
			"busy_timeout(5000)",
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// probeHasName reports whether the node table has the schema v3 name
// column. Run once at store creation, before any transaction is open, so a
// writable store's single connection is free and no query inside a
// transaction can deadlock waiting for a second one.
func probeHasName(db *sql.DB) bool {
	rows, err := db.Query("PRAGMA table_info(node)")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false
		}
		if name == "name" {
			return true
		}
	}
	return false
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("stats: read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("stats: schema version %d is newer than supported %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	// Every step is gated by the current version, so an upgrade from any
	// version runs only the DDL it has not applied yet: schemaV3's ALTER
	// fails if the name column already exists, and the other DDLs are
	// CREATE ... IF NOT EXISTS. A fresh database (version 0) runs all four;
	// a version 1, 2 or 3 database keeps all of its data.
	for _, step := range []struct {
		version int
		ddl     string
	}{
		{1, schemaV1},
		{2, schemaV2},
		{3, schemaV3},
		{4, schemaV4},
	} {
		if version >= step.version {
			continue
		}
		if _, err := db.Exec(step.ddl); err != nil {
			return fmt.Errorf("stats: migrate: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("stats: set schema version: %w", err)
	}
	return nil
}

// Record queues a sample without blocking. When the queue is full the sample
// is dropped and counted; on a read-only store or after Close it is a no-op.
func (s *Store) Record(sm Sample) {
	if s.ro {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- item{sample: sm}:
	default:
		s.dropped.Add(1)
	}
}

// RecordAccount queues a per-account sample without blocking. When the
// queue is full the sample is dropped and counted; on a read-only store or
// after Close it is a no-op.
func (s *Store) RecordAccount(sm AccountSample) {
	if s.ro {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- item{acct: &sm}:
	default:
		s.dropped.Add(1)
	}
}

// Dropped reports the number of samples dropped because the queue was full
// or the retry buffer overflowed.
func (s *Store) Dropped() uint64 { return s.dropped.Load() }

// SetNodes upserts provider/country for the labels, creating missing rows.
// It is safe to call while the writer runs; the work is serialised through
// the writer. On a read-only store or after Close it is a no-op.
func (s *Store) SetNodes(infos []NodeInfo) error {
	if s.ro || len(infos) == 0 {
		return nil
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil
	}
	ack := make(chan error, 1)
	s.queue <- item{nodes: infos, ack: ack}
	s.mu.RUnlock()
	return <-ack
}

// Flush writes every sample queued before the call and returns when they are
// on disk. It honours ctx. On a read-only store, or after Close, it is a
// no-op. When the database keeps failing, the samples stay queued and every
// Flush returns the current error.
func (s *Store) Flush(ctx context.Context) error {
	if s.ro {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil
	}
	ack := make(chan error, 1)
	select {
	case s.queue <- item{ack: ack}:
	case <-ctx.Done():
		s.mu.RUnlock()
		return ctx.Err()
	}
	s.mu.RUnlock()
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting samples, drains the queue, writes it, stops the
// goroutines and closes the database. It is idempotent and returns the
// final flush error joined with the close error.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.ro {
		return s.db.Close()
	}
	close(s.queue)
	close(s.done)
	s.wg.Wait()
	// The WaitGroup wait happens-before reading writerErr.
	return errors.Join(s.writerErr, s.db.Close())
}
