package servers

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var storeProtocol = &storeTestProtocol{}

func registerStoreTestProtocol(t *testing.T) *storeTestProtocol {
	t.Helper()
	registryMu.Lock()
	registry["store-test"] = storeProtocol
	registryMu.Unlock()
	storeProtocol.materialCalls, storeProtocol.secretCalls = 0, 0
	t.Cleanup(func() {
		registryMu.Lock()
		delete(registry, "store-test")
		registryMu.Unlock()
	})
	return storeProtocol
}

func TestStoreMissingAndPersistenceRepair(t *testing.T) {
	p := registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 0 {
		t.Fatalf("names = %v", got)
	}
	if err := os.WriteFile(s.Path(), []byte("servers: {\n  test: {\n    material: { key: old }\n    users: { alice: { password: old-pass } }\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "test", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	got, err := s.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Material["key"] != "old" || got.Users["alice"]["password"] != "old-pass" || got.Users["alice"]["token"] != "generated-token" {
		t.Fatal("existing secrets not preserved")
	}
	if p.materialCalls != 1 || p.secretCalls != 1 {
		t.Fatalf("generation calls = %d/%d", p.materialCalls, p.secretCalls)
	}
	loaded, err := s.Apply(spec)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Users["alice"]["password"] != "old-pass" {
		t.Fatal("not persisted")
	}
}

type storeTestProtocol struct{ materialCalls, secretCalls int }

func (*storeTestProtocol) Name() string                     { return "store-test" }
func (*storeTestProtocol) SingleUser() bool                 { return false }
func (*storeTestProtocol) Validate(map[string]string) error { return nil }
func (p *storeTestProtocol) NewMaterial(map[string]string) (map[string]string, error) {
	p.materialCalls++
	return map[string]string{"key": "generated"}, nil
}
func (p *storeTestProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	p.secretCalls++
	return map[string]string{"password": "generated-" + strconv.Itoa(p.secretCalls), "token": "generated-token"}, nil
}
func (*storeTestProtocol) Start(Spec, Env) (io.Closer, error)        { return nil, nil }
func (*storeTestProtocol) Link(Spec, string, string) (string, error) { return "", nil }

func TestStoreRepairEmptyAndNilFields(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("servers: {\n  test: {\n    material: { key: '', extra: stable }\n    users: { alice: null }\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "test", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	got, err := s.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Material["key"] == "" || got.Material["extra"] != "stable" || got.Users["alice"]["password"] == "" {
		t.Fatal("empty or nil fields were not repaired")
	}
}

func TestStoreCorruptionRedactionAndAtomicReplacement(t *testing.T) {
	s := NewStore(t.TempDir())
	secret := "sensitive-marker"
	if err := os.WriteFile(s.Path(), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.Load()
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), s.Path()) {
		t.Fatalf("unsafe error: %v", err)
	}
	if s.String() != "<redacted>" || s.GoString() != "<redacted>" {
		t.Fatal("store formatting is not redacted")
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	if err := s.write(map[string]storeEntry{}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(s.Path()) {
		t.Fatalf("temporary file leaked: %v", entries)
	}
}

func TestStoreRotateRemovePrune(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	first := Spec{Name: "one", Protocol: "store-test", Port: 2345, Accounts: []string{"alice", "bob"}}
	if _, err := s.EnsureSecrets(first); err != nil {
		t.Fatal(err)
	}
	before, err := s.Apply(first)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.Rotate(first, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Users["alice"]["password"] == before.Users["alice"]["password"] {
		t.Fatal("account secret did not rotate")
	}
	if rotated.Users["bob"]["password"] != before.Users["bob"]["password"] {
		t.Fatal("other account secret changed")
	}
	all, err := s.Rotate(first, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Users["bob"]["password"] == rotated.Users["bob"]["password"] {
		t.Fatal("empty login did not rotate all configured users")
	}
	second := first
	second.Name = "two"
	if _, err := s.EnsureSecrets(second); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune([]Spec{first}); err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 1 || got[0] != "one" {
		t.Fatalf("pruned names = %v", got)
	}
	if err := s.Remove("one"); err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 0 {
		t.Fatalf("remaining names = %v", got)
	}
}

func TestStoreSeparateHandlesDoNotClobber(t *testing.T) {
	registerStoreTestProtocol(t)
	dir := t.TempDir()
	a, b := NewStore(dir), NewStore(dir)
	one := Spec{Name: "one", Protocol: "store-test", Port: 3456, Accounts: []string{"alice"}}
	two := Spec{Name: "two", Protocol: "store-test", Port: 3457, Accounts: []string{"bob"}}
	if _, err := a.EnsureSecrets(one); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureSecrets(two); err != nil {
		t.Fatal(err)
	}
	if got := a.Names(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("stale handle lost update: %v", got)
	}
}
