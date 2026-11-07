package angel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// crashSeams replaces the runtime seams and records what the dump asked of them.
func crashSeams(t *testing.T) (outputs *[]*os.File, tracebacks *[]string) {
	t.Helper()
	oldOut, oldTB := setCrashOutput, setTraceback
	t.Cleanup(func() { setCrashOutput, setTraceback = oldOut, oldTB })
	var outs []*os.File
	var tbs []string
	setCrashOutput = func(f *os.File) error { outs = append(outs, f); return nil }
	setTraceback = func(level string) { tbs = append(tbs, level) }
	return &outs, &tbs
}

func crashFiles(t *testing.T, home, suffix string) []string {
	t.Helper()
	entries, err := os.ReadDir(CrashDir(home))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestStartCrashDumpArmsAndCloseRemoves(t *testing.T) {
	outs, tbs := crashSeams(t)
	home := t.TempDir()
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	d, err := StartCrashDump(home, "1.2.3", at)
	if err != nil {
		t.Fatal(err)
	}
	if len(*outs) != 1 || (*outs)[0] == nil {
		t.Fatalf("crash output not armed: %v", *outs)
	}
	if len(*tbs) != 1 || (*tbs)[0] != "all" {
		t.Fatalf("traceback = %v, want all", *tbs)
	}
	files := crashFiles(t, home, crashPending)
	want := fmt.Sprintf("crash-20261107T120000Z-%d.pending", os.Getpid())
	if len(files) != 1 || files[0] != want {
		t.Fatalf("files = %v, want [%s]", files, want)
	}
	data, err := os.ReadFile(filepath.Join(CrashDir(home), files[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"started: 2026-11-07T12:00:00Z", fmt.Sprintf("pid: %d", os.Getpid()), "version: 1.2.3"} {
		if !strings.Contains(string(data), part) {
			t.Errorf("header lacks %q: %q", part, data)
		}
	}

	d.Close()
	if len(*outs) != 2 || (*outs)[1] != nil {
		t.Fatalf("Close did not disarm the crash output: %v", *outs)
	}
	if left := crashFiles(t, home, crashPending); len(left) != 0 {
		t.Fatalf("a clean stop left %v", left)
	}
	d.Close() // twice is harmless
}

func TestStartCrashDumpRefusedOutputLeavesNoFile(t *testing.T) {
	crashSeams(t)
	setCrashOutput = func(*os.File) error { return fmt.Errorf("no") }
	home := t.TempDir()
	if _, err := StartCrashDump(home, "v", time.Now()); err == nil {
		t.Fatal("want an error")
	}
	if left := crashFiles(t, home, crashPending); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

func writeCrashFile(t *testing.T, home, name, body string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(CrashDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(CrashDir(home), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestReportCrashesKeepsTracebackOnceAndRemovesEmpty(t *testing.T) {
	home := t.TempDir()
	at := time.Date(2026, 10, 29, 11, 7, 0, 0, time.UTC)
	header := "netivri angel crash dump\nstarted: 2026-10-29T11:06:53Z\npid: 20488\nversion: 0.9.0\n\n"
	writeCrashFile(t, home, "crash-20261029T110653Z-20488.pending",
		header+"panic: buffer overflow: capacity 32768,start 0, need 2\n\ngoroutine 841 [running]:\n", at)
	writeCrashFile(t, home, "crash-20261030T080000Z-777.pending",
		"netivri angel crash dump\nstarted: 2026-10-30T08:00:00Z\npid: 777\nversion: 0.9.1\n\n", at)

	rep := ReportCrashes(home)
	if len(rep.Dumps) != 1 || rep.Dumps[0].PID != 20488 || rep.Dumps[0].Version != "0.9.0" ||
		rep.Dumps[0].File != "crash-20261029T110653Z-20488.txt" || !rep.Dumps[0].At.Equal(at) {
		t.Fatalf("dumps = %+v", rep.Dumps)
	}
	if len(rep.Unclean) != 1 || rep.Unclean[0].PID != 777 || rep.Unclean[0].Version != "0.9.1" {
		t.Fatalf("unclean = %+v", rep.Unclean)
	}
	if got := crashFiles(t, home, crashPending); len(got) != 0 {
		t.Fatalf("pending files left: %v", got)
	}
	if got := crashFiles(t, home, crashKept); len(got) != 1 {
		t.Fatalf("kept files = %v", got)
	}

	again := ReportCrashes(home)
	if len(again.Dumps) != 0 || len(again.Unclean) != 0 {
		t.Fatalf("reported twice: %+v", again)
	}
}

func TestReportCrashesFatalErrorCounts(t *testing.T) {
	home := t.TempDir()
	writeCrashFile(t, home, "crash-20261031T000000Z-5.pending",
		"netivri angel crash dump\nstarted: x\npid: 5\nversion: v\n\nfatal error: all goroutines are asleep\n", time.Now())
	if rep := ReportCrashes(home); len(rep.Dumps) != 1 {
		t.Fatalf("dumps = %+v", rep.Dumps)
	}
}

func TestReportCrashesNoDirectory(t *testing.T) {
	rep := ReportCrashes(t.TempDir())
	if len(rep.Dumps) != 0 || len(rep.Unclean) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestReportCrashesPrunesOldKeptDumps(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < MaxCrashDumps+3; i++ {
		writeCrashFile(t, home, fmt.Sprintf("crash-202611%02dT000000Z-%d.txt", i+1, i+1), "panic: x\n", time.Now())
	}
	ReportCrashes(home)
	got := crashFiles(t, home, crashKept)
	if len(got) != MaxCrashDumps {
		t.Fatalf("kept %d dumps: %v", len(got), got)
	}
	if got[0] != "crash-20261104T000000Z-4.txt" {
		t.Fatalf("the oldest kept = %s, want the 4th", got[0])
	}
}
