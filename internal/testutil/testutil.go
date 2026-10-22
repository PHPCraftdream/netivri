// Package testutil holds shared test helpers. Import it only from
// _test.go files, never from production code.
package testutil

import "testing"

// TempHome creates a temporary directory and points NETIVRI_HOME at it.
// It returns the directory path.
func TempHome(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NETIVRI_HOME", dir)
	return dir
}
