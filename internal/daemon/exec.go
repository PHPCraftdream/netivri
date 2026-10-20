package daemon

import (
	"os"
	"os/exec"
)

// startChild starts the detached daemon child with the given args. The
// child inherits the caller's environment; stdin is closed, stdout and
// stderr go to the daemon log. It is split from spawn.go so the
// orchestration stays unit-testable without spawning anything.
func startChild(exe string, args []string, stdout, stderr *os.File) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
