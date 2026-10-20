package config

// WriteFileAtomic writes data to path atomically (temp file in the same
// directory, fsync, rename) with mode 0600; the parent directory is
// created with mode 0700 if missing.
func WriteFileAtomic(path string, data []byte) error {
	return writeFileAtomic(path, data)
}
