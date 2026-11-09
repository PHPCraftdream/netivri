package servers

import (
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func registerStoreTestProtocol(t *testing.T) *storeTestProtocol {
	t.Helper()
	p := &storeTestProtocol{}
	registryMu.Lock()
	previous, hadPrevious := registry[p.Name()]
	registry[p.Name()] = p
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		if hadPrevious {
			registry[p.Name()] = previous
		} else {
			delete(registry, p.Name())
		}
		registryMu.Unlock()
	})
	return p
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

type storeTestProtocol struct {
	materialCalls, secretCalls int
	onMaterial                 func()
	onSecret                   func()
}

func (*storeTestProtocol) Name() string                     { return "store-test" }
func (*storeTestProtocol) SingleUser() bool                 { return false }
func (*storeTestProtocol) Validate(map[string]string) error { return nil }
func (p *storeTestProtocol) NewMaterial(map[string]string) (map[string]string, error) {
	p.materialCalls++
	if p.onMaterial != nil {
		p.onMaterial()
	}
	return map[string]string{"key": "generated"}, nil
}
func (p *storeTestProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	p.secretCalls++
	if p.onSecret != nil {
		p.onSecret()
	}
	return map[string]string{"password": "generated-" + strconv.Itoa(p.secretCalls), "token": "generated-token"}, nil
}
func (*storeTestProtocol) Start(Spec, Env) (io.Closer, error)        { return nil, nil }
func (*storeTestProtocol) Link(Spec, string, string) (string, error) { return "", nil }

