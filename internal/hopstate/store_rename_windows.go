//go:build windows

package hopstate

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const renameAttempts = 8

func renameReplace(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	for i := 0; i < renameAttempts; i++ {
		err = windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil || !transientRenameError(err) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	return err
}

func transientRenameError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.Errno(5), syscall.Errno(32), syscall.Errno(33):
		return true
	}
	return false
}
