package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, home, text string) {
	t.Helper()
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LogPath(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLog(t *testing.T, home, text string) {
	t.Helper()
	f, err := os.OpenFile(LogPath(home), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestFormatParseLogLineRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 30, 5, 0, time.UTC)
	line := FormatLogLine(at, LevelWarn, "node main/a1b2c3 is down")
	if line != "2026-10-24T12:30:05Z WARN node main/a1b2c3 is down" {
		t.Fatalf("line = %q", line)
	}
	e, ok := ParseLogLine(line)
	if !ok || !e.Time.Equal(at) || e.Level != LevelWarn || e.Message != "node main/a1b2c3 is down" {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestFormatLogLineKeepsOneLineAndUTC(t *testing.T) {
	zone := time.FixedZone("x", 3*3600)
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, zone)
	line := FormatLogLine(at, LevelError, "a\nb\r\nc")
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("line has a line break: %q", line)
	}
	if !strings.HasPrefix(line, "2026-10-24T12:00:00Z ERROR ") {
		t.Fatalf("line = %q", line)
	}
}

func TestParseLogLineLegacy(t *testing.T) {
	e, ok := ParseLogLine("netivri-daemon 2026/10/24 12:30:05 start pid=1 listen=127.0.0.1:7890 route=direct")
	if !ok {
		t.Fatal("legacy line not parsed")
	}
	want := time.Date(2026, 10, 24, 12, 30, 5, 0, time.UTC)
	if !e.Time.Equal(want) || e.Level != LevelInfo || e.Message != "start pid=1 listen=127.0.0.1:7890 route=direct" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestParseLogLineRejectsOther(t *testing.T) {
	for _, line := range []string{
		"",
		"panic: boom",
		"goroutine 1 [running]:",
		"2026-10-24T12:00:00Z",
		"2026-10-24T12:00:00Z LOUD message",
		"not-a-time INFO message",
		"netivri-daemon garbage",
		"netivri-daemon 2026/10/24",
	} {
		if e, ok := ParseLogLine(line); ok {
			t.Errorf("ParseLogLine(%q) = %+v, want no entry", line, e)
		}
	}
}

func TestParseLogLineEmptyMessage(t *testing.T) {
	e, ok := ParseLogLine("2026-10-24T12:00:00Z INFO")
	if !ok || e.Level != LevelInfo || e.Message != "" {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestNormalizeLevel(t *testing.T) {
	cases := map[string]string{"debug": LevelDebug, "INFO": LevelInfo, " Warn ": LevelWarn, "warning": LevelWarn, "error": LevelError}
	for in, want := range cases {
		got, ok := NormalizeLevel(in)
		if !ok || got != want {
			t.Errorf("NormalizeLevel(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "fatal", "trace", "5"} {
		if _, ok := NormalizeLevel(bad); ok {
			t.Errorf("NormalizeLevel(%q) accepted", bad)
		}
	}
}

func TestLevelRankOrder(t *testing.T) {
	order := []string{LevelDebug, LevelInfo, LevelWarn, LevelError}
	for i := 1; i < len(order); i++ {
		if LevelRank(order[i-1]) >= LevelRank(order[i]) {
			t.Errorf("%s is not below %s", order[i-1], order[i])
		}
	}
	if LevelRank("nope") != -1 {
		t.Error("an unknown level must rank -1")
	}
}

const sampleLog = "" +
	"netivri-daemon 2026/10/24 10:00:00 start pid=1 listen=127.0.0.1:7890 route=direct\n" +
	"2026-10-24T11:00:00Z INFO configuration applied: routes=1 accounts=2\n" +
	"2026-10-24T11:30:00Z WARN subscription main: refresh failed: FETCH_FAILED\n" +
	"2026-10-24T11:31:00Z ERROR the previous configuration did not start either, stopping: x\n" +
	"panic: boom\n" +
	"goroutine 1 [running]:\n" +
	"2026-10-24T12:00:00Z INFO stop pid=1 reason=control\n"

func TestReadLogAll(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, sampleLog)
	got, end, err := ReadLog(home, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d entries, want 7: %+v", len(got), got)
	}
	if end != int64(len(sampleLog)) {
		t.Errorf("end = %d, want %d", end, len(sampleLog))
	}
	if got[0].Level != LevelInfo || !strings.HasPrefix(got[0].Message, "start pid=1") {
		t.Errorf("first = %+v", got[0])
	}
}

func TestReadLogLevelFilter(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, sampleLog)
	got, _, err := ReadLog(home, LogQuery{MinLevel: LevelWarn})
	if err != nil {
		t.Fatal(err)
	}
	// WARN, ERROR and the two trace lines that belong to the ERROR entry.
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(got), got)
	}
	for _, e := range got {
		if LevelRank(e.Level) < LevelRank(LevelWarn) {
			t.Errorf("entry below warn: %+v", e)
		}
	}
	if got[2].Message != "panic: boom" || got[2].Level != LevelError || got[2].Time.IsZero() {
		t.Errorf("trace line = %+v, want it to continue the ERROR entry", got[2])
	}
	if got[0].Level != LevelWarn {
		t.Errorf("first = %+v", got[0])
	}
}

func TestReadLogSince(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, sampleLog)
	since := time.Date(2026, 10, 24, 11, 30, 0, 0, time.UTC)
	got, _, err := ReadLog(home, LogQuery{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	// 11:30 WARN, 11:31 ERROR, its two trace lines, 12:00 INFO.
	if len(got) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(got), got)
	}
	if got[0].Level != LevelWarn {
		t.Errorf("first = %+v", got[0])
	}
}

func TestReadLogSinceDropsLinesWithoutTime(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "stray line before any entry\n2026-10-24T12:00:00Z INFO ok\n")
	got, _, err := ReadLog(home, LogQuery{Since: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message != "ok" {
		t.Fatalf("got %+v", got)
	}
	all, _, err := ReadLog(home, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Level != LevelInfo || !all[0].Time.IsZero() {
		t.Fatalf("all = %+v", all)
	}
}

func TestReadLogTail(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, sampleLog)
	got, _, err := ReadLog(home, LogQuery{Tail: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Message != "stop pid=1 reason=control" || got[0].Message != "goroutine 1 [running]:" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadLogMissing(t *testing.T) {
	if _, _, err := ReadLog(t.TempDir(), LogQuery{}); !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestReadLogEmptyFile(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "")
	got, end, err := ReadLog(home, LogQuery{})
	if err != nil || len(got) != 0 || end != 0 {
		t.Fatalf("got %+v end %d err %v", got, end, err)
	}
}

func TestReadLogLastLineWithoutNewline(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T12:00:00Z INFO one\n2026-10-24T12:00:01Z INFO two")
	got, _, err := ReadLog(home, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Message != "two" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadLogOnlyTheTail(t *testing.T) {
	home := t.TempDir()
	line := "2026-10-24T12:00:00Z INFO " + strings.Repeat("x", 1000) + "\n"
	var b strings.Builder
	for b.Len() < logTailBytes+10*len(line) {
		b.WriteString(line)
	}
	writeLog(t, home, b.String())
	got, end, err := ReadLog(home, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(b.Len()) {
		t.Errorf("end = %d, want %d", end, b.Len())
	}
	if len(got)*len(line) > logTailBytes {
		t.Errorf("read %d bytes of entries, more than the %d byte window", len(got)*len(line), logTailBytes)
	}
	// The line cut by the window is dropped: every entry is whole.
	for _, e := range got {
		if len(e.Message) != 1000 {
			t.Fatalf("a cut entry came through: %d bytes", len(e.Message))
		}
	}
}

func TestReadLogRangeFollowTakesCompleteLinesOnly(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T12:00:00Z INFO one\n2026-10-24T12:00:01Z INFO tw")
	first := int64(len("2026-10-24T12:00:00Z INFO one\n"))
	data, end, err := readLogRange(LogPath(home), 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "2026-10-24T12:00:00Z INFO one\n" || end != first {
		t.Fatalf("data %q end %d", data, end)
	}
	// Nothing complete yet after the offset.
	data, end, err = readLogRange(LogPath(home), first)
	if err != nil || len(data) != 0 || end != first {
		t.Fatalf("data %q end %d err %v", data, end, err)
	}
	appendLog(t, home, "o\n")
	data, _, err = readLogRange(LogPath(home), first)
	if err != nil || string(data) != "2026-10-24T12:00:01Z INFO two\n" {
		t.Fatalf("data %q err %v", data, err)
	}
}

func TestReadLogRangeRestartsAfterShrink(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T12:00:00Z INFO fresh\n")
	data, _, err := readLogRange(LogPath(home), 9999)
	if err != nil || string(data) != "2026-10-24T12:00:00Z INFO fresh\n" {
		t.Fatalf("data %q err %v", data, err)
	}
}

func TestFollowLogEmitsNewEntries(t *testing.T) {
	home := t.TempDir()
	writeLog(t, home, "2026-10-24T12:00:00Z INFO old\n")
	_, offset, err := ReadLog(home, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan LogEntry, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		FollowLog(ctx, home, offset, LogQuery{MinLevel: LevelWarn}, func(e LogEntry) { got <- e })
	}()
	appendLog(t, home, "2026-10-24T12:00:01Z INFO skipped\n2026-10-24T12:00:02Z WARN seen\n")
	select {
	case e := <-got:
		if e.Message != "seen" || e.Level != LevelWarn {
			t.Fatalf("entry = %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no entry followed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FollowLog did not stop")
	}
	select {
	case e := <-got:
		t.Fatalf("unexpected extra entry %+v", e)
	default:
	}
}

func TestFollowLogWaitsForMissingLog(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan LogEntry, 4)
	go FollowLog(ctx, home, 0, LogQuery{}, func(e LogEntry) { got <- e })
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	appendLog(t, home, "2026-10-24T12:00:00Z INFO late\n")
	select {
	case e := <-got:
		if e.Message != "late" {
			t.Fatalf("entry = %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the log that appeared later was not followed")
	}
}

func TestRotateLog(t *testing.T) {
	home := t.TempDir()
	if err := RotateLog(home, 10); err != nil {
		t.Fatalf("missing log: %v", err)
	}
	writeLog(t, home, "0123456789")
	if err := RotateLog(home, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LogPath(home)); err != nil {
		t.Fatal("a log at the limit must stay")
	}
	writeLog(t, home, "0123456789A")
	if err := os.WriteFile(LogPath(home)+".1", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RotateLog(home, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LogPath(home)); !os.IsNotExist(err) {
		t.Fatalf("the log was not moved: %v", err)
	}
	moved, err := os.ReadFile(LogPath(home) + ".1")
	if err != nil || string(moved) != "0123456789A" {
		t.Fatalf("netivri.log.1 = %q, %v", moved, err)
	}
}
