package servers

import (
	"os"
	"path/filepath"
	"testing"
)

func assertStoreLockHeld(t *testing.T, contender *Store) {
	t.Helper()
	unlock, acquired, err := contender.tryLock()
	if err != nil {
		t.Errorf("tryLock during mutation = %v", err)
		return
	}
	if acquired {
		unlock()
		t.Error("mutation did not hold the interprocess lock during generation")
	}
}

func TestStoreEnsureAndRotateHoldLockThroughGeneration(t *testing.T) {
	p := registerStoreTestProtocol(t)
	dir := t.TempDir()
	store, contender := NewStore(dir), NewStore(dir)
	spec := Spec{Name: "locked", Protocol: "store-test", Port: 2345, Accounts: []string{"alice"}}
	materialChecked, secretChecked := false, false
	p.onMaterial = func() {
		materialChecked = true
		assertStoreLockHeld(t, contender)
	}
	p.onSecret = func() {
		secretChecked = true
		assertStoreLockHeld(t, contender)
	}
	if _, err := store.EnsureSecrets(spec); err != nil {
		t.Fatal(err)
	}
	if !materialChecked || !secretChecked {
		t.Fatalf("ensure generation callbacks called material=%t secret=%t", materialChecked, secretChecked)
	}

	secretChecked = false
	if _, err := store.Rotate(spec, "alice"); err != nil {
		t.Fatal(err)
	}
	if !secretChecked {
		t.Fatal("rotate did not call the secret-generation callback")
	}
}

func TestStoreSeparateHandlesRotateDifferentUsersWithoutClobber(t *testing.T) {
	registerStoreTestProtocol(t)
	dir := t.TempDir()
	firstStore, secondStore := NewStore(dir), NewStore(dir)
	spec := Spec{Name: "rotated", Protocol: "store-test", Port: 2345, Accounts: []string{"alice", "bob"}}
	initial, err := firstStore.EnsureSecrets(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstStore.Rotate(spec, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.Rotate(spec, "bob"); err != nil {
		t.Fatal(err)
	}
	final, err := firstStore.Apply(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range spec.Accounts {
		if final.Users[login]["password"] == initial.Users[login]["password"] {
			t.Errorf("%s secret was not rotated", login)
		}
	}
}

func TestStoreCleanupRemoveAndPruneFailBeforeChangingFileWithoutLock(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Store) error
	}{
		{name: "cleanup", run: func(s *Store) error {
			return s.CleanupCreated(EnsureSecretsReceipt{
				Server:   "kept",
				Material: []CreatedSecretField{{Key: "key", Value: "stable"}},
			})
		}},
		{name: "remove", run: func(s *Store) error { return s.Remove("kept") }},
		{name: "prune", run: func(s *Store) error { return s.Prune(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			if err := s.write(map[string]storeEntry{
				"kept":  {Material: map[string]string{"key": "stable"}, Users: map[string]map[string]string{}},
				"other": {Material: map[string]string{"key": "also-stable"}, Users: map[string]map[string]string{}},
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(s.Path()+storeLockSuffix, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(s); err == nil {
				t.Fatal("mutation succeeded without acquiring its lock")
			}
			after, err := os.ReadFile(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("mutation changed the secrets file after lock acquisition failed")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(s.Path()), filepath.Base(s.Path())+storeLockSuffix)); err != nil {
				t.Fatalf("lock failure seam disappeared: %v", err)
			}
		})
	}
}
