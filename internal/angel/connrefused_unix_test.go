//go:build !windows

package angel

import "syscall"

func refusedTestErr() error { return syscall.ECONNREFUSED }
