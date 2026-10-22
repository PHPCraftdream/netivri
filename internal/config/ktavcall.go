package config

import (
	"errors"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/cli"
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

// KtavLoadsInto parses src as Ktav into target (a struct or a map), pinning the
// native library first. A parse error is reduced to its structured fields
// (class, reason, line, key path): the binding's own message can quote the
// source text, which for a stored subscription is a node credential.
func KtavLoadsInto(src string, target any) error {
	err := ktavLoadsInto(src, target)
	if err == nil {
		return nil
	}
	var ce *cli.Error
	if errors.As(err, &ce) {
		return ce
	}
	return errors.New(bindingErrorSummary(err))
}
