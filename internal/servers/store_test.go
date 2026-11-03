package servers

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
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

func TestStoreTrackedCleanupPreservesConcurrentChanges(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tracked", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	e := entries[spec.Name]
	e.Material["writer"] = "added"
	e.Users["alice"]["external"] = "added"
	e.Users["alice"]["password"] = "rotated"
	e.Users["bob"] = map[string]string{"password": "other-user"}
	entries[spec.Name] = e
	entries["other"] = storeEntry{Material: map[string]string{"key": "other"}, Users: map[string]map[string]string{}}
	if err := s.write(entries); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	entries, err = s.read()
	if err != nil {
		t.Fatal(err)
	}
	got := entries[spec.Name]
	if got.Material["writer"] != "added" || got.Users["alice"]["external"] != "added" || got.Users["alice"]["password"] != "rotated" || got.Users["bob"]["password"] != "other-user" {
		t.Fatalf("concurrent values lost: %#v", got)
	}
	if len(got.Material) != 1 || got.Material["writer"] != "added" {
		t.Fatalf("created material was not cleaned: %#v", got.Material)
	}
	if _, ok := entries["other"]; !ok {
		t.Fatal("concurrent entry was removed")
	}
}

func TestStoreTrackedCleanupRemovesCleanNewEntry(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tracked", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 0 {
		t.Fatalf("names after cleanup = %v", got)
	}
}

func TestStoreTrackedReceiptLeavesExistingFieldsUnchanged(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("servers: {\n  tracked: {\n    material: { key: existing }\n    users: { alice: { password: existing-pass, token: existing-token } }\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "tracked", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Material) != 0 || len(receipt.Users["alice"]) != 0 || receipt.CreatedEntry || len(receipt.CreatedUsers) != 0 {
		t.Fatalf("receipt includes pre-existing fields: %#v", receipt)
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("no-op cleanup rewrote the store")
	}
}

func TestStoreCleanupCreatedNoopReceiptDoesNotWrite(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("servers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(EnsureSecretsReceipt{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("no-op receipt rewrote the store")
	}
}

func TestStoreEnsureValidatedRejectsAmbiguousSecretsWithoutWriting(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	peer := Spec{Name: "peer", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Accounts: []string{"alice"}, Params: map[string]string{"sni": "same.example"}}
	peerEntry := storeEntry{Material: map[string]string{}, Users: map[string]map[string]string{"alice": {"password": "peer-fake"}}}
	if err := s.write(map[string]storeEntry{peer.Name: peerEntry}); err != nil {
		t.Fatal(err)
	}
	target := Spec{Name: "target", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Accounts: []string{"alice"}, Params: map[string]string{"sni": "SAME.EXAMPLE"}}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := s.EnsureSecretsValidated(target, []Spec{peer})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Message != "shared server TLS match keys overlap" || ce.Exit != cli.ExitConflict {
		t.Fatalf("validation error = %v, want SERVER_PORT_AMBIGUOUS cli.Error", err)
	}
	if receipt.CreatedEntry || len(receipt.Material) != 0 || len(receipt.Users) != 0 {
		t.Fatalf("failed validation returned a receipt: %#v", receipt)
	}
	after, readErr := os.ReadFile(s.Path())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("validation failure wrote generated secrets")
	}
}

func TestStoreEnsureValidatedStorageErrorsAreNotValidationErrors(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := Spec{Name: "target", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Accounts: []string{"alice"}, Params: map[string]string{"sni": "target.example"}}
	_, _, err := s.EnsureSecretsValidated(target, nil)
	if err == nil {
		t.Fatal("corrupt store was accepted")
	}
	if _, ok := err.(*cli.Error); ok {
		t.Fatalf("storage error was mapped to a validation error: %v", err)
	}
}

func TestStoreTrackedCleanupRestoresRepairedEmptyFields(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("servers: {\n  tracked: {\n    material: { key: '' }\n    users: { alice: { password: '' } }\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "tracked", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Material) != 1 || !receipt.Material[0].HadPrevious || receipt.Material[0].Previous != "" || len(receipt.Users["alice"]) != 2 {
		t.Fatalf("receipt did not capture empty prior values: %s", receipt)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	e := entries[spec.Name]
	e.Material["external"] = "concurrent"
	e.Users["alice"]["password"] = "rotated"
	e.Users["alice"]["external"] = "concurrent"
	entries[spec.Name] = e
	if err := s.write(entries); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	entries, err = s.read()
	if err != nil {
		t.Fatal(err)
	}
	got := entries[spec.Name]
	if value, ok := got.Material["key"]; !ok || value != "" {
		t.Fatalf("empty material field not restored: %#v", got.Material)
	}
	if got.Users["alice"]["password"] != "rotated" || got.Users["alice"]["external"] != "concurrent" || got.Material["external"] != "concurrent" {
		t.Fatalf("concurrent updates were not preserved: %#v", got)
	}
	if _, ok := got.Users["alice"]["token"]; ok {
		t.Fatal("new generated token was not removed")
	}
}

func TestStoreCleanupCreatedReturnsReadError(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupCreated(EnsureSecretsReceipt{Server: "tracked", CreatedEntry: true}); err == nil {
		t.Fatal("cleanup accepted corrupt store")
	}
}
