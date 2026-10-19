//go:build !windows

package core

import (
	"errors"
	"syscall"
)

// isAddrInUse reports a busy listen port (POSIX errno).
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
