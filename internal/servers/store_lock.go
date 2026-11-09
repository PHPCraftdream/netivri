package servers

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const storeLockSuffix = ".lock"

// lock holds the stable sidecar for a store transaction.
func (s *Store) lock() (func(), error) {
	file, err := openStoreLockFile(s)
	if err != nil {
		return nil, errors.New("cannot lock server secrets")
	}
	if err := lockStoreFile(file); err != nil {
		_ = file.Close()
		return nil, errors.New("cannot lock server secrets")
	}
	return storeUnlock(file), nil
}

// tryLock checks contention without timing-dependent waits.
func (s *Store) tryLock() (unlock func(), acquired bool, err error) {
	file, err := openStoreLockFile(s)
	if err != nil {
		return nil, false, errors.New("cannot lock server secrets")
	}
	acquired, err = tryLockStoreFile(file)
	if err != nil {
		_ = file.Close()
		return nil, false, errors.New("cannot lock server secrets")
	}
	if !acquired {
		_ = file.Close()
		return nil, false, nil
	}
	return storeUnlock(file), true, nil
}

func openStoreLockFile(s *Store) (*os.File, error) {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Keep this sidecar stable; unlinking it can split locks across inodes.
	return os.OpenFile(s.path+storeLockSuffix, os.O_CREATE|os.O_RDWR, 0o600)
}

func storeUnlock(file *os.File) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockStoreFile(file)
		})
	}
}
