package testutil

import "fmt"

// FailReporter is the part of testing.TB that LogOnFail needs; *testing.T and
// *testing.B satisfy it.
type FailReporter interface {
	Helper()
	Failed() bool
	Cleanup(func())
	Logf(format string, args ...any)
}

// LogOnFail registers a cleanup that logs report() under title only when the
// test has failed, so detailed diagnostics (process output, logs, state dumps)
// cost nothing on a green run. Cleanups run last-in first-out: register it
// after the cleanup that stops what report() reads, so the report runs while
// that is still alive. A panic in report is logged, not propagated.
func LogOnFail(t FailReporter, title string, report func() string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		t.Logf("%s (shown because the test failed):\n%s", title, safeReport(report))
	})
}

func safeReport(report func() string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("report panicked: %v", r)
		}
	}()
	return report()
}
