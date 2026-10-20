package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// ktavLibMissingHint is shown whenever the Ktav native library cannot
// be located.
const ktavLibMissingHint = "reinstall netivri: the Ktav native library must sit next to the binary"

// libName is the canonical native library name shipped next to the
// netivri binary (docs/design/config-ktav.md).
func libName() string {
	switch runtime.GOOS {
	case "windows":
		return "ktav_cabi.dll"
	case "darwin":
		return "libktav_cabi.dylib"
	default:
		return "libktav_cabi.so"
	}
}

// EnsureKtavLib pins the Ktav native library so the ktav-go binding
// never falls through to its auto-download from GitHub (that download
// would bypass the configured fetch proxy). It is idempotent and cheap:
// call it before every first ktav use in a code path.
//
// If KTAV_LIB_PATH is already set, the file must exist. Otherwise the
// canonical library name is looked up next to the running executable
// and KTAV_LIB_PATH is set to its absolute path. When nothing is found,
// a *cli.Error with code KTAV_LIB_MISSING is returned and no ktav
// function must be called.
func EnsureKtavLib() error {
	if p := os.Getenv("KTAV_LIB_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return &cli.Error{
				Code:    CodeKtavLibMissing,
				Exit:    cli.ExitInternal,
				Message: fmt.Sprintf("KTAV_LIB_PATH points at a missing file: %v", err),
				Hint:    ktavLibMissingHint,
			}
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return ktavLibMissing(fmt.Sprintf("cannot locate the running executable: %v", err))
	}
	// A symlinked launcher must find the library next to the real binary.
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	path := filepath.Join(filepath.Dir(exe), libName())
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if _, err := os.Stat(abs); err != nil {
		return ktavLibMissing(fmt.Sprintf("Ktav native library not found at %s", abs))
	}
	return os.Setenv("KTAV_LIB_PATH", abs)
}

func ktavLibMissing(message string) *cli.Error {
	return &cli.Error{
		Code:    CodeKtavLibMissing,
		Exit:    cli.ExitInternal,
		Message: message,
		Hint:    ktavLibMissingHint,
	}
}
