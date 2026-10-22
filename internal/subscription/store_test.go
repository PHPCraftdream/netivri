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

// Numbers inside Raw are float64 here because that is what a decoded file
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
	// The snapshot is a Ktav file with the raw mappings in it; node JSON keeps
	// them out.
	data, err := os.ReadFile(filepath.Join(dir, "subscriptions", "main.ktav"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), "raw") || !strings.Contains(string(data), "password") {
		t.Error("raw mapping missing from the stored file")
	}
	if strings.Contains(string(data), `"raw"`) || strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("the stored file is JSON, want Ktav:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "subscriptions", "main.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a .json file was written next to the .ktav one: %v", err)
	}
	nodeJSON, _ := json.Marshal(snap.Nodes[0])
	if strings.Contains(string(nodeJSON), "password") {
		t.Error("node JSON leaks credentials")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "subscriptions", "main.ktav"))
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

// What provider links hold: numbers, booleans and null written as text, text
// that looks like them, nested lists and maps, other scripts.
func TestStoreRoundTripOfAwkwardValues(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	snap := Snapshot{
		FetchedAt: time.Now().UTC().Truncate(time.Second),
		Nodes: []Node{{
			ID: "cccccccccccc", Name: "🇺🇸 США 5G", Type: "vless", Server: "example.invalid", Port: 443, Country: "US",
			Raw: map[string]any{
				"type": "vless", "server": "example.invalid", "port": float64(443),
				"uuid":    "123456",
				"flow":    "true",
				"sni":     "null",
				"udp":     true,
				"tls":     false,
				"ratio":   0.5,
				"alpn":    []any{"h2", "http/1.1"},
				"empty":   "",
				"ws-opts": map[string]any{"path": "/a b", "headers": map[string]any{"Host": "example.invalid"}},
				"reality-opts": map[string]any{
					"public-key": "abc=",
					"short-id":   "0123",
				},
			},
		}},
	}
	if err := s.Save("main", snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("main")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, _ := json.Marshal(snap.Nodes)
	have, _ := json.Marshal(got.Nodes)
	if string(want) != string(have) {
		t.Errorf("values changed on the way through the store:\n got %s\nwant %s", have, want)
	}
}

// A value that Ktav cannot hold without change keeps the snapshot in the JSON
// form instead of damaging it.
func TestStoreFallsBackToJSONWhenKtavWouldChangeAValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subscriptions")
	s := NewStore(dir)
	snap := Snapshot{
		FetchedAt: time.Now().UTC().Truncate(time.Second),
		Nodes: []Node{{
			ID: "dddddddddddd", Name: "n", Type: "ss", Server: "example.invalid", Port: 1, Country: "DE",
			Raw: map[string]any{"type": "ss", "private-key": "-----BEGIN KEY-----\nline one\nline two\n-----END KEY-----\n", "note": "  padded  "},
		}},
	}
	if err := s.Save("main", snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("main")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, _ := json.Marshal(snap.Nodes)
	have, _ := json.Marshal(got.Nodes)
	if string(want) != string(have) {
		t.Fatalf("a snapshot was damaged:\n got %s\nwant %s", have, want)
	}
	// Whichever form was used, there is exactly one file for the name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("files in the store: %v", entries)
	}
}

func TestStoreReadsAndMigratesTheLegacyJSONFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subscriptions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	snap := testSnap(time.Now().UTC().Truncate(time.Second))
	legacy := `{"fetched_at": "` + snap.FetchedAt.Format(time.RFC3339) + `", "skipped": 3, "nodes": [` +
		`{"id": "aaaaaaaaaaaa", "name": "🇩🇪 Frankfurt 1", "type": "ss", "server": "example.invalid", "port": 8388, "country": "DE",` +
		` "raw": {"type": "ss", "server": "example.invalid", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"}},` +
		`{"id": "bbbbbbbbbbbb", "name": "Tokyo 01", "type": "vmess", "server": "example.invalid", "port": 443, "country": "JP",` +
		` "raw": {"type": "vmess", "uuid": "00000000-0000-0000-0000-000000000000"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)

	got, err := s.Load("main")
	if err != nil {
		t.Fatalf("Load of a legacy file: %v", err)
	}
	if !reflect.DeepEqual(got.Nodes, snap.Nodes) {
		t.Errorf("legacy nodes:\n got %+v\nwant %+v", got.Nodes, snap.Nodes)
	}
	if names, _ := s.List(); strings.Join(names, ",") != "main" {
		t.Errorf("List with a legacy file = %v", names)
	}

	// The next save replaces it by a Ktav file.
	if err := s.Save("main", snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the legacy file is still there after a save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.ktav")); err != nil {
		t.Errorf("no Ktav file after a save: %v", err)
	}
	if names, _ := s.List(); strings.Join(names, ",") != "main" {
		t.Errorf("List after migration = %v", names)
	}
}

func TestStoreListWithBothFormsCountsANameOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subscriptions")
	s := NewStore(dir)
	if err := s.Save("a", testSnap(time.Now())); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names, err := s.List()
	if err != nil || strings.Join(names, ",") != "a,b" {
		t.Errorf("List = %v, %v; want [a b]", names, err)
	}
	// The Ktav file wins when both exist.
	got, err := s.Load("a")
	if err != nil || len(got.Nodes) != 2 {
		t.Errorf("Load with both forms = %+v, %v; want the Ktav snapshot", got, err)
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
	// A legacy file next to it goes too.
	if err := os.WriteFile(filepath.Join(s.Dir(), "main.json"), []byte("{}"), 0o600); err != nil {
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
	s := NewStore(dir)

	// A Ktav file that does not parse; its text, which holds credentials, is
	// not part of the error.
	secret := "very-secret-password"
	broken := "nodes: [\n    {\n        password: " + secret + "\n"
	if err := os.WriteFile(filepath.Join(dir, "main.ktav"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("main")
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Load corrupt Ktav = %v, want corrupt error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error quotes the file: %v", err)
	}

	// A legacy JSON file that does not parse.
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("old"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("Load corrupt JSON = %v, want corrupt error", err)
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
