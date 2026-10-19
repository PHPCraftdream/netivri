package config

import (
	"errors"
	"os"
	"testing"
)

func TestRenameReplaceRetriesTransientErrors(t *testing.T) {
	busy := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errorAccessDenied}
	calls := 0
	old := renameFn
	renameFn = func(string, string) error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	}
	t.Cleanup(func() { renameFn = old })

	if err := renameReplace("a", "b"); err != nil {
		t.Fatalf("renameReplace: %v", err)
	}
	if calls != 3 {
		t.Fatalf("rename attempts = %d, want 3", calls)
	}
}

func TestRenameReplaceGivesUpOnPersistentBusy(t *testing.T) {
	busy := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errorSharingViolation}
	calls := 0
	old := renameFn
	renameFn = func(string, string) error { calls++; return busy }
	t.Cleanup(func() { renameFn = old })

	if err := renameReplace("a", "b"); !errors.Is(err, errorSharingViolation) {
		t.Fatalf("err = %v, want the sharing violation", err)
	}
	if calls != renameAttempts {
		t.Fatalf("rename attempts = %d, want %d", calls, renameAttempts)
	}
}

func TestRenameReplaceDoesNotRetryOtherErrors(t *testing.T) {
	other := errors.New("disk on fire")
	calls := 0
	old := renameFn
	renameFn = func(string, string) error { calls++; return other }
	t.Cleanup(func() { renameFn = old })

	if err := renameReplace("a", "b"); !errors.Is(err, other) {
		t.Fatalf("err = %v, want %v", err, other)
	}
	if calls != 1 {
		t.Fatalf("rename attempts = %d, want 1", calls)
	}
}
