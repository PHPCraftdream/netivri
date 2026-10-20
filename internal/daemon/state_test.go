package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func testState() State {
	return State{
		PID:        4321,
		Listen:     "127.0.0.1:8080",
		Control:    "127.0.0.1:4321",
		Token:      "test-token",
		Foreground: true,
		StartedAt:  "2025-01-02T03:04:05Z",
		Version:    "test",
		Auth:       "none",
		Protocols:  []string{"http", "https"},
		Route:      "",
	}
}

func stateExists(home string) bool {
	_, err := os.Stat(StatePath(home))
	return err == nil
}

func TestStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := testState()
	if err := WriteState(home, want); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	got, err := ReadState(home)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("state round-trip mismatch")
	}
}

func TestStateFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not apply on Windows")
	}
	home := t.TempDir()
	if err := WriteState(home, testState()); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	fi, err := os.Stat(StatePath(home))
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(RunDir(home))
	if err != nil {
		t.Fatalf("stat run directory: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("run directory mode = %o, want 700", di.Mode().Perm())
	}
}

func TestReadStateMissing(t *testing.T) {
	_, err := ReadState(t.TempDir())
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("ReadState(missing) error = %v, want ErrNotRunning", err)
	}
}

func TestReadStateCorruptAndIncomplete(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	cases := map[string]string{
		"corrupt":     "{not json",
		"empty":       "",
		"unknown-key": `{"pid":1,"control":"127.0.0.1:1","token":"t","bogus":true}`,
		"incomplete":  `{"control":"127.0.0.1:1","token":"t"}`,
		"trailing":    `{"pid":1,"control":"127.0.0.1:1","token":"t"} {"x":1}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(StatePath(home), []byte(content), 0o600); err != nil {
				t.Fatalf("write state: %v", err)
			}
			_, err := ReadState(home)
			if err == nil || errors.Is(err, ErrNotRunning) {
				t.Error("ReadState accepted invalid state")
			}
		})
	}
}

func TestReadStateOversized(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	if err := os.WriteFile(StatePath(home), []byte(strings.Repeat("x", maxStateSize+1)), 0o600); err != nil {
		t.Fatalf("write oversized state: %v", err)
	}
	if _, err := ReadState(home); err == nil {
		t.Error("oversized state accepted")
	}
}

func TestRemoveStateIf(t *testing.T) {
	home := t.TempDir()
	st := testState()
	if err := WriteState(home, st); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	t.Run("matching pid and token", func(t *testing.T) {
		RemoveStateIf(home, st.PID, st.Token)
		if stateExists(home) {
			t.Error("matching state remains")
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		if err := WriteState(home, st); err != nil {
			t.Fatalf("WriteState: %v", err)
		}
		RemoveStateIf(home, st.PID, "wrong")
		if !stateExists(home) {
			t.Error("state removed with wrong token")
		}
	})
	t.Run("wrong pid", func(t *testing.T) {
		RemoveStateIf(home, st.PID+1, st.Token)
		if !stateExists(home) {
			t.Error("state removed with wrong pid")
		}
	})
	t.Run("missing", func(t *testing.T) {
		RemoveStateIf(filepath.Join(home, "nope"), st.PID, st.Token)
	})
}
