//go:build e2e

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

type e2eLogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func e2eLogEntries(t *testing.T, stdout string) []e2eLogEntry {
	t.Helper()
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Entries []e2eLogEntry `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || !env.OK {
		t.Fatalf("decode logs output %q: %v", stdout, err)
	}
	return env.Data.Entries
}

// TestE2ELogsOfTheDaemon runs a real daemon and reads its log with `logs`:
// the start line while it runs, the stop line after `down`, the level filter
// and the missing-log error of an untouched home.
func TestE2ELogsOfTheDaemon(t *testing.T) {
	code, _, stderr := daemonRun(t, t.TempDir(), "logs")
	if code != cli.ExitNotFound || decodeErrEnvelope(t, stderr).Error.Code != "LOG_NOT_FOUND" {
		t.Fatalf("logs of an empty home: exit %d, stderr %q", code, stderr)
	}

	h := newDaemonHome(t)
	code, stdout, stderr := daemonRun(t, h.dir, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	h.pid = daemonInt(t, daemonData(t, stdout), "pid")

	code, stdout, stderr = daemonRun(t, h.dir, "logs")
	if code != 0 {
		t.Fatalf("logs exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	entries := e2eLogEntries(t, stdout)
	var started bool
	for _, e := range entries {
		if e.Level == "INFO" && strings.HasPrefix(e.Message, "start pid=") && e.Time != "" {
			started = true
		}
	}
	if !started {
		t.Fatalf("no start line in %+v", entries)
	}

	if code, stdout, stderr = daemonRun(t, h.dir, "down"); code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	code, stdout, stderr = daemonRun(t, h.dir, "logs", "--level", "info", "--tail", "1")
	if code != 0 {
		t.Fatalf("logs after down: exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	entries = e2eLogEntries(t, stdout)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Message, "stop pid=") || !strings.Contains(entries[0].Message, "reason=control") {
		t.Fatalf("last entry after down = %+v", entries)
	}

	code, stdout, stderr = daemonRun(t, h.dir, "logs", "--level", "warn")
	if code != 0 {
		t.Fatalf("logs --level warn: exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	for _, e := range e2eLogEntries(t, stdout) {
		if e.Level != "WARN" && e.Level != "ERROR" {
			t.Errorf("an entry below warn came through: %+v", e)
		}
	}
}
