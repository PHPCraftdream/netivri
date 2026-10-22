package config

import (
	ktav "github.com/ktav-lang/golang"
)

// Every call into the ktav binding must go through these wrappers: each
// one pins the native library first (EnsureKtavLib), so the binding's
// auto-download from GitHub is never reached, no matter which code path
// calls in (docs/design/config-ktav.md).

func ktavLoadsStrict(src string) error {
	if err := EnsureKtavLib(); err != nil {
		return err
	}
	_, err := ktav.LoadsStrict(src)
	return err
}

func ktavLoadsInto(src string, target any) error {
	if err := EnsureKtavLib(); err != nil {
		return err
	}
	return ktav.LoadsInto(src, target)
}

// KtavDumps renders a value as a Ktav document, pinning the native library
// first. The command output (internal/cli) is written through it.
func KtavDumps(v any) (string, error) { return ktavDumps(v) }

func ktavDumps(v any) (string, error) {
	if err := EnsureKtavLib(); err != nil {
		return "", err
	}
	return ktav.Dumps(v)
}
