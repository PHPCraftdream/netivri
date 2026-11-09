//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package servers

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockStoreFile(file *os.File) error {
	return flockRetry(file, unix.LOCK_EX)
}

func tryLockStoreFile(file *os.File) (bool, error) {
	err := flockRetry(file, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockStoreFile(file *os.File) {
	defer func() { _ = file.Close() }()
	_ = flockRetry(file, unix.LOCK_UN)
}

func flockRetry(file *os.File, operation int) error {
	for {
		err := unix.Flock(int(file.Fd()), operation)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