func TestStoreRepairEmptyAndNilFields(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := s.write(map[string]storeEntry{
		"test": {
			Material: map[string]string{"key": "", "extra": "stable"},
			Users:    map[string]map[string]string{"alice": nil},
		},
	}); err != nil {
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

func TestStoreReadKTAVQuotedScalarsRemainLiteral(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("servers: {\n  quoted: {\n    material: {\n      single: ''\n      double: \"\"\n      empty:\n    }\n    users: {}\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	material := entries["quoted"].Material
	if got := len(material["single"]); got != 2 {
		t.Errorf("single-quoted scalar length=%d, want 2", got)
	}
	if got := len(material["double"]); got != 2 {
		t.Errorf("double-quoted scalar length=%d, want 2", got)
	}
	if value, present := material["empty"]; !present || len(value) != 0 {
		t.Errorf("bare empty scalar presence=%t length=%d, want present empty scalar", present, len(value))
	}
}

func TestStoreConfiguredTLSPathsOverrideStoredMaterial(t *testing.T) {
	s := NewStore(t.TempDir())
	certA, err := NewTLSMaterial(nil, "store-a.example.test")
	if err != nil {
		t.Fatal("first certificate generation failed")
	}
	certB, err := NewTLSMaterial(nil, "store-b.example.test")
	if err != nil {
		t.Fatal("second certificate generation failed")
	}
	dir := t.TempDir()
	certPathA, keyPathA := filepath.Join(dir, "a-cert.pem"), filepath.Join(dir, "a-key.pem")
	certPathB, keyPathB := filepath.Join(dir, "b-cert.pem"), filepath.Join(dir, "b-key.pem")
	writeSecretFixture(t, certPathA, certA[tlsCertKey])
	writeSecretFixture(t, keyPathA, certA[tlsKeyKey])
	writeSecretFixture(t, certPathB, certB[tlsCertKey])
	writeSecretFixture(t, keyPathB, certB[tlsKeyKey])

	specA := Spec{Name: "tls-paths", Protocol: "trojan", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"cert_file": certPathA, "key_file": keyPathA}}
	before, err := s.EnsureSecrets(specA)
	if err != nil {
		t.Fatal(err)
	}
	specB := specA
	specB.Params = map[string]string{"cert_file": certPathB, "key_file": keyPathB}
	ensured, receipt, err := s.EnsureSecretsTracked(specB)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	persisted := stored[specA.Name].Material
	if persisted[tlsCertFileKey] != certPathB || persisted[tlsPrivateKeyFileKey] != keyPathB || persisted[tlsFingerprintKey] != certB[tlsFingerprintKey] {
		t.Fatal("EnsureSecrets did not persist the newly configured certificate pair")
	}
	cert, key := TLSFiles(persisted)
	if cert != certPathB || key != keyPathB {
		t.Fatal("TLSFiles did not select the newly persisted paths")
	}
	if ensured.Material[tlsCertFileKey] != certPathB || ensured.Material[tlsPrivateKeyFileKey] != keyPathB || ensured.Material[tlsFingerprintKey] != certB[tlsFingerprintKey] {
		t.Fatal("EnsureSecrets did not return the newly configured certificate pair")
	}
	if ensured.Users["alice"]["password"] != before.Users["alice"]["password"] {
		t.Fatal("TLS path update changed persisted user secrets")
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	stored, err = s.read()
	if err != nil {
		t.Fatal(err)
	}
	persisted = stored[specA.Name].Material
	if persisted[tlsCertFileKey] != certPathA || persisted[tlsPrivateKeyFileKey] != keyPathA || persisted[tlsFingerprintKey] != certA[tlsFingerprintKey] {
		t.Fatal("tracked cleanup did not restore the previous certificate paths")
	}
	cert, key = TLSFiles(persisted)
	if cert != certPathA || key != keyPathA || stored[specA.Name].Users["alice"]["password"] != before.Users["alice"]["password"] {
		t.Fatal("tracked cleanup changed the prior pair or user secrets")
	}
}

func TestStoreConfiguredTLSFilesReplaceGeneratedPEMAndRollback(t *testing.T) {
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tls-pem-transition", Protocol: "trojan", Port: 443, Accounts: []string{"alice"}}
	before, err := s.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	beforeCert, beforeKey := TLSFiles(before.Material)
	if beforeCert == "" || beforeKey == "" || before.Material[tlsCertKey] == "" || before.Material[tlsKeyKey] == "" {
		t.Fatal("initial generated PEM pair is incomplete")
	}

	configured, err := NewTLSMaterial(nil, "configured.example.test")
	if err != nil {
		t.Fatal("configured certificate generation failed")
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeSecretFixture(t, certPath, configured[tlsCertKey])
	writeSecretFixture(t, keyPath, configured[tlsKeyKey])
	spec.Params = map[string]string{"cert_file": certPath, "key_file": keyPath}
	ensured, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	material := stored[spec.Name].Material
	if _, ok := material[tlsCertKey]; ok {
		t.Fatal("configured file pair left the generated certificate PEM persisted")
	}
	if _, ok := material[tlsKeyKey]; ok {
		t.Fatal("configured file pair left the generated private-key PEM persisted")
	}
	cert, key := TLSFiles(material)
	if cert != certPath || key != keyPath || material[tlsFingerprintKey] != configured[tlsFingerprintKey] {
		t.Fatal("configured file pair was not selected and persisted")
	}
	if ensured.Users["alice"]["password"] != before.Users["alice"]["password"] {
		t.Fatal("TLS file transition changed persisted user secrets")
	}
	removedPEM := map[string]bool{}
	for _, field := range receipt.Material {
		if field.Removed {
			removedPEM[field.Key] = field.Previous == before.Material[field.Key]
		}
	}
	if !removedPEM[tlsCertKey] || !removedPEM[tlsKeyKey] {
		t.Fatal("receipt did not track removal of both generated PEM values")
	}

	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	stored, err = s.read()
	if err != nil {
		t.Fatal(err)
	}
	material = stored[spec.Name].Material
	if material[tlsCertKey] != before.Material[tlsCertKey] || material[tlsKeyKey] != before.Material[tlsKeyKey] || material[tlsFingerprintKey] != before.Material[tlsFingerprintKey] {
		t.Fatal("tracked cleanup did not restore the generated PEM pair")
	}
	if _, ok := material[tlsCertFileKey]; ok {
		t.Fatal("tracked cleanup left the configured certificate path")
	}
	if _, ok := material[tlsPrivateKeyFileKey]; ok {
		t.Fatal("tracked cleanup left the configured private-key path")
	}
	cert, key = TLSFiles(material)
	if cert != beforeCert || key != beforeKey || stored[spec.Name].Users["alice"]["password"] != before.Users["alice"]["password"] {
		t.Fatal("tracked cleanup did not restore the initial listener pair and user secrets")
	}
}

func TestStoreTrackedCleanupPreservesRotatedConfiguredTLSGroup(t *testing.T) {
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tls-rotated-config", Protocol: "trojan", Port: 443, Accounts: []string{"alice"}}
	initial, err := s.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Material[tlsCertKey] == "" || initial.Material[tlsKeyKey] == "" {
		t.Fatal("initial generated PEM pair is incomplete")
	}

	configuredB, err := NewTLSMaterial(nil, "configured-b.example.test")
	if err != nil {
		t.Fatal("configured B certificate generation failed")
	}
	configuredC, err := NewTLSMaterial(nil, "configured-c.example.test")
	if err != nil {
		t.Fatal("configured C certificate generation failed")
	}
	dir := t.TempDir()
	certB, keyB := filepath.Join(dir, "b-cert.pem"), filepath.Join(dir, "b-key.pem")
	certC, keyC := filepath.Join(dir, "c-cert.pem"), filepath.Join(dir, "c-key.pem")
	writeSecretFixture(t, certB, configuredB[tlsCertKey])
	writeSecretFixture(t, keyB, configuredB[tlsKeyKey])
	writeSecretFixture(t, certC, configuredC[tlsCertKey])
	writeSecretFixture(t, keyC, configuredC[tlsKeyKey])

	specB := spec
	specB.Params = map[string]string{"cert_file": certB, "key_file": keyB}
	_, receipt, err := s.EnsureSecretsTracked(specB)
	if err != nil {
		t.Fatal(err)
	}
	specC := spec
	specC.Params = map[string]string{"cert_file": certC, "key_file": keyC}
	rotated, err := s.Rotate(specC, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Material[tlsCertFileKey] != certC || rotated.Material[tlsPrivateKeyFileKey] != keyC || rotated.Material[tlsFingerprintKey] != configuredC[tlsFingerprintKey] {
		t.Fatal("whole rotation did not select configured C TLS material")
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	material := entries[spec.Name].Material
	if material[tlsCertFileKey] != certC || material[tlsPrivateKeyFileKey] != keyC || material[tlsFingerprintKey] != configuredC[tlsFingerprintKey] {
		t.Fatal("tracked cleanup changed the newer configured TLS group")
	}
	if _, ok := material[tlsCertKey]; ok {
		t.Fatal("tracked cleanup restored the superseded generated certificate PEM")
	}
	if _, ok := material[tlsKeyKey]; ok {
		t.Fatal("tracked cleanup restored the superseded generated private-key PEM")
	}
	cert, key := TLSFiles(material)
	if cert != certC || key != keyC || TLSFingerprint(material) != material[tlsFingerprintKey] {
		t.Fatal("stored TLS pair and fingerprint are inconsistent after cleanup")
	}
	cert, key = TLSFiles(rotated.Material)
	if cert != certC || key != keyC || TLSFingerprint(rotated.Material) != configuredC[tlsFingerprintKey] {
		t.Fatal("rotation result did not retain configured C TLS material")
	}
}

func TestStoreTrackedCleanupChecksUnchangedTLSFields(t *testing.T) {
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tls-unchanged-fingerprint", Protocol: "trojan", Port: 443, Accounts: []string{"alice"}}
	initial, err := s.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	configuredC, err := NewTLSMaterial(nil, "configured-c.example.test")
	if err != nil {
		t.Fatal("configured C certificate generation failed")
	}
	certPath, keyPath := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	writeSecretFixture(t, certPath, initial.Material[tlsCertKey])
	writeSecretFixture(t, keyPath, initial.Material[tlsKeyKey])
	spec.Params = map[string]string{"cert_file": certPath, "key_file": keyPath}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range receipt.Material {
		if field.Key == tlsFingerprintKey {
			t.Fatal("unchanged TLS fingerprint unexpectedly appeared in the receipt")
		}
	}

	// Keep the configured paths unchanged while their contents move from B to C.
	writeSecretFixture(t, certPath, configuredC[tlsCertKey])
	writeSecretFixture(t, keyPath, configuredC[tlsKeyKey])
	rotated, err := s.Rotate(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Material[tlsCertFileKey] != certPath || rotated.Material[tlsPrivateKeyFileKey] != keyPath || rotated.Material[tlsFingerprintKey] != configuredC[tlsFingerprintKey] {
		t.Fatal("whole rotation did not update the TLS fingerprint for the unchanged paths")
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	material := entries[spec.Name].Material
	if material[tlsCertFileKey] != certPath || material[tlsPrivateKeyFileKey] != keyPath || material[tlsFingerprintKey] != configuredC[tlsFingerprintKey] {
		t.Fatal("tracked cleanup changed TLS fields after an unchanged field changed")
	}
	if _, ok := material[tlsCertKey]; ok {
		t.Fatal("tracked cleanup restored the superseded generated certificate PEM")
	}
	if _, ok := material[tlsKeyKey]; ok {
		t.Fatal("tracked cleanup restored the superseded generated private-key PEM")
	}
	cert, key := TLSFiles(material)
	if cert != certPath || key != keyPath || TLSFingerprint(material) != material[tlsFingerprintKey] {
		t.Fatal("stored TLS pair and fingerprint are inconsistent after cleanup")
	}
}

func TestStoreTrackedCleanupPreservesRotatedPartialTLSRepair(t *testing.T) {
	stale, err := NewTLSMaterial(nil, "partial-stale.example.test")
	if err != nil {
		t.Fatal("stale certificate generation failed")
	}
	s := NewStore(t.TempDir())
	spec := Spec{Name: "tls-partial-rotated", Protocol: "hysteria2", Port: 443, Accounts: []string{"alice"}}
	if err := s.write(map[string]storeEntry{
		spec.Name: {
			Material: map[string]string{tlsCertKey: stale[tlsCertKey], tlsFingerprintKey: stale[tlsFingerprintKey]},
			Users:    map[string]map[string]string{"alice": {"password": "stable-password"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.Rotate(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Material[tlsCertKey] == stale[tlsCertKey] || rotated.Material[tlsKeyKey] == "" {
		t.Fatal("whole rotation did not replace the partial TLS repair")
	}
	if err := s.CleanupCreated(receipt); err != nil {
		t.Fatal(err)
	}
	entries, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	material := entries[spec.Name].Material
	if material[tlsCertKey] != rotated.Material[tlsCertKey] || material[tlsKeyKey] != rotated.Material[tlsKeyKey] || material[tlsFingerprintKey] != rotated.Material[tlsFingerprintKey] {
		t.Fatal("tracked cleanup did not preserve the newer complete TLS pair")
	}
	cert, key := TLSFiles(material)
	if cert != rotated.Material[tlsCertKey] || key != rotated.Material[tlsKeyKey] || TLSFingerprint(material) != material[tlsFingerprintKey] {
		t.Fatal("stored TLS pair and fingerprint are inconsistent after cleanup")
	}
}

func TestStoreRepairsPartialGeneratedTLSPair(t *testing.T) {
	stale, err := NewTLSMaterial(nil, "stale.example.test")
	if err != nil {
		t.Fatal("stale certificate generation failed")
	}
	for _, tc := range []struct {
		name       string
		keyPresent bool
	}{
		{name: "missing_key_pem"},
		{name: "empty_key_pem", keyPresent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			material := map[string]string{tlsCertKey: stale[tlsCertKey], tlsFingerprintKey: stale[tlsFingerprintKey], "obfs_password": "stable-obfs", "unrelated": "stable"}
			if tc.keyPresent {
				material[tlsKeyKey] = ""
			}
			spec := Spec{Name: "repair-tls", Protocol: "hysteria2", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"obfs": "salamander"}}
			if err := s.write(map[string]storeEntry{
				spec.Name: {
					Material: material,
					Users:    map[string]map[string]string{"alice": {"password": "stable-password"}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			ensured, receipt, err := s.EnsureSecretsTracked(spec)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair([]byte(ensured.Material[tlsCertKey]), []byte(ensured.Material[tlsKeyKey]))
			if err != nil {
				t.Fatal("repaired generated pair is invalid")
			}
			leaf, err := parsePairLeaf(pair)
			if err != nil || TLSFingerprint(ensured.Material) != ensured.Material[tlsFingerprintKey] || ensured.Material[tlsFingerprintKey] == stale[tlsFingerprintKey] {
				t.Fatal("repaired pair fingerprint does not match its certificate")
			}
			if ensured.Material["obfs_password"] != "stable-obfs" || ensured.Material["unrelated"] != "stable" || ensured.Users["alice"]["password"] != "stable-password" {
				t.Fatal("repair changed existing non-TLS material or user secrets")
			}
			if leaf.Subject.CommonName == "stale.example.test" {
				t.Fatal("stale certificate remained in repaired pair")
			}
			if len(receipt.Material) != 3 {
				t.Fatalf("material receipt fields = %d, want certificate, key, and fingerprint", len(receipt.Material))
			}
			if err := s.CleanupCreated(receipt); err != nil {
				t.Fatal(err)
			}
			restored, err := s.read()
			if err != nil {
				t.Fatal(err)
			}
			material = restored[spec.Name].Material
			if material[tlsCertKey] != stale[tlsCertKey] || material[tlsFingerprintKey] != stale[tlsFingerprintKey] {
				t.Fatal("cleanup did not restore replaced partial TLS material")
			}
			if value, ok := material[tlsKeyKey]; ok != tc.keyPresent || (tc.keyPresent && value != "") {
				t.Fatal("cleanup did not restore the original missing or empty key_pem state")
			}
			if material["obfs_password"] != "stable-obfs" || material["unrelated"] != "stable" {
				t.Fatal("cleanup changed non-TLS material")
			}
		})
	}
}

func TestStoreTrackedCleanupRestoresRepairedEmptyFields(t *testing.T) {
	registerStoreTestProtocol(t)
	s := NewStore(t.TempDir())
	if err := s.write(map[string]storeEntry{
		"tracked": {
			Material: map[string]string{"key": ""},
			Users:    map[string]map[string]string{"alice": {"password": ""}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	before := entriesBefore["tracked"]
	key, keyPresent := before.Material["key"]
	password, passwordPresent := before.Users["alice"]["password"]
	if !keyPresent || len(key) != 0 || !passwordPresent || len(password) != 0 {
		t.Fatalf("empty fixture was not loaded: key_present=%t key_len=%d password_present=%t password_len=%d", keyPresent, len(key), passwordPresent, len(password))
	}
	spec := Spec{Name: "tracked", Protocol: "store-test", Port: 1234, Accounts: []string{"alice"}}
	_, receipt, err := s.EnsureSecretsTracked(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Material) != 1 {
		t.Errorf("created material field count = %d, want 1", len(receipt.Material))
	} else {
		if !receipt.Material[0].HadPrevious {
			t.Errorf("created material field had previous=%t, want true", receipt.Material[0].HadPrevious)
		}
		if len(receipt.Material[0].Previous) != 0 {
			t.Errorf("created material previous value length=%d, want 0", len(receipt.Material[0].Previous))
		}
	}
	if got := len(receipt.Users["alice"]); got != 2 {
		t.Errorf("created user field count = %d, want 2", got)
	}
	seenUserFields := map[string]bool{}
	for _, field := range receipt.Users["alice"] {
		if field.Key != "password" && field.Key != "token" {
			t.Errorf("unexpected created user field name %q", field.Key)
		}
		seenUserFields[field.Key] = true
		wantHadPrevious := field.Key == "password"
		if field.HadPrevious != wantHadPrevious {
			t.Errorf("user field %s had previous=%t, want %t", field.Key, field.HadPrevious, wantHadPrevious)
		}
		if len(field.Previous) != 0 {
			t.Errorf("user field %s previous value length=%d, want 0", field.Key, len(field.Previous))
		}
	}
	if len(seenUserFields) != 2 || !seenUserFields["password"] || !seenUserFields["token"] {
		t.Errorf("created user field names = %v, want password and token", seenUserFields)
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
