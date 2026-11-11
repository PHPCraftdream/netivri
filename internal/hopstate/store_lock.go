package hopstate

import (
	"os"
	"path/filepath"
	"sync"
)

const storeLockSuffix = ".lock"

func (s *Store) lock() (func(), error) {
	file, err := openStoreLockFile(s)
	if err != nil {
		return nil, err
	}
	if err := lockStoreFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return storeUnlock(file), nil
}

func (s *Store) tryLock() (unlock func(), acquired bool, err error) {
	file, err := openStoreLockFile(s)
	if err != nil {
		return nil, false, err
	}
	acquired, err = tryLockStoreFile(file)
	if err != nil {
		_ = file.Close()
		return nil, false, err
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
	return os.OpenFile(s.path+storeLockSuffix, os.O_CREATE|os.O_RDWR, 0o600)
}

func storeUnlock(file *os.File) func() {
	var once sync.Once
	return func() {
		once.Do(func() { unlockStoreFile(file) })
	}
}
