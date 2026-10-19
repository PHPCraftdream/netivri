// Package testutil holds shared test helpers. Import it only from
// _test.go files, never from production code.
package testutil

import (
	"fmt"
	"os"
	"testing"
)

// RequireKtavLib fails the test unless the Ktav native library is
// available via KTAV_LIB_PATH. CI fetches it with
// scripts/fetch-ktav-lib.sh; a failure here means the setup is broken,
// so the test must fail rather than skip.
func RequireKtavLib(t testing.TB) {
	t.Helper()
	if err := requireKtavLib(os.Getenv("KTAV_LIB_PATH")); err != nil {
		t.Fatalf("Ktav native library required but unavailable: %v (CI provides it via scripts/fetch-ktav-lib.sh and sets KTAV_LIB_PATH)", err)
	}
}

// requireKtavLib validates a candidate KTAV_LIB_PATH value. Split out so
// it can be tested without a failing testing.TB.
func requireKtavLib(path string) error {
	if path == "" {
		return fmt.Errorf("KTAV_LIB_PATH is unset or empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("KTAV_LIB_PATH=%q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("KTAV_LIB_PATH=%q is a directory, expected the library file", path)
	}
	return nil
}

// TempHome creates a temporary directory and points NETIVRI_HOME at it.
// It returns the directory path.
func TempHome(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NETIVRI_HOME", dir)
	return dir
}
