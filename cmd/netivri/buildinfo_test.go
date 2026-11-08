package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
)

const fullRevision = "05f31ce0123456789abcdef0123456789abcdef0"

// withBuildInfo makes the binary carry the given VCS settings (none when
// settings is nil and ok is false) and restores the real reader after the test.
func withBuildInfo(t *testing.T, ok bool, settings ...debug.BuildSetting) {
	t.Helper()
	old := readBuildInfo
	t.Cleanup(func() { readBuildInfo = old })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		if !ok {
			return nil, false
		}
		return &debug.BuildInfo{Settings: settings}, true
	}
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	t.Cleanup(func() { version = old })
	version = v
}

func TestBuildCommit(t *testing.T) {
	rev := debug.BuildSetting{Key: "vcs.revision", Value: fullRevision}
	cases := []struct {
		name     string
		ok       bool
		settings []debug.BuildSetting
		want     string
	}{
		{"a clean checkout", true, []debug.BuildSetting{rev, {Key: "vcs.modified", Value: "false"}}, "05f31ce"},
		{"a modified tree", true, []debug.BuildSetting{rev, {Key: "vcs.modified", Value: "true"}}, "05f31ce-dirty"},
		{"no modified flag", true, []debug.BuildSetting{rev}, "05f31ce"},
		{"a short revision is kept whole", true, []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}, "abc"},
		{"no revision (-buildvcs=false)", true, []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}, {Key: "GOOS", Value: "linux"}}, ""},
		{"no build info at all", false, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withBuildInfo(t, c.ok, c.settings...)
			if got := buildCommit(); got != c.want {
				t.Fatalf("buildCommit() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestVersionWithCommit(t *testing.T) {
	withVersion(t, "0.0.0-dryrun")
	withBuildInfo(t, true, debug.BuildSetting{Key: "vcs.revision", Value: fullRevision})
	if got := versionWithCommit(); got != "0.0.0-dryrun+05f31ce" {
		t.Fatalf("versionWithCommit() = %q", got)
	}
	withBuildInfo(t, false)
	if got := versionWithCommit(); got != "0.0.0-dryrun" {
		t.Fatalf("without a commit versionWithCommit() = %q, want the bare version", got)
	}
}

func TestStartMessageNamesTheBuild(t *testing.T) {
	withVersion(t, "0.1.0")
	withBuildInfo(t, true, debug.BuildSetting{Key: "vcs.revision", Value: fullRevision}, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
	want := "start pid=4242 listen=127.0.0.1:7890 route=direct version=0.1.0+05f31ce-dirty"
	if got := startMessage(4242, "127.0.0.1:7890", "direct"); got != want {
		t.Fatalf("startMessage = %q, want %q", got, want)
	}
	// the prefix that readers of the log look for stays first
	if !strings.HasPrefix(startMessage(1, "a", "b"), "start pid=1 listen=a route=b") {
		t.Fatal("the start line no longer begins as it did")
	}
}

func TestVersionCommandAddsTheCommitOnlyWhenKnown(t *testing.T) {
	withVersion(t, "0.1.0")
	withBuildInfo(t, true, debug.BuildSetting{Key: "vcs.revision", Value: fullRevision})
	out := mustDispatch(t, "version")
	if !strings.Contains(out, "0.1.0") || !strings.Contains(out, "05f31ce") || !strings.Contains(out, "commit") {
		t.Fatalf("version output = %q, want the version and the commit", out)
	}
	withBuildInfo(t, false)
	out = mustDispatch(t, "version")
	if !strings.Contains(out, "0.1.0") || strings.Contains(out, "commit") {
		t.Fatalf("version output without a commit = %q", out)
	}
}

func TestVersionWithCommitIsWhatTheCrashDumpHeaderCarries(t *testing.T) {
	withVersion(t, "0.0.0-dryrun")
	withBuildInfo(t, true, debug.BuildSetting{Key: "vcs.revision", Value: fullRevision})
	var infos, errs, warns []string
	rec := func(dst *[]string) func(string, ...any) {
		return func(format string, args ...any) { *dst = append(*dst, fmt.Sprintf(format, args...)) }
	}
	home := t.TempDir()
	disarm := armCrashDump(home, rec(&infos), rec(&errs), rec(&warns))
	defer disarm()
	if len(warns) != 0 {
		t.Fatalf("warnings = %q", warns)
	}
	files := pendingDumps(t, angel.CrashDir(home))
	if len(files) != 1 {
		t.Fatalf("dump files = %v", files)
	}
	raw, err := os.ReadFile(filepath.Join(angel.CrashDir(home), files[0]))
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	if !strings.Contains(data, "version: 0.0.0-dryrun+05f31ce\n") {
		t.Fatalf("dump header = %q, want the version with the commit", data)
	}
}
