package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

func seedLog(t *testing.T, home, text string) {
	t.Helper()
	if err := os.MkdirAll(angel.RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(angel.LogPath(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

const logsSample = "" +
	"2026-10-24T10:00:00Z INFO start pid=1 listen=127.0.0.1:7890 route=direct\n" +
	"2026-10-24T10:05:00Z WARN subscription main: refresh failed: FETCH_FAILED\n" +
	"2026-10-24T10:06:00Z ERROR the previous configuration did not start either, stopping: x\n" +
	"2026-10-24T10:07:00Z INFO stop pid=1 reason=control\n"

func logsRun(t *testing.T, home string, args ...string) ([]map[string]any, error) {
	t.Helper()
	var out bytes.Buffer
	if err := runLogs(append(args, "--home", home), &out); err != nil {
		return nil, err
	}
	data := decodeData(t, &out)
	raw, ok := data["entries"].([]any)
	if !ok {
		t.Fatalf("entries = %#v", data["entries"])
	}
	items := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		items = append(items, r.(map[string]any))
	}
	return items, nil
}

func TestLogsShowsEntriesOldestFirst(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample)
	items, err := logsRun(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d entries: %v", len(items), items)
	}
	if items[0]["time"] != "2026-10-24T10:00:00Z" || items[0]["level"] != "INFO" ||
		items[0]["message"] != "start pid=1 listen=127.0.0.1:7890 route=direct" {
		t.Errorf("first = %v", items[0])
	}
	if items[3]["message"] != "stop pid=1 reason=control" {
		t.Errorf("last = %v", items[3])
	}
}

func TestLogsLevelFilter(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample)
	items, err := logsRun(t, home, "--level", "warn")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0]["level"] != "WARN" || items[1]["level"] != "ERROR" {
		t.Fatalf("items = %v", items)
	}
	items, err = logsRun(t, home, "--level=ERROR")
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %v err %v", items, err)
	}
}

func TestLogsTail(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample)
	items, err := logsRun(t, home, "--tail", "1")
	if err != nil || len(items) != 1 || items[0]["message"] != "stop pid=1 reason=control" {
		t.Fatalf("items = %v err %v", items, err)
	}
	items, err = logsRun(t, home, "--tail", "0")
	if err != nil || len(items) != 4 {
		t.Fatalf("tail 0: items = %v err %v", items, err)
	}
}

func TestLogsDefaultTailIsBounded(t *testing.T) {
	home := t.TempDir()
	var b strings.Builder
	for i := 0; i < defaultLogTail+25; i++ {
		b.WriteString("2026-10-24T10:00:00Z INFO line\n")
	}
	seedLog(t, home, b.String())
	items, err := logsRun(t, home)
	if err != nil || len(items) != defaultLogTail {
		t.Fatalf("got %d entries, err %v; want %d", len(items), err, defaultLogTail)
	}
}

func TestLogsSince(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	seedLog(t, home, old+" INFO old\n"+recent+" INFO recent\n")
	items, err := logsRun(t, home, "--since", "1h")
	if err != nil || len(items) != 1 || items[0]["message"] != "recent" {
		t.Fatalf("items = %v err %v", items, err)
	}
	items, err = logsRun(t, home, "--since", "7d")
	if err != nil || len(items) != 2 {
		t.Fatalf("7d: items = %v err %v", items, err)
	}
}

func TestLogsNoEntriesIsAnEmptyList(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, "")
	items, err := logsRun(t, home)
	if err != nil || len(items) != 0 {
		t.Fatalf("items = %v err %v", items, err)
	}
}

func TestLogsMissingLog(t *testing.T) {
	_, err := logsRun(t, t.TempDir())
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "LOG_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("err = %#v", err)
	}
}

func TestLogsUsageErrors(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample)
	for _, args := range [][]string{
		{"--level", "loud"},
		{"--since", "soon"},
		{"--since", "0s"},
		{"--tail", "-1"},
		{"--tail", "many"},
		{"extra"},
		{"--nope"},
	} {
		_, err := logsRun(t, home, args...)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
			t.Errorf("logs %v: err = %#v, want USAGE", args, err)
			continue
		}
		if !strings.HasPrefix(ce.Message, "logs:") {
			t.Errorf("logs %v: message %q does not name the command", args, ce.Message)
		}
	}
}

