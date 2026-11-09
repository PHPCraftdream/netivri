//go:build windows

package servers

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func lockStoreFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
	runtime.KeepAlive(&overlapped)
	return err
}

func tryLockStoreFile(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	runtime.KeepAlive(&overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlockStoreFile(file *os.File) {
	defer func() { _ = file.Close() }()
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
	runtime.KeepAlive(&overlapped)
}
