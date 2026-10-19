package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// configFromBindingError builds the CONFIG_INVALID message from the
// binding's STRUCTURED error fields only (Class, Reason, Line,
// SpecSection, Path). Free-form fields of *ktav.Error (Msg, LineText,
// Body, Canonical) can embed source text — a password, a subscription
// URL — and are never included. A non-*ktav.Error gets a fixed generic
// message.
func configFromBindingError(err error) *cli.Error {
	// Guarded-wrapper failures (KTAV_LIB_MISSING) propagate unchanged,
	// wherever they occur — including a library check that fails after
	// an earlier call succeeded.
	var cliErr *cli.Error
	if errors.As(err, &cliErr) {
		return cliErr
	}
	var kerr *ktav.Error
	if !errors.As(err, &kerr) {
		return invalid("config parse error: the document is not valid Ktav")
	}
	var b strings.Builder
	b.WriteString("config parse error")
	if kerr.Class != "" {
		b.WriteString(": " + kerr.Class)
	}
	if kerr.Reason != "" {
		b.WriteString(" (" + kerr.Reason + ")")
	}
	if kerr.Line > 0 {
		b.WriteString(fmt.Sprintf(" at line %d", kerr.Line))
	}
	if kerr.SpecSection != "" {
		b.WriteString(" [" + kerr.SpecSection + "]")
	}
	if len(kerr.Path) > 0 {
		b.WriteString(" at key " + strings.Join(kerr.Path, "."))
	}
	return invalid("%s", b.String())
}

// Load reads and validates the configuration file at path. A missing
// file is reported as an error wrapping os.ErrNotExist; parse or
// validation problems are *cli.Error with code CONFIG_INVALID. Errors
// from the guarded ktav wrappers (KTAV_LIB_MISSING) propagate
// unchanged. The Ktav native library is pinned first so no hidden
// download happens.
func Load(path string) (*Config, error) {
	if err := EnsureKtavLib(); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	// Strict parse first: lossy numeric spellings are rejected instead
	// of silently coerced (docs/design/config-ktav.md).
	if err := ktavLoadsStrict(string(src)); err != nil {
		return nil, configFromBindingError(err)
	}
	cfg := Default()
	if err := ktavLoadsInto(string(src), cfg); err != nil {
		return nil, configFromBindingError(err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadOrDefault loads the configuration file, falling back to Default()
// when the file does not exist. Any other error is propagated. A missing
// file never needs the Ktav native library.
func LoadOrDefault(path string) (*Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	cfg, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return nil, err
	}
	return cfg, nil
}
