//go:build windows

package core

import (
	"errors"
	"syscall"
)

// isAddrInUse reports a busy listen port. Windows reports Winsock
// WSAEADDRINUSE (10048), which is not syscall.EADDRINUSE.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.Errno(10048))
}
