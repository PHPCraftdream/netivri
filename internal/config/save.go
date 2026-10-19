package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Save renders the configuration with Marshal and writes it atomically.
func Save(path string, cfg *Config) error {
	data, err := Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(data))
}

// writeFileAtomic writes data to path atomically: a temporary file is
// created in the same directory with mode 0600, fsynced and renamed
// over the target, so readers never observe a partial file. The parent
// directory is created with mode 0700 when missing.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".netivri-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
