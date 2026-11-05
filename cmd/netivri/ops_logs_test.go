package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestOpsLogsQueryDefaults(t *testing.T) {
	q, err := opsLogsQuery("", "", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Tail != 7 || q.MinLevel != angel.LevelInfo || !q.Since.IsZero() {
		t.Fatalf("q = %+v", q)
	}
}

func TestOpsLogsQueryLevel(t *testing.T) {
	q, err := opsLogsQuery("warn", "", 0, nil)
	if err != nil || q.MinLevel != angel.LevelWarn {
		t.Fatalf("q = %+v err %v", q, err)
	}
	q, err = opsLogsQuery("ERROR", "", 0, nil)
	if err != nil || q.MinLevel != angel.LevelError {
		t.Fatalf("q = %+v err %v", q, err)
	}
	_, err = opsLogsQuery("loud", "", 0, nil)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" || ce.Exit != cli.ExitUsage ||
		ce.Message != "logs: --level must be debug, info, warn or error" {
		t.Fatalf("err = %#v", err)
	}
}

func TestOpsLogsQuerySince(t *testing.T) {
	now := time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC)
	q, err := opsLogsQuery("", "30m", 0, func() time.Time { return now })
	if err != nil || !q.Since.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("q = %+v err %v", q, err)
	}
	_, err = opsLogsQuery("", "soon", 0, func() time.Time { return now })
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" || ce.Exit != cli.ExitUsage ||
		!strings.HasPrefix(ce.Message, "logs: --since must be a duration") {
		t.Fatalf("err = %#v", err)
	}
	q, err = opsLogsQuery("", "", 0, func() time.Time { return now })
	if err != nil || !q.Since.IsZero() {
		t.Fatalf("q = %+v err %v", q, err)
	}
}

func TestOpsLogsQueryUsesClockWhenNowNil(t *testing.T) {
	q, err := opsLogsQuery("", "1h", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Since.After(time.Now()) {
		t.Fatalf("since = %v is in the future", q.Since)
	}
}

func TestOpsLogsReadMatchesTheCommand(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, logsSample)
	q, err := opsLogsQuery("", "", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, offset, err := opsLogsRead(home, q)
	if err != nil || len(entries) != 4 || offset <= 0 {
		t.Fatalf("%d entries, offset %d, err %v", len(entries), offset, err)
	}
	if entries[0].Message != "start pid=1 listen=127.0.0.1:7890 route=direct" ||
		entries[3].Message != "stop pid=1 reason=control" {
		t.Fatalf("entries = %+v", entries)
	}
	q1, _ := opsLogsQuery("", "", 1, nil)
	entries, _, err = opsLogsRead(home, q1)
	if err != nil || len(entries) != 1 || entries[0].Message != "stop pid=1 reason=control" {
		t.Fatalf("tail 1: %d entries, err %v", len(entries), err)
	}
	qw, _ := opsLogsQuery("warn", "", 100, nil)
	entries, _, err = opsLogsRead(home, qw)
	if err != nil || len(entries) != 2 || entries[0].Level != angel.LevelWarn || entries[1].Level != angel.LevelError {
		t.Fatalf("warn: %d entries, err %v", len(entries), err)
	}
}

func TestOpsLogsReadSince(t *testing.T) {
	home := t.TempDir()
	seedLog(t, home, "2026-10-22T10:00:00Z INFO old\n2026-10-24T11:30:00Z INFO recent\n")
	fixedNow := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	q, err := opsLogsQuery("", "1h", 100, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	entries, _, err := opsLogsRead(home, q)
	if err != nil || len(entries) != 1 || entries[0].Message != "recent" {
		t.Fatalf("%d entries, err %v", len(entries), err)
	}
}

func TestOpsLogsReadMissingLogIsErrNotExist(t *testing.T) {
	entries, offset, err := opsLogsRead(t.TempDir(), angel.LogQuery{})
	if !errors.Is(err, os.ErrNotExist) || entries != nil || offset != 0 {
		t.Fatalf("entries = %v offset = %d err = %v", entries, offset, err)
	}
}

func TestOpsLogsReadKeepsPartialLastLine(t *testing.T) {
	home := t.TempDir()
	text := "2026-10-24T10:00:00Z INFO complete\n2026-10-24T10:01:00Z INFO partial-no-newline"
	seedLog(t, home, text)
	q, _ := opsLogsQuery("", "", 100, nil)
	entries, offset, err := opsLogsRead(home, q)
	if err != nil || len(entries) != 2 || entries[1].Message != "partial-no-newline" {
		t.Fatalf("%d entries, err %v", len(entries), err)
	}
	info, err := os.Stat(angel.LogPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if offset != info.Size() {
		t.Fatalf("offset = %d, want %d", offset, info.Size())
	}
}
