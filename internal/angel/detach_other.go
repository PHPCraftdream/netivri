//go:build !windows

package angel

import (
	"os/exec"
	"syscall"
)

// detach puts the child into its own session so it survives the parent.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
