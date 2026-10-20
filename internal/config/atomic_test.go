package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExportedWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	if err := WriteFileAtomic(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Fatalf("content = %q", data)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600", perm)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the target (temp file left behind)", len(entries))
	}

	// Overwrite replaces the content and leaves no temporary files.
	if err := WriteFileAtomic(path, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("second WriteFileAtomic: %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != `{"a":2}` {
		t.Fatalf("content after overwrite = %q", data)
	}
	entries, _ = os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("directory has %d entries after overwrite, want 1", len(entries))
	}
}
