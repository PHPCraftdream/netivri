//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// isConnRefused reports a connection-refused error. On Unix the errno
// survives unwrapping, so errors.Is finds it through net.OpError and
// http client errors.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
