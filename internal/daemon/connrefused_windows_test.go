//go:build windows

package daemon

import "syscall"

func refusedTestErr() error { return syscall.Errno(10061) }
