package config

import (
	"errors"

	ktav "github.com/ktav-lang/golang"
)

// The binding runs its pure-Go backend: nothing is loaded, downloaded or
// shipped next to the binary, and nothing can reach GitHub on its own
// (docs/design/config-ktav.md). Every call into it goes through the
// wrappers below.
func init() {
	if err := ktav.UsePure(); err != nil {
		panic("netivri: cannot select the pure Ktav backend: " + err.Error())
	}
}

func ktavLoadsStrict(src string) error {
	_, err := ktav.LoadsStrict(src)
	return err
}

func ktavLoadsInto(src string, target any) error {
	return ktav.LoadsInto(src, target)
}

// KtavDumps renders a value as a Ktav document. The command output
// (internal/cli) is written through it.
func KtavDumps(v any) (string, error) { return ktavDumps(v) }

func ktavDumps(v any) (string, error) { return ktav.Dumps(v) }

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
