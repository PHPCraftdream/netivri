package subscription

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Numbers inside Raw are float64 here because that is what a JSON round trip
// returns; Mihomo's decoder accepts them for integer fields.
func testSnap(fetched time.Time) Snapshot {
	return Snapshot{
		FetchedAt: fetched,
		Skipped:   3,
		Nodes: []Node{
			{ID: "aaaaaaaaaaaa", Name: "🇩🇪 Frankfurt 1", Type: "ss", Server: "example.invalid", Port: 8388, Country: "DE",
				Raw: map[string]any{"type": "ss", "server": "example.invalid", "port": float64(8388), "cipher": "aes-128-gcm", "password": "pass"}},
			{ID: "bbbbbbbbbbbb", Name: "Tokyo 01", Type: "vmess", Server: "example.invalid", Port: 443, Country: "JP",
				Raw: map[string]any{"type": "vmess", "uuid": "00000000-0000-0000-0000-000000000000"}},
		},
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "subscriptions"))
	fetched := time.Now().UTC().Truncate(time.Second)
	snap := testSnap(fetched)
	if err := s.Save("main", snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("main")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.FetchedAt.Equal(fetched) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, fetched)
	}
	if got.Skipped != snap.Skipped {
		t.Errorf("Skipped = %d, want %d", got.Skipped, snap.Skipped)
	}
	if !reflect.DeepEqual(got.Nodes, snap.Nodes) {
		t.Errorf("nodes not preserved:\n got %+v\nwant %+v", got.Nodes, snap.Nodes)
	}
	// Raw mappings are on disk, node JSON keeps them out.
	data, err := os.ReadFile(filepath.Join(dir, "subscriptions", "main.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), `"raw"`) || !strings.Contains(string(data), "password") {
		t.Error("raw mapping missing from the stored file")
	}
	nodeJSON, _ := json.Marshal(snap.Nodes[0])
	if strings.Contains(string(nodeJSON), "password") {
		t.Error("node JSON leaks credentials")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "subscriptions", "main.json"))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
		}
		dinfo, err := os.Stat(filepath.Join(dir, "subscriptions"))
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if dinfo.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", dinfo.Mode().Perm())
		}
	}
}

func TestStoreList(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	for _, name := range []string{"b", "a", "c"} {
		if err := s.Save(name, testSnap(time.Now())); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Errorf("List = %v, want [a b c]", names)
	}
	// Non-subscription files are ignored.
	if err := os.WriteFile(filepath.Join(s.Dir(), "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err = s.List()
	if err != nil || len(names) != 3 {
		t.Errorf("List after stray file = %v, %v", names, err)
	}
	// Missing directory lists empty.
	empty := NewStore(filepath.Join(t.TempDir(), "missing"))
	if names, err := empty.List(); err != nil || len(names) != 0 {
		t.Errorf("List on missing dir = %v, %v", names, err)
	}
}

func TestStoreRemove(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	if err := s.Save("main", testSnap(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("main"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if names, _ := s.List(); len(names) != 0 {
		t.Errorf("List after remove = %v", names)
	}
	err := s.Remove("main")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Remove missing = %v, want os.ErrNotExist", err)
	}
}

func TestStoreNameValidation(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	bad := []string{"", ".", "..", "a/b", "a b", "a\b", strings.Repeat("a", 65), "sub name", "n\n", "..hidden/escape"}
	for _, name := range bad {
		if err := s.Save(name, testSnap(time.Now())); err == nil {
			t.Errorf("Save(%q) accepted", name)
		}
		if _, err := s.Load(name); err == nil {
			t.Errorf("Load(%q) accepted", name)
		}
		if err := s.Remove(name); err == nil {
			t.Errorf("Remove(%q) accepted", name)
		}
	}
	good := []string{"a", "Sub_1", "x.y-z", strings.Repeat("a", 64)}
	for _, name := range good {
		if err := s.Save(name, testSnap(time.Now())); err != nil {
			t.Errorf("Save(%q): %v", name, err)
		}
	}
}

func TestStoreCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subscriptions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if _, err := s.Load("main"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("Load corrupt = %v, want corrupt error", err)
	}
}

func TestStoreLoadMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	_, err := s.Load("main")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load missing = %v, want os.ErrNotExist", err)
	}
}

func TestStoreConcurrentSave(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snap := testSnap(time.Now().UTC().Truncate(time.Second))
			snap.Nodes = snap.Nodes[:1]
			snap.Skipped = i
			if err := s.Save("main", snap); err != nil {
				t.Errorf("concurrent Save: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.Load("main")
	if err != nil {
		t.Fatalf("Load after concurrent saves: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Errorf("nodes = %d, want 1", len(got.Nodes))
	}
}
