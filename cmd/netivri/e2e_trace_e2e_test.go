//go:build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// A trace is a log that costs nothing on a green run: a test that opts in with
// e2eTraceOn collects timed events (e2eMark, and the start and the exit of every
// angel startUp spawns) and the log shows them, with facts about the host, only
// when the test failed. It exists for failures that name no cause in their own
// message, such as an angel that disappeared in the middle of a test.

type e2eTraceEvent struct {
	at   time.Time
	text string
}

type e2eTrace struct {
	name    string
	start   time.Time
	mu      sync.Mutex
	events  []e2eTraceEvent
	dropped int
}

// e2eTraceMaxEvents bounds a trace; the later events are counted, not kept.
const e2eTraceMaxEvents = 400

// e2eTraces maps a running test to its trace.
var e2eTraces sync.Map // *testing.T -> *e2eTrace

// e2eTraceOn starts the trace of t and logs it when t fails. Call it first in
// the test: its report is logged after the reports of the angels the test
// started.
func e2eTraceOn(t *testing.T) *e2eTrace {
	t.Helper()
	tr := &e2eTrace{name: t.Name(), start: time.Now()}
	e2eTraces.Store(t, tr)
	t.Cleanup(func() { e2eTraces.Delete(t) })
	testutil.LogOnFail(t, "trace of "+t.Name(), func() string { return tr.report(e2eHostFacts()) })
	return tr
}

// e2eMark adds an event to the trace of t; it does nothing for a test without
// one, so shared helpers can call it freely.
func e2eMark(t *testing.T, format string, args ...any) {
	if v, ok := e2eTraces.Load(t); ok {
		if tr, isTrace := v.(*e2eTrace); isTrace {
			tr.add(fmt.Sprintf(format, args...))
		}
	}
}

func (tr *e2eTrace) add(text string) {
	text = e2eRedactCrashLine(strings.ReplaceAll(strings.TrimSpace(text), "\n", " | "))
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.events) >= e2eTraceMaxEvents {
		tr.dropped++
		return
	}
	tr.events = append(tr.events, e2eTraceEvent{at: time.Now(), text: text})
}

// report renders the events with their time since the start of the test, then
// facts (the host description, may be empty).
func (tr *e2eTrace) report(facts string) string {
	tr.mu.Lock()
	events := append([]e2eTraceEvent(nil), tr.events...)
	dropped := tr.dropped
	tr.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "%d events in %s", len(events)+dropped, time.Since(tr.start).Round(time.Millisecond))
	for _, ev := range events {
		fmt.Fprintf(&b, "\n+%8.3fs  %s", ev.at.Sub(tr.start).Seconds(), ev.text)
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n... %d later events not kept", dropped)
	}
	if facts != "" {
		b.WriteString("\n" + facts)
	}
	return b.String()
}

// e2eHostFacts describes the machine the test ran on. On Windows it also asks
// for the memory left and for the system events of the last 30 minutes that
// name the program (a fault of the process) or a resource exhaustion, which a
// Go program that is gone leaves nowhere else. It runs only on a failure, within
// a time limit, and never fails the test.
func e2eHostFacts() string {
	var b strings.Builder
	fmt.Fprintf(&b, "host: %s/%s, %s, NumCPU=%d, GOMAXPROCS=%d", runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU(), runtime.GOMAXPROCS(0))
	for _, key := range []string{"RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion"} {
		if v := os.Getenv(key); v != "" {
			fmt.Fprintf(&b, ", %s=%s", key, v)
		}
	}
	if runtime.GOOS == "windows" {
		b.WriteString("\n" + e2eWindowsFacts())
	}
	return b.String()
}

const e2eWindowsFactsScript = `$ErrorActionPreference = 'SilentlyContinue'
$os = Get-CimInstance Win32_OperatingSystem
'memory KB: free physical ' + $os.FreePhysicalMemory + ' of ' + $os.TotalVisibleMemorySize + ', free virtual ' + $os.FreeVirtualMemory + ' of ' + $os.TotalVirtualMemorySize
$since = (Get-Date).AddMinutes(-30)
$queries = @(
  @{LogName='Application'; StartTime=$since},
  @{LogName='System'; ProviderName='Microsoft-Windows-Resource-Exhaustion-Detector'; StartTime=$since}
)
foreach ($q in $queries) {
  $events = Get-WinEvent -FilterHashtable $q -MaxEvents 300 | Where-Object { $q.LogName -eq 'System' -or $_.Message -match 'netivri' } | Select-Object -First 8
  foreach ($e in $events) {
    $m = ($e.Message -replace '\s+', ' ')
    $e.TimeCreated.ToString('o') + ' ' + $e.LogName + ' ' + $e.ProviderName + ' id ' + $e.Id + ': ' + $m.Substring(0, [Math]::Min(400, $m.Length))
  }
}`

func e2eWindowsFacts() string {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", e2eWindowsFactsScript).CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 40 {
		lines = lines[:40]
	}
	for i, line := range lines {
		lines[i] = e2eRedactCrashLine(strings.TrimRight(line, "\r"))
	}
	text := strings.Join(lines, "\n")
	if err != nil {
		return fmt.Sprintf("windows facts unavailable (%v):\n%s", err, text)
	}
	return "windows facts:\n" + text
}

