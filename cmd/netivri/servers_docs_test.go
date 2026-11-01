package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestServerProtocolDocumentation(t *testing.T) {
	read := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	docsDir := filepath.Join("..", "..", "docs", "servers")
	index := read(t, filepath.Join(docsDir, "README.md"))
	rootReadme := read(t, filepath.Join("..", "..", "README.md"))
	topic := findHelpTopic("server")
	if topic == nil {
		t.Fatal("no server help topic")
	}
	var notes strings.Builder
	for _, note := range topic.Notes {
		notes.WriteString(note)
		notes.WriteByte('\n')
	}

	e2eFiles, err := filepath.Glob("server_e2e*_test.go")
	if err != nil {
		t.Fatalf("glob server e2e tests: %v", err)
	}
	if len(e2eFiles) == 0 {
		t.Fatal("no server_e2e*_test.go files")
	}

	for _, name := range servers.Names() {
		name := name
		t.Run(name, func(t *testing.T) {
			pagePath := filepath.Join(docsDir, name+".md")
			page := read(t, pagePath)
			if !strings.HasPrefix(page, "# ") {
				t.Errorf("%s must start with a Markdown heading", pagePath)
			}
			if !strings.Contains(index, "]("+name+".md)") {
				t.Errorf("docs/servers/README.md has no link to %s.md", name)
			}
			if !strings.Contains(rootReadme, name) {
				t.Errorf("root README.md does not mention registered protocol %q", name)
			}
			if !strings.Contains(notes.String(), name) {
				t.Errorf("server help topic Notes do not mention protocol %q", name)
			}
			quotedName := strconv.Quote(name)
			found := false
			for _, path := range e2eFiles {
				if strings.Contains(read(t, path), quotedName) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no server e2e test contains protocol %s as a quoted Go string", quotedName)
			}
		})
	}
}

func TestServerProtocolPagesAreRegistered(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "docs", "servers"))
	if err != nil {
		t.Fatalf("read docs/servers: %v", err)
	}
	registered := make(map[string]bool)
	for _, name := range servers.Names() {
		registered[name] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "README.md" {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".md")
		if base == "cli" || base == "tls" {
			continue
		}
		t.Run(base, func(t *testing.T) {
			if !registered[base] {
				t.Errorf("docs/servers/%s.md is not a registered protocol", base)
			}
		})
	}
}
