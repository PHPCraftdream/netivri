//go:build e2e

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestE2EStatusWatchNDJSON runs a real angel, starts `status --watch
// --interval 1s --json` as a subprocess, decodes two NDJSON frames (read
// with a bounded deadline) and kills the subprocess by its own PID, then
// waits for it with a deadline.
func TestE2EStatusWatchNDJSON(t *testing.T) {
	home := newAngelHome(t)
	up := startUp(t, home.dir, "--port", "0")
	assertStatusSuccess(t, up.Status())

	cmd := exec.Command(netivriBin, "status", "--watch", "--interval", "1s", "--json")
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home.dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	killed := false
	kill := func() {
		if killed {
			return
		}
		killed = true
		// Kill by the PID the test itself started, never by name.
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	// cmd.Wait delivers exactly one result, so the exit wait is
	// idempotent: the first caller consumes it and stores it, later
	// callers (the cleanup after the test body already waited) only read
	// the stored result.
	var exitOnce sync.Once
	var exitErr error
	exitDone := make(chan struct{})
	waitForExit := func(what string) {
		t.Helper()
		exitOnce.Do(func() {
			select {
			case exitErr = <-waitCh:
				close(exitDone)
			case <-time.After(e2eKillGrace):
				t.Errorf("%s: the watcher did not exit within %v", what, e2eKillGrace)
			}
		})
		select {
		case <-exitDone:
		case <-time.After(e2eKillGrace):
			t.Errorf("%s: the watcher did not exit within %v", what, e2eKillGrace)
		}
	}
	t.Cleanup(func() {
		kill()
		waitForExit("cleanup")
		_ = exitErr
	})

	// One reader for both lines: a fresh bufio.Reader per line could
	// read ahead past its newline and swallow the next frame.
	br := bufio.NewReader(stdout)
	// readLine returns one stdout line or fails the test on the deadline.
	readLine := func(n int) string {
		t.Helper()
		type readRes struct {
			line string
			err  error
		}
		ch := make(chan readRes, 1)
		go func() {
			line, err := br.ReadString('\n')
			ch <- readRes{line, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("read frame %d: %v", n, r.err)
			}
			return r.line
		case <-time.After(e2eFirstLineTimeout):
			t.Fatalf("no frame %d within %v", n, e2eFirstLineTimeout)
		}
		return ""
	}
	for n := 1; n <= 2; n++ {
		line := readLine(n)
		if strings.TrimSpace(line) == "" {
			t.Fatalf("frame %d is an empty line", n)
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame %d is not one JSON object: %v (%q)", n, err, line)
		}
		for _, key := range []string{"time", "active_accounts", "accounts"} {
			if _, ok := frame[key]; !ok {
				t.Errorf("frame %d has no %q: %q", n, key, line)
			}
		}
	}
	kill()
	waitForExit("after the frames")
}
