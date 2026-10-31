//go:build e2e

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// localExec runs the binary in dir to completion. NETIVRI_HOME points at an
// unrelated directory, to show that the directory of the run wins over it.
func localExec(dir, global string, args ...string) (code int, stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), angelE2ETimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+global)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return 0, outBuf.String(), errBuf.String(), errors.New("command timed out")
	}
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee):
		code = ee.ExitCode()
	case runErr != nil:
		err = runErr
	}
	return code, outBuf.String(), errBuf.String(), err
}

func localRun(t *testing.T, dir, global string, args ...string) (int, string, string) {
	t.Helper()
	code, stdout, stderr, err := localExec(dir, global, args...)
	if err != nil {
		t.Fatalf("netivri %v in %s: %v (stdout %q, stderr %q)", args, dir, err, stdout, stderr)
	}
	return code, stdout, stderr
}

// stopLocalAngel stops the angel of dir's ./.netivri at the end of a test, and
// kills it by the pid in its state file when `down` did not.
func stopLocalAngel(t *testing.T, dir, global string) {
	t.Cleanup(func() {
		_, _, _, _ = localExec(dir, global, "down")
		if st, err := angelReadState(filepath.Join(dir, ".netivri")); err == nil && st.PID > 0 {
			if p, ferr := os.FindProcess(st.PID); ferr == nil {
				_ = p.Kill()
			}
		}
	})
}

func sameDirectory(t *testing.T, got any, want string) {
	t.Helper()
	path, ok := got.(string)
	if !ok {
		t.Fatalf("home = %v, want a path", got)
	}
	a, errA := os.Stat(path)
	b, errB := os.Stat(want)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Errorf("home = %q, want the directory %q", path, want)
	}
}

// Two directories, two angels: each one keeps its settings, state and angel in
// its own ./.netivri, which is found without a flag once it exists, and none of
// it reaches the directory that NETIVRI_HOME names.
func TestE2ELocalHomesAreIsolated(t *testing.T) {
	global := t.TempDir()
	a, b := t.TempDir(), t.TempDir()
	stopLocalAngel(t, a, global)
	stopLocalAngel(t, b, global)

	code, out, errOut := localRun(t, a, global, "up", "--local", "--port", "0")
	if code != 0 {
		t.Fatalf("up --local in a: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	upA := angelData(t, out)
	homeA := filepath.Join(a, ".netivri")
	if !angelStateExists(homeA) {
		t.Fatalf("no angel state in %s after up --local", homeA)
	}
	if angelStateExists(global) {
		t.Error("the state of a --local run reached NETIVRI_HOME")
	}
	if ignore, err := os.ReadFile(filepath.Join(homeA, ".gitignore")); err != nil || string(ignore) != "*\n" {
		t.Errorf(".gitignore = %q, %v; want everything in the directory ignored", ignore, err)
	}

	// No flag: the directory of the run is found on its own.
	code, out, errOut = localRun(t, a, global, "status")
	if code != 0 {
		t.Fatalf("status in a: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	status := angelData(t, out)
	if angelInt(t, status, "pid") != angelInt(t, upA, "pid") || status["home_source"] != "./.netivri" {
		t.Errorf("status in a: pid %v source %v, want the angel of a found through ./.netivri", status["pid"], status["home_source"])
	}
	sameDirectory(t, status["home"], homeA)

	// Another directory, and the global one, know nothing of it.
	for _, args := range [][]string{{"status"}, {"status", "--home", global}} {
		if code, _, errOut = localRun(t, b, global, args...); code != cli.ExitNoAngel {
			t.Errorf("%v in b: exit %d (stderr %q), want %d: no angel there", args, code, errOut, cli.ExitNoAngel)
		}
	}
	if _, err := os.Stat(filepath.Join(b, ".netivri")); err == nil {
		t.Error("a command without --local created ./.netivri")
	}

	// A second angel with its own directory runs next to the first.
	code, out, errOut = localRun(t, b, global, "up", "--local", "--port", "0")
	if code != 0 {
		t.Fatalf("up --local in b: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	upB := angelData(t, out)
	if angelInt(t, upB, "pid") == angelInt(t, upA, "pid") || upB["listen"] == upA["listen"] {
		t.Errorf("two directories, one angel: a %v/%v, b %v/%v", upA["pid"], upA["listen"], upB["pid"], upB["listen"])
	}
	_, out, _ = localRun(t, a, global, "status")
	if angelInt(t, angelData(t, out), "pid") != angelInt(t, upA, "pid") {
		t.Error("status in a answers with another angel")
	}

	// Both flags at once are refused, and stopping one leaves the other.
	if code, _, errOut = localRun(t, a, global, "status", "--local", "--home", global); code != cli.ExitUsage {
		t.Errorf("--local with --home: exit %d (stderr %q), want %d", code, errOut, cli.ExitUsage)
	}
	if code, out, errOut = localRun(t, a, global, "down"); code != 0 {
		t.Fatalf("down in a: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code, _, _ = localRun(t, a, global, "status"); code != cli.ExitNoAngel {
		t.Errorf("status in a after down: exit %d, want %d", code, cli.ExitNoAngel)
	}
	if code, _, errOut = localRun(t, b, global, "status"); code != 0 {
		t.Errorf("status in b after a was stopped: exit %d (stderr %q)", code, errOut)
	}
}
