//go:build windows

package daemon

import (
	"errors"
	"syscall"
)

// wsaEConnRefused is Windows' connection-refused errno (10061). Go's
// errors.Is does not map it to syscall.ECONNREFUSED, so the underlying
// errno is compared directly.
const wsaEConnRefused = syscall.Errno(10061)

// isConnRefused reports a connection-refused error.
func isConnRefused(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == wsaEConnRefused
}
