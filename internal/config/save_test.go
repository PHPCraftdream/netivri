package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ktav")
	if err := writeFileAtomic(path, []byte("listen.port: 7890")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "listen.port: 7890" {
		t.Fatalf("content = %q", got)
	}
	assertNoTempFiles(t, dir)
}

func TestWriteFileAtomicOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ktav")
	if err := writeFileAtomic(path, []byte("a: 1")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileAtomic(path, []byte("a: 2")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a: 2" {
		t.Fatalf("content = %q", got)
	}
	assertNoTempFiles(t, dir)
}

func TestWriteFileAtomicCreatesMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "config.ktav")
	if err := writeFileAtomic(path, []byte("a: 1")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

// Concurrent Save calls to one path: the final file must be exactly
// one of the rendered configurations (rename is atomic) and no temp
// files remain.
func TestSaveConcurrentWriters(t *testing.T) {
	requireKtavLib(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ktav")
	cfgs := []*Config{Default(), fullConfig(), remoteInitBaseline()}
	contents := make([]string, len(cfgs))
	for i, cfg := range cfgs {
		doc, err := Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal %d: %v", i, err)
		}
		contents[i] = doc
	}
	errs := make(chan error, len(cfgs))
	var wg sync.WaitGroup
	for _, cfg := range cfgs {
		wg.Add(1)
		go func(cfg *Config) {
			defer wg.Done()
			errs <- Save(path, cfg)
		}(cfg)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Save: %v", err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, c := range contents {
		if string(got) == c {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("final content is not one of the rendered configurations")
	}
	assertNoTempFiles(t, dir)
}

// POSIX file mode: readable and writable by the owner only.
func TestWriteFileAtomicModeIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on windows")
	}
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := writeFileAtomic(path, []byte("a: 1")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".netivri-config-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
