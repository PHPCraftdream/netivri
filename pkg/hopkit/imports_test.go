package hopkit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestImportsStayPublicOrStandardLibrary(t *testing.T) {
	root := "."
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			first := strings.SplitN(importPath, "/", 2)[0]
			if !strings.Contains(first, ".") {
				continue
			}
			if importPath == "github.com/PHPCraftdream/netivri/pkg/hopkit" || strings.HasPrefix(importPath, "github.com/PHPCraftdream/netivri/pkg/hopkit/") {
				continue
			}
			t.Errorf("%s imports non-standard package %q outside hopkit", path, importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
