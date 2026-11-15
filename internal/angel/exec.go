package angel

import (
	"context"
	"os"
	"os/exec"
)

// startChild starts the detached angel child with the given args. The
// child inherits the caller's environment; stdin is closed, stdout and
// stderr go to the angel log. It is split from spawn.go so the
// orchestration stays unit-testable without spawning anything.
func startChild(ctx context.Context, exe string, args []string, stdout, stderr *os.File) (*exec.Cmd, error) {
	// WithoutCancel: the angel must outlive the spawn request, so cancelling ctx must not kill it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, args...) //nolint:gosec // G204: exe is the running netivri binary (os.Executable), args are built by the program
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
