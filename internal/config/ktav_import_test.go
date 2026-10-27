package config

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The root package of the Ktav binding links the native-library loader
// (purego), which turns the Linux binary into a dynamically linked one that
// cannot run on musl. Only its pure-Go subpackage may be imported.
func TestNoPackageImportsTheKtavRootPackage(t *testing.T) {
	const root = "github.com/ktav-lang/golang"
	var seen int
	for _, dir := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if _, statErr := os.Stat(path); statErr != nil {
				return statErr
			}
			f, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			seen++
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == root {
					t.Errorf("%s imports %s: use %s/pure so the Linux binary stays static", path, root, root)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 50 {
		t.Fatalf("only %d Go files scanned: the walk did not find the sources", seen)
	}
}
