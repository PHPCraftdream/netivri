package main

import (
	"context"
	"io"

	"github.com/PHPCraftdream/netivri/internal/angel"
)

// The service operations shared by the CLI and the interactive shell: the
// shell calls the same handlers the commands run, so the effect is the same
// by construction (docs/design/parity.md).

// ServiceStart starts the background angel like `netivri up`; it is idempotent.
func ServiceStart(home string) error {
	return runUp([]string{"--home", home}, io.Discard)
}

// ServiceStop stops the angel like `netivri down`; nothing running is not an error.
func ServiceStop(home string) error {
	return runDown([]string{"--home", home}, io.Discard)
}

// ServiceProbe reads the live status of the angel of this home.
func ServiceProbe(home string) (angel.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
	defer cancel()
	_, st, err := angelProbeLive(ctx, home)
	return st, err
}

// ServiceChecks runs every check of `netivri doctor` once.
func ServiceChecks(home string) []doctorCheck { return doctorRun(home) }
