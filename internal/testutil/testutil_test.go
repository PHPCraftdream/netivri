package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRequireKtavLibPublic exercises the public helper end to end: with a
// valid KTAV_LIB_PATH it must return without failing the test.
func TestRequireKtavLibPublic(t *testing.T) {
	t.Run("public helper, lib present", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "libktav.so")
		if err := os.WriteFile(path, []byte("fake"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KTAV_LIB_PATH", path)
		RequireKtavLib(t) // must not call t.Fatalf
	})
}

func TestTempHome(t *testing.T) {
	dir := TempHome(t)
	if dir == "" {
		t.Fatal("TempHome returned empty dir")
	}
	if got := os.Getenv("NETIVRI_HOME"); got != dir {
		t.Fatalf("NETIVRI_HOME = %q, want %q", got, dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("TempHome dir %q missing: %v", dir, err)
	}
}

func TestRequireKtavLibInternal(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "libktav.so")
		if err := os.WriteFile(path, []byte("fake"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := requireKtavLib(path); err != nil {
			t.Fatalf("requireKtavLib(%q) = %v, want nil", path, err)
		}
	})

	t.Run("unset", func(t *testing.T) {
		err := requireKtavLib("")
		if err == nil {
			t.Fatal("requireKtavLib(\"\") = nil, want error")
		}
		if !strings.Contains(err.Error(), "unset") {
			t.Fatalf("error %q should mention the variable is unset", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		err := requireKtavLib(filepath.Join(t.TempDir(), "nope.so"))
		if err == nil {
			t.Fatal("requireKtavLib on missing file = nil, want error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error %q should wrap os.ErrNotExist", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		if err := requireKtavLib(t.TempDir()); err == nil {
			t.Fatal("requireKtavLib on directory = nil, want error")
		}
	})
}