// syncBuffer is a writer the test can read while a command still writes to it.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{}
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{wake: make(chan struct{}, 1)} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor returns once the text contains want, or fails after the timeout.
func (b *syncBuffer) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(b.String(), want) {
		select {
		case <-b.wake:
		case <-deadline:
			t.Fatalf("output never contained %q: %q", want, b.String())
		}
	}
}

// followRun runs `logs --follow` until the returned stop function is called.
func followRun(t *testing.T, home string, out *syncBuffer, args ...string) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	prev := logsContext
	logsContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { logsContext = prev })
	done := make(chan error, 1)
	go func() { done <- runLogs(append([]string{"--follow"}, append(args, "--home", home)...), out) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("logs --follow did not stop")
			return nil
		}
	}
}

func appendLogFile(t *testing.T, home, text string) {
	t.Helper()
	f, err := os.OpenFile(angel.LogPath(home), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestLogsFollowPrintsBacklogThenNewLines(t *testing.T) {
	t.Setenv("NETIVRI_OUTPUT", "")
	home := t.TempDir()
	seedLog(t, home, logsSample)
	out := newSyncBuffer()
	stop := followRun(t, home, out, "--tail", "2", "--level", "info")
	out.waitFor(t, "stop pid=1 reason=control")
	appendLogFile(t, home, "2026-10-24T10:08:00Z WARN node main/a1b2c3 is down\n")
	out.waitFor(t, "2026-10-24T10:08:00Z WARN node main/a1b2c3 is down\n")
	if err := stop(); err != nil {
		t.Fatalf("follow returned %v", err)
	}
	got := out.String()
	if strings.Contains(got, "start pid=1") || strings.Contains(got, "refresh failed") {
		t.Errorf("--tail 2 showed older entries: %q", got)
	}
	if strings.Contains(got, "schema_version") || strings.Contains(got, "entries") {
		t.Errorf("a followed log must be a stream, not a document: %q", got)
	}
}

func TestLogsFollowJSONIsNDJSON(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, "2026-10-24T10:00:00Z INFO first\n")
	out := newSyncBuffer()
	stop := followRun(t, home, out, "--json")
	out.waitFor(t, "first")
	appendLogFile(t, home, "2026-10-24T10:01:00Z ERROR second\n")
	out.waitFor(t, "second")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), out.String())
	}
	for i, want := range []map[string]string{
		{"time": "2026-10-24T10:00:00Z", "level": "INFO", "message": "first"},
		{"time": "2026-10-24T10:01:00Z", "level": "ERROR", "message": "second"},
	} {
		var got map[string]string
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("line %d is not JSON: %q", i, lines[i])
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("line %d %s = %q, want %q", i, k, got[k], v)
			}
		}
	}
}

func TestLogsFollowWaitsForTheLog(t *testing.T) {
	home := t.TempDir()
	out := newSyncBuffer()
	stop := followRun(t, home, out)
	if err := os.MkdirAll(angel.RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	appendLogFile(t, home, "2026-10-24T10:00:00Z INFO the angel started\n")
	out.waitFor(t, "the angel started")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLogsIsRegisteredAndNotPlanned(t *testing.T) {
	if _, ok := commands["logs"]; !ok {
		t.Fatal("logs is not in the command table")
	}
	for _, p := range helpPlanned {
		if p == "logs" {
			t.Fatal("logs is still listed as planned")
		}
	}
}

func TestLogsHidesDebugUnlessAsked(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample+"2026-10-24T10:08:00Z DEBUG node own/x is down: 2 probe failures (timeout)\n")
	items, err := logsRun(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("default level shows %d entries, want 4 without the DEBUG one: %v", len(items), items)
	}
	items, err = logsRun(t, home, "--level", "debug")
	if err != nil || len(items) != 5 {
		t.Fatalf("--level debug: %d entries (err %v), want 5", len(items), err)
	}
}