// e2eRedactStderrLine redacts a line of an angel's stderr. The lines of a Go
// stack (function lines with their arguments, "\tfile:line" lines, goroutine
// headers) keep their module paths, which hold an "@" that
// e2eRedactDiagnostic would hide together with the whole line; other lines
// get the strict redaction.
func e2eRedactStderrLine(line string) string {
	if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "goroutine ") || strings.HasPrefix(line, "created by ") ||
		strings.Contains(line, " fp=0x") || strings.Contains(line, "(0x") || strings.HasSuffix(line, "(...)") || strings.HasSuffix(line, "({})") {
		return e2eRedactCrashLine(line)
	}
	return e2eRedactDiagnostic(line)
}

// e2eHeadTail returns the first head and the last tail non-empty redacted lines
// of text, and says how many lie between them.
func e2eHeadTail(name, text string, head, tail int) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, e2eRedactStderrLine(line))
	}
	if len(kept) <= head+tail {
		return fmt.Sprintf("%s (%d lines):\n%s", name, len(kept), strings.Join(kept, "\n"))
	}
	shown := append([]string{}, kept[:head]...)
	shown = append(shown, fmt.Sprintf("... %d lines skipped ...", len(kept)-head-tail))
	shown = append(shown, kept[len(kept)-tail:]...)
	return fmt.Sprintf("%s (first %d and last %d of %d lines):\n%s", name, head, tail, len(kept), strings.Join(shown, "\n"))
}

func TestE2ETraceReport(t *testing.T) {
	tr := &e2eTrace{name: "x", start: time.Now().Add(-5 * time.Second)}
	tr.add("first\nsecond line")
	tr.add("dial https://sub.example.invalid/feed?token=abc failed")
	tr.add("user password hunter2")
	got := tr.report("host: test")
	for _, want := range []string{"3 events in ", "first | second line", "[redacted]", "host: test"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"sub.example.invalid", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Errorf("report keeps %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "+   5.0") {
		t.Errorf("events are not timed from the start of the test:\n%s", got)
	}
	for range e2eTraceMaxEvents - 3 + 5 { // three events are in already: five are over the limit
		tr.add("more")
	}
	if got := tr.report(""); !strings.Contains(got, "5 later events not kept") || strings.Count(got, "\n") != e2eTraceMaxEvents+1 {
		t.Errorf("an overfull trace is not bounded:\n%d line breaks", strings.Count(got, "\n"))
	}
}

func TestE2EMarkWithoutTraceDoesNothing(t *testing.T) {
	e2eMark(t, "nobody listens %d", 1) // must not panic
	tr := e2eTraceOn(t)
	e2eMark(t, "heard %d", 2)
	if got := tr.report(""); !strings.Contains(got, "heard 2") {
		t.Errorf("mark missing from the trace:\n%s", got)
	}
}

func TestE2EHeadTail(t *testing.T) {
	lines := make([]string, 0, 50)
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := e2eHeadTail("log", strings.Join(lines, "\n"), 3, 4)
	for _, want := range []string{"first 3 and last 4 of 50 lines", "line 0\nline 1\nline 2\n... 43 lines skipped ...\nline 46\nline 47\nline 48\nline 49"} {
		if !strings.Contains(got, want) {
			t.Errorf("head/tail lacks %q:\n%s", want, got)
		}
	}
	short := e2eHeadTail("log", "a\n\n b \r\n", 3, 4)
	if short != "log (2 lines):\na\n b " {
		t.Errorf("short text: %q", short)
	}
}

func TestE2ERedactStderrLineKeepsStackFrames(t *testing.T) {
	for _, line := range []string{
		"\tgithub.com/metacubex/http@v1.2.3/transport.go:10 +0x1 fp=0x2 sp=0x3 pc=0x4",
		"github.com/metacubex/mihomo/common/net.(*refConn).Read(0x0?, {0x10baa8a1f000?, 0x7ff79a6c228a?, 0x0?})",
		"goroutine 91 gp=0x10baa8c885a0 m=8 mp=0x10baa8950808 [syscall]:",
		"created by main.(*liveAngel).startLoops in goroutine 1",
		"runtime.goexit({})",
	} {
		if got := e2eRedactStderrLine(line); got != line {
			t.Errorf("stack line changed: %q -> %q", line, got)
		}
	}
	for _, line := range []string{
		"2026-11-09T21:32:22Z WARN dial https://sub.example.invalid/x failed",
		"2026-11-09T21:32:22Z INFO account alice@example.invalid logged in",
	} {
		if got := e2eRedactStderrLine(line); strings.Contains(got, "example.invalid") {
			t.Errorf("log line kept a link or an address: %q", got)
		}
	}
	if got := e2eRedactStderrLine("\tpassword=hunter2 at x.go:1"); got != "[redacted]" {
		t.Errorf("a stack line naming a password stayed: %q", got)
	}
}
