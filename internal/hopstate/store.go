package hopstate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/ktav-lang/golang/pure"
)

const (
	maxStoreBytes = 1 << 20
	maxEntries    = 1024
)

// ErrUnreadable marks a hop-state file that cannot be safely read.
var ErrUnreadable = errors.New("hop state is unreadable")

// Store persists hop quarantine state in one private Ktav file.
type Store struct {
	path     string
	encodeFn func([]diskEntry) (string, error)
	lockFn   func() (func(), error)
}

var storeMu sync.Mutex

type diskEntry struct {
	Key       string `json:"key"`
	Trips     int    `json:"trips"`
	NextProbe string `json:"next_probe"`
}

type diskFile struct {
	Version int         `json:"version"`
	Hops    []diskEntry `json:"hops"`
}

// NewStore returns a store rooted at the given state-file path.
func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) acquireLock() (func(), error) {
	if s.lockFn != nil {
		return s.lockFn()
	}
	return s.lock()
}

// Load returns persisted quarantine entries, or an empty slice when the file is absent.
func (s *Store) Load() ([]hoppool.Entry, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	unlock, err := s.acquireLock()
	if err != nil {
		return nil, unreadableError()
	}
	defer unlock()

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []hoppool.Entry{}, nil
	}
	if err != nil {
		return nil, unreadableError()
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil || len(data) > maxStoreBytes || len(data) == 0 {
		return nil, unreadableError()
	}
	text := string(data)
	if _, err := pure.LoadsStrict(text); err != nil {
		return nil, unreadableError()
	}
	var shape map[string]any
	if err := pure.LoadsInto(text, &shape); err != nil || len(shape) != 2 {
		return nil, unreadableError()
	}
	_, hasVersion := shape["version"]
	hopsValue, hasHops := shape["hops"]
	if !hasVersion || !hasHops {
		return nil, unreadableError()
	}
	hops, ok := hopsValue.([]any)
	if !ok || len(hops) > maxEntries {
		return nil, unreadableError()
	}
	for _, value := range hops {
		fields, ok := value.(map[string]any)
		if !ok || len(fields) != 3 {
			return nil, unreadableError()
		}
		if _, ok := fields["key"]; !ok {
			return nil, unreadableError()
		}
		if _, ok := fields["trips"]; !ok {
			return nil, unreadableError()
		}
		if _, ok := fields["next_probe"]; !ok {
			return nil, unreadableError()
		}
	}
	var doc diskFile
	if err := pure.LoadsInto(text, &doc); err != nil || doc.Version != 1 || len(doc.Hops) != len(hops) {
		return nil, unreadableError()
	}
	entries := make(map[string]hoppool.Entry, len(doc.Hops))
	for _, item := range doc.Hops {
		if !validHashKey(item.Key) || item.Trips < 0 {
			continue
		}
		nextProbe, err := time.Parse(time.RFC3339Nano, item.NextProbe)
		if err != nil || !strings.HasSuffix(item.NextProbe, "Z") {
			return nil, unreadableError()
		}
		entries[item.Key] = hoppool.Entry{Key: item.Key, Trips: item.Trips, NextProbe: nextProbe.UTC()}
	}
	return sortedEntries(entries), nil
}

// Save atomically writes normalized quarantine state, removing the file when empty.
func (s *Store) Save(entries []hoppool.Entry) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	unlock, err := s.acquireLock()
	if err != nil {
		return errors.New("cannot save hop state")
	}
	defer unlock()

	normalized := make(map[string]hoppool.Entry, min(len(entries), maxEntries))
	for _, entry := range entries {
		if !validHashKey(entry.Key) || entry.Trips < 0 {
			continue
		}
		entry.NextProbe = entry.NextProbe.UTC()
		normalized[entry.Key] = entry
		if len(normalized) > maxEntries {
			return errors.New("cannot save hop state")
		}
	}
	if len(normalized) == 0 {
		err := os.Remove(s.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cannot save hop state")
		}
		return nil
	}

	disk := make([]diskEntry, 0, len(normalized))
	for _, entry := range sortedEntries(normalized) {
		if year := entry.NextProbe.Year(); year < 0 || year > 9999 {
			return errors.New("cannot save hop state")
		}
		disk = append(disk, diskEntry{Key: entry.Key, Trips: entry.Trips, NextProbe: entry.NextProbe.Format(time.RFC3339Nano)})
	}
	encode := s.encodeFn
	if encode == nil {
		encode = encodeKtav
	}
	text, err := encode(disk)
	if err != nil {
		return errors.New("cannot save hop state")
	}
	if err := writeAtomic(s.path, []byte(text)); err != nil {
		return errors.New("cannot save hop state")
	}
	return nil
}

func encodeKtav(entries []diskEntry) (string, error) {
	return pure.Dumps(diskFile{Version: 1, Hops: entries})
}

func sortedEntries(entries map[string]hoppool.Entry) []hoppool.Entry {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]hoppool.Entry, 0, len(keys))
	for _, key := range keys {
		result = append(result, entries[key])
	}
	return result
}

func unreadableError() error { return fmt.Errorf("cannot read hop state: %w", ErrUnreadable) }

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hop-state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameReplace(name, path); err != nil {
		return err
	}
	keep = true
	return nil
}
