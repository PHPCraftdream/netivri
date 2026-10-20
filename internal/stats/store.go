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

// NodeInfo carries the provider/country metadata of a node label.
type NodeInfo struct{ Label, Provider, Country string }

// Options configures Open. Zero values select the defaults.
type Options struct {
	Path      string
	Retention time.Duration    // raw samples; 0 = 7 days
	QueueSize int              // 0 = 4096
	Now       func() time.Time // nil = time.Now (tests)
}

const (
	driverName        = "sqlite"
	schemaVersion     = 1
	flushBatchSize    = 256
	flushInterval     = time.Second
	pruneInterval     = time.Hour
	defaultRetention  = 7 * 24 * time.Hour
	defaultQueueSize  = 4096
	hourlyTTL         = 90 * 24 * time.Hour
	maxPendingSamples = 16384 // retry buffer bound; the oldest excess is dropped
)

// schema is the version 1 DDL.
const schema = `
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

// Store is a SQLite-backed sample store. Create with Open or OpenReadOnly.
type Store struct {
	db   *sql.DB
	ro   bool
	opts Options
	now  func() time.Time

	// queue carries samples, flush requests and SetNodes requests to the
	// writer goroutine, in order.
	queue   chan item
	ids     map[string]int64 // label -> node id, writer goroutine only
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
	sample Sample
	nodes  []NodeInfo
	ack    chan error // flush and SetNodes requests carry an ack
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
	return newStore(db, Options{Path: path, Retention: defaultRetention}, true), nil
}

func newStore(db *sql.DB, o Options, ro bool) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Store{db: db, ro: ro, opts: o, now: o.Now, ids: map[string]int64{}}
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
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("stats: migrate: %w", err)
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
