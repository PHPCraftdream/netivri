package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestEnsureKtavLibMissingEnvPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-lib.so")
	t.Setenv("KTAV_LIB_PATH", missing)
	err := EnsureKtavLib()
	if err == nil {
		t.Fatal("expected an error for a missing KTAV_LIB_PATH file")
	}
	cliErr, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("error type = %T, want *cli.Error", err)
	}
	if cliErr.Code != CodeKtavLibMissing {
		t.Errorf("code = %q, want %q", cliErr.Code, CodeKtavLibMissing)
	}
	if cliErr.Exit != cli.ExitInternal {
		t.Errorf("exit = %d, want %d", cliErr.Exit, cli.ExitInternal)
	}
	if cliErr.Hint != ktavLibMissingHint {
		t.Errorf("hint = %q", cliErr.Hint)
	}
}

func TestEnsureKtavLibIdempotentWithEnv(t *testing.T) {
	requireKtavLib(t)
	path := os.Getenv("KTAV_LIB_PATH")
	for i := 0; i < 3; i++ {
		if err := EnsureKtavLib(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if os.Getenv("KTAV_LIB_PATH") != path {
		t.Fatalf("KTAV_LIB_PATH changed from %q to %q", path, os.Getenv("KTAV_LIB_PATH"))
	}
}
