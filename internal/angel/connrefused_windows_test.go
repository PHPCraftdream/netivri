//go:build windows

package angel

import "syscall"

func refusedTestErr() error { return syscall.Errno(10061) }
