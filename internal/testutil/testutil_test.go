package testutil

import (
	"os"
	"testing"
)

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
