//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package servers

import (
	"errors"
	"os"
)

func lockStoreFile(*os.File) error {
	return errors.New("file locking is not supported on this platform")
}

func tryLockStoreFile(*os.File) (bool, error) {
	return false, errors.New("file locking is not supported on this platform")
}

func unlockStoreFile(file *os.File) {
	_ = file.Close()
}
