//go:build windows

package angel

import (
	"os/exec"
	"syscall"
)

const createDetachedProcess = 0x00000008 // DETACHED_PROCESS

// detach hides the console window and detaches the child from this
// process group so it survives the parent.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createDetachedProcess,
		HideWindow:    true,
	}
}
