package angel

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A crash of the angel (a panic or a fatal runtime error in any goroutine,
// the core's included) is written by the Go runtime to run/netivri.log without
// a time or a level. The crash dump gives it a file of its own: the runtime
// writes the traceback of every goroutine to run/crash/crash-*.pending, which
// starts with a header (start time, pid, version). A clean stop removes the
// file; the next start finds a file with a traceback, keeps it as
// crash-*.txt and reports it in the log.

const (
	crashPrefix  = "crash-"
	crashPending = ".pending"
	crashKept    = ".txt"
	// MaxCrashDumps is the number of kept dumps; older ones are removed.
	MaxCrashDumps = 10
	crashHeader   = "netivri angel crash dump"
)

// CrashDir returns the directory of the crash dumps inside a home directory.
func CrashDir(home string) string { return filepath.Join(RunDir(home), "crash") }

// Seams over the runtime, replaced by tests: the real ones are global.
var (
	setCrashOutput = func(f *os.File) error { return debug.SetCrashOutput(f, debug.CrashOptions{}) }
	setTraceback   = debug.SetTraceback
)

// CrashDump is the armed dump file of the running angel.
type CrashDump struct {
	f    *os.File
	path string
}

// StartCrashDump arms the crash dump: the runtime writes the traceback of all
// goroutines to a new file in run/crash on a panic or a fatal error. Close it
// on a clean stop.
func StartCrashDump(home, version string, now time.Time) (*CrashDump, error) {
	dir := CrashDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pid := os.Getpid()
	name := fmt.Sprintf("%s%s-%d%s", crashPrefix, now.UTC().Format("20060102T150405Z"), pid, crashPending)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("%s\nstarted: %s\npid: %d\nversion: %s\n\n", crashHeader, now.UTC().Format(time.RFC3339), pid, version)
	if _, err := f.WriteString(header); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := setCrashOutput(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	setTraceback("all")
	return &CrashDump{f: f, path: path}, nil
}

// Close disarms the dump and removes its file: the angel stopped cleanly.
func (d *CrashDump) Close() {
	if d == nil || d.f == nil {
		return
	}
	_ = setCrashOutput(nil)
	_ = d.f.Close()
	_ = os.Remove(d.path)
	d.f = nil
}

// Crash is one finished (kept) or abandoned dump of an earlier angel.
type Crash struct {
	// File is the name inside run/crash.
	File string
	PID  int
	// Version is the angel's version from the dump header ("" when unreadable).
	Version string
	// At is when the file was last written: the crash for a dump.
	At time.Time
}

// CrashReport is what the earlier angels left behind.
type CrashReport struct {
	// Dumps crashed: their traceback is kept in run/crash.
	Dumps []Crash
	// Unclean stopped without a traceback (killed, or the machine lost
	// power); their empty dump files are removed.
	Unclean []Crash
}

// ReportCrashes looks at the dump files an earlier angel left in run/crash:
// a file with a traceback is kept (renamed to .txt) and reported once, an
// empty one is removed and reported as an unclean stop. It also prunes the kept
// dumps to MaxCrashDumps. Call it once the angel owns its port, before
// StartCrashDump.
func ReportCrashes(home string) CrashReport {
	var rep CrashReport
	dir := CrashDir(home)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rep
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, crashPrefix) || !strings.HasSuffix(name, crashPending) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		c := Crash{File: name, PID: crashPID(name), Version: crashVersion(string(data))}
		if info, err := e.Info(); err == nil {
			c.At = info.ModTime().UTC()
		}
		if strings.Contains(string(data), "\ngoroutine ") || strings.Contains(string(data), "\npanic: ") || strings.Contains(string(data), "\nfatal error: ") {
			kept := strings.TrimSuffix(name, crashPending) + crashKept
			if err := os.Rename(path, filepath.Join(dir, kept)); err != nil {
				continue
			}
			c.File = kept
			rep.Dumps = append(rep.Dumps, c)
			continue
		}
		if err := os.Remove(path); err == nil {
			rep.Unclean = append(rep.Unclean, c)
		}
	}
	pruneCrashDumps(dir)
	return rep
}

// crashPID reads the pid out of crash-<time>-<pid>.<ext>; 0 when it is not there.
func crashPID(name string) int {
	base := strings.TrimSuffix(strings.TrimSuffix(name, crashPending), crashKept)
	if i := strings.LastIndex(base, "-"); i >= 0 {
		if pid, err := strconv.Atoi(base[i+1:]); err == nil {
			return pid
		}
	}
	return 0
}

// crashVersion reads the version line of a dump header.
func crashVersion(text string) string {
	for _, line := range strings.SplitN(text, "\n", 6) {
		if v, ok := strings.CutPrefix(line, "version: "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pruneCrashDumps keeps the MaxCrashDumps newest kept dumps (the names sort by time).
func pruneCrashDumps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var kept []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasPrefix(n, crashPrefix) && strings.HasSuffix(n, crashKept) {
			kept = append(kept, n)
		}
	}
	sort.Strings(kept)
	for len(kept) > MaxCrashDumps {
		_ = os.Remove(filepath.Join(dir, kept[0]))
		kept = kept[1:]
	}
}
