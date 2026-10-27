package config

import (
	"errors"

	"github.com/ktav-lang/golang/pure"
)

// Only the pure-Go subpackage of the binding is imported, never the root
// package: the root one links the native-library loader (purego), which makes
// the Linux binary dynamically linked against libc and so unusable on musl.
// Nothing is loaded, downloaded or shipped next to the binary, and nothing can
// reach GitHub on its own (docs/design/config-ktav.md). Every call into the
// binding goes through the wrappers below.

func ktavLoadsStrict(src string) error {
	_, err := pure.LoadsStrict(src)
	return err
}

func ktavLoadsInto(src string, target any) error {
	return pure.LoadsInto(src, target)
}

// KtavDumps renders a value as a Ktav document. The command output
// (internal/cli) is written through it.
func KtavDumps(v any) (string, error) { return ktavDumps(v) }

func ktavDumps(v any) (string, error) { return pure.Dumps(v) }

// KtavLoadsInto parses src as Ktav into target (a struct or a map). A parse
// error is reduced to its structured fields (class, reason, line): the
// binding's own message can quote the source text, which for a stored
// subscription is a node credential.
func KtavLoadsInto(src string, target any) error {
	err := ktavLoadsInto(src, target)
	if err == nil {
		return nil
	}
	return errors.New(bindingErrorSummary(err))
}
