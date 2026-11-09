package testutil

import (
	"fmt"
	"strings"
	"testing"
)

// fakeT records what LogOnFail does, without failing the real test.
type fakeT struct {
	failed   bool
	cleanups []func()
	logs     []string
}

func (f *fakeT) Helper()      {}
func (f *fakeT) Failed() bool { return f.failed }
func (f *fakeT) Cleanup(fn func()) {
	f.cleanups = append(f.cleanups, fn)
}
func (f *fakeT) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

// finish runs the cleanups last-in first-out, as testing does.
func (f *fakeT) finish() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func TestLogOnFailSilentWhenTheTestPassed(t *testing.T) {
	f := &fakeT{}
	called := false
	LogOnFail(f, "diag", func() string { called = true; return "detail" })
	f.finish()
	if called || len(f.logs) != 0 {
		t.Fatalf("a passing test produced a report: called=%v logs=%q", called, f.logs)
	}
}

func TestLogOnFailReportsWhenTheTestFailed(t *testing.T) {
	f := &fakeT{}
	LogOnFail(f, "angel diagnostics", func() string { return "stderr tail: boom" })
	f.failed = true
	f.finish()
	if len(f.logs) != 1 {
		t.Fatalf("logs = %q, want one report", f.logs)
	}
	for _, want := range []string{"angel diagnostics", "shown because the test failed", "stderr tail: boom"} {
		if !strings.Contains(f.logs[0], want) {
			t.Errorf("report %q lacks %q", f.logs[0], want)
		}
	}
}

func TestLogOnFailReadsStateBeforeEarlierCleanups(t *testing.T) {
	f := &fakeT{}
	alive := true
	f.Cleanup(func() { alive = false }) // the "stop" registered first
	var seen bool
	LogOnFail(f, "diag", func() string { seen = alive; return "x" })
	f.failed = true
	f.finish()
	if !seen {
		t.Fatal("the report ran after the earlier cleanup had stopped what it reads")
	}
}

func TestLogOnFailSurvivesAPanickingReport(t *testing.T) {
	f := &fakeT{}
	LogOnFail(f, "diag", func() string { panic("bad report") })
	f.failed = true
	f.finish()
	if len(f.logs) != 1 || !strings.Contains(f.logs[0], "report panicked: bad report") {
		t.Fatalf("logs = %q, want the panic named in the report", f.logs)
	}
}
