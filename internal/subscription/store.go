package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// validNameRe restricts subscription names to a single safe path element.
var validNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Snapshot is a stored subscription: when it was fetched and what it
// contained.
type Snapshot struct {
	FetchedAt time.Time `json:"fetched_at"`
	Nodes     []Node    `json:"nodes"`
	Skipped   int       `json:"skipped"`
}

// Store persists subscriptions as JSON files in dir. Files carry the raw
// proxy mappings (credentials) and are therefore secret: mode 0600.
type Store struct {
	dir string
}

// storeMu serialises in-process writers across all Store values: several
// stores may point at one directory.
var storeMu sync.Mutex

// NewStore returns a store rooted at dir (normally <home>/subscriptions).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

type diskNode struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Server  string         `json:"server"`
	Port    int            `json:"port"`
	Country string         `json:"country,omitempty"`
	Raw     map[string]any `json:"raw"`
}

type diskSnapshot struct {
	FetchedAt time.Time  `json:"fetched_at"`
	Nodes     []diskNode `json:"nodes"`
	Skipped   int        `json:"skipped"`
}

func validName(name string) error {
	if !validNameRe.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid subscription name %q: must match [A-Za-z0-9._-]{1,64}", name)
	}
	return nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// Save atomically writes the snapshot to <name>.json.
func (s *Store) Save(name string, snap Snapshot) error {
	if err := validName(name); err != nil {
		return err
	}
	d := diskSnapshot{FetchedAt: snap.FetchedAt, Skipped: snap.Skipped, Nodes: make([]diskNode, 0, len(snap.Nodes))}
	for _, n := range snap.Nodes {
		d.Nodes = append(d.Nodes, diskNode{ID: n.ID, Name: n.Name, Type: n.Type, Server: n.Server, Port: n.Port, Country: n.Country, Raw: n.Raw})
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode subscription %q: %w", name, err)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	return writeFileAtomic(s.path(name), data)
}

// Load reads a snapshot; a missing subscription reports os.ErrNotExist.
func (s *Store) Load(name string) (*Snapshot, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("subscription %q: %w", name, os.ErrNotExist)
		}
		return nil, fmt.Errorf("read subscription %q: %w", name, err)
	}
	var d diskSnapshot
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("subscription %q is corrupt: %w", name, err)
	}
	snap := &Snapshot{FetchedAt: d.FetchedAt, Skipped: d.Skipped, Nodes: make([]Node, 0, len(d.Nodes))}
	for _, n := range d.Nodes {
		snap.Nodes = append(snap.Nodes, Node{ID: n.ID, Name: n.Name, Type: n.Type, Server: n.Server, Port: n.Port, Country: n.Country, Raw: n.Raw})
	}
	return snap, nil
}

// List returns the stored subscription names, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if filepath.Ext(n) != ".json" {
			continue
		}
		name := n[:len(n)-len(".json")]
		if validName(name) != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes a stored subscription; a missing one reports
// os.ErrNotExist.
func (s *Store) Remove(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := os.Remove(s.path(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("subscription %q: %w", name, os.ErrNotExist)
		}
		return fmt.Errorf("remove subscription %q: %w", name, err)
	}
	return nil
}

// writeFileAtomic writes data to path atomically: a temporary file is
// created in the same directory with mode 0600, fsynced and renamed over
// the target. The directory is created with mode 0700 when missing.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create subscriptions directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".netivri-sub-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write subscription: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync subscription: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close subscription: %w", err)
	}
	if err := renameReplace(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace subscription: %w", err)
	}
	return nil
}
