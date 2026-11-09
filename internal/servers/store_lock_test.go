package servers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLockContentionAndRelease(t *testing.T) {
	dir := t.TempDir()
	first, second := NewStore(dir), NewStore(dir)
	unlock, err := first.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if gotUnlock, acquired, err := second.tryLock(); err != nil || acquired || gotUnlock != nil {
		t.Fatalf("tryLock while held = (%t, %t, %v), want (false, false, nil)", gotUnlock != nil, acquired, err)
	}
	unlock()
	unlock()

	gotUnlock, acquired, err := second.tryLock()
	if err != nil || !acquired || gotUnlock == nil {
		t.Fatalf("tryLock after release = (%t, %t, %v), want acquired", gotUnlock != nil, acquired, err)
	}
	gotUnlock()
	if _, err := os.Stat(first.path + storeLockSuffix); err != nil {
		t.Fatalf("sidecar was not retained: %v", err)
	}
}

func TestStoreLockIndependentDirectories(t *testing.T) {
	first, second := NewStore(t.TempDir()), NewStore(t.TempDir())
	unlock, err := first.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	secondUnlock, acquired, err := second.tryLock()
	if err != nil || !acquired || secondUnlock == nil {
		t.Fatalf("tryLock in separate directory = (%t, %t, %v), want acquired", secondUnlock != nil, acquired, err)
	}
	secondUnlock()
}

func TestStoreLockFailureIsRedacted(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(parent)
	_, err := store.lock()
	if err == nil || err.Error() != "cannot lock server secrets" || strings.Contains(err.Error(), parent) {
		t.Fatalf("lock error = %v, want redacted failure", err)
	}
	_, acquired, err := store.tryLock()
	if acquired || err == nil || err.Error() != "cannot lock server secrets" || strings.Contains(err.Error(), parent) {
		t.Fatalf("tryLock error = (%t, %v), want redacted failure", acquired, err)
	}
}
