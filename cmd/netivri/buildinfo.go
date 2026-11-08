package main

import (
	"fmt"
	"runtime/debug"
)

// readBuildInfo is replaced by tests: a test binary carries no VCS data.
var readBuildInfo = debug.ReadBuildInfo

// commitLen is how many characters of the commit hash are shown.
const commitLen = 7

// buildCommit is the commit this binary was built from: the first characters
// of the hash Go stamps into a binary built in a git checkout, with "-dirty"
// after them when the tree had uncommitted changes. It is "" when the binary
// carries no VCS data (a test binary, or -buildvcs=false).
func buildCommit() string {
	info, ok := readBuildInfo()
	if !ok {
		return ""
	}
	var rev string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > commitLen {
		rev = rev[:commitLen]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}

// versionWithCommit names the build in a log line or a dump header: the version,
// and after a "+" (semver build metadata) the commit when it is known. Every
// dry-run build has the same version, so the commit is what tells them apart.
func versionWithCommit() string {
	if c := buildCommit(); c != "" {
		return fmt.Sprintf("%s+%s", version, c)
	}
	return version
}

// startMessage is the log line of an angel that started.
func startMessage(pid int, listen, route string) string {
	return fmt.Sprintf("start pid=%d listen=%s route=%s version=%s", pid, listen, route, versionWithCommit())
}
