//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const (
	angelE2ETimeout       = 30 * time.Second
	angelFirstLineTimeout = 20 * time.Second
	angelExitTimeout      = 15 * time.Second
)

// angelStateRel is the state file path relative to the home directory.
var angelStateRel = filepath.Join("run", "netivri.json")

// angelStateFile is the part of run/netivri.json the tests need.
type angelStateFile struct {
	PID     int    `json:"pid"`
	Listen  string `json:"listen"`
	Control string `json:"control"`
	Token   string `json:"token"`
}

func angelReadState(home string) (angelStateFile, error) {
	var st angelStateFile
	data, err := os.ReadFile(filepath.Join(home, angelStateRel))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func angelStateExists(home string) bool {
	_, err := os.Stat(filepath.Join(home, angelStateRel))
	return err == nil
}

// angelExec runs the binary to completion. Problems that are not a non-zero
// exit are returned as errors instead of failing the test, so cleanups can use
// it too.
func angelExec(home string, args ...string) (code int, stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), angelE2ETimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, args...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
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

func angelRun(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	code, stdout, stderr, err := angelExec(home, args...)
	if err != nil {
		t.Fatalf("netivri %v: %v (stdout %q, stderr %q)", args, err, stdout, stderr)
	}
	return code, stdout, stderr
}

// angelHome is a temp state directory whose angel, if any, is stopped at
// the end of the test.
type angelHome struct {
	dir string
	pid int // angel pid taken from the `up` output, for the last-resort kill
}

func newAngelHome(t *testing.T) *angelHome {
	t.Helper()
	h := &angelHome{dir: t.TempDir()}
	t.Cleanup(func() {
		code, _, _, err := angelExec(h.dir, "down")
		downOK := err == nil && code == 0
		pid := h.pid
		if st, rerr := angelReadState(h.dir); rerr == nil {
			pid = st.PID
			downOK = false // the state file survived: the angel is still there
		}
		if !downOK && pid > 0 {
			// Last resort, own child only: the pid the angel reported.
			if p, ferr := os.FindProcess(pid); ferr == nil {
				_ = p.Kill()
			}
		}
	})
	return h
}

// angelData decodes the success data object of one stdout envelope.
func angelData(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if !env.OK {
		t.Fatalf("stdout reports ok=false: %q", stdout)
	}
	return env.Data
}

// angelInt reads a JSON number from a decoded data object.
func angelInt(t *testing.T, data map[string]any, key string) int {
	t.Helper()
	v, ok := data[key].(float64)
	if !ok {
		t.Fatalf("data[%q] = %v, want a number", key, data[key])
	}
	return int(v)
}

func TestE2EAngelStatusNotRunning(t *testing.T) {
	code, stdout, stderr := angelRun(t, t.TempDir(), "status")
	if code != cli.ExitNoAngel {
		t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoAngel, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "ANGEL_NOT_RUNNING" {
		t.Errorf("code = %q, want ANGEL_NOT_RUNNING", got)
	}
}

func TestE2EAngelDownNotRunning(t *testing.T) {
	code, stdout, stderr := angelRun(t, t.TempDir(), "down")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	data := angelData(t, stdout)
	if data["angel"] != "stopped" || data["was_running"] != false {
		t.Errorf("data = %v", data)
	}
}

// TestE2EAngelLifecycle covers up, status, a proxied round trip, idempotent
// up, the conflict on a different explicit port, and down.
func TestE2EAngelLifecycle(t *testing.T) {
	h := newAngelHome(t)

	// token is learned from the state file after the first up; from then on no
	// command output may contain it, and a failure message never prints it.
	var token string
	run := func(args ...string) (int, string, string) {
		code, stdout, stderr := angelRun(t, h.dir, args...)
		if token != "" {
			if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
				t.Errorf("the control token leaked into the output of netivri %v", args)
				stdout = strings.ReplaceAll(stdout, token, "<token>")
				stderr = strings.ReplaceAll(stderr, token, "<token>")
			}
		}
		return code, stdout, stderr
	}

	// up --port 0 returns promptly while the angel keeps running.
	code, stdout, stderr := run("up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	up := angelData(t, stdout)
	pid := angelInt(t, up, "pid")
	h.pid = pid
	listen, _ := up["listen"].(string)
	if up["already_running"] != false || up["foreground"] != false || listen == "" {
		t.Fatalf("up data = %v", up)
	}
	st, err := angelReadState(h.dir)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	token = st.Token
	if token == "" || strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Fatal("state file has no token, or the token leaked into the up output")
	}

	// status answers with the same pid and listen address.
	code, stdout, stderr = run("status")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	status := angelData(t, stdout)
	if angelInt(t, status, "pid") != pid || status["listen"] != listen {
		t.Errorf("status = %v, want pid %d and listen %q", status, pid, listen)
	}
	if status["foreground"] != false || status["auth"] != "none" || status["route"] != "direct" {
		t.Errorf("status = %v, want foreground false, auth none, route direct", status)
	}
	if _, ok := status["uptime_seconds"]; !ok {
		t.Error("status has no uptime_seconds")
	}

	// The proxy really works through the angel.
	conn := socks5NoAuthConnect(t, listen, echoServer(t))
	echoRoundTrip(t, conn, conn)

	// A second up is idempotent.
	code, stdout, stderr = run("up", "--port", "0")
	if code != 0 {
		t.Fatalf("second up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	again := angelData(t, stdout)
	if again["already_running"] != true || angelInt(t, again, "pid") != pid {
		t.Errorf("second up data = %v, want already_running with pid %d", again, pid)
	}

	// A different explicit port conflicts with the running angel.
	code, stdout, stderr = run("up", "--port", strconv.Itoa(freePort(t)))
	if code != cli.ExitConflict {
		t.Fatalf("conflicting up exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitConflict, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "ALREADY_RUNNING" {
		t.Errorf("code = %q, want ALREADY_RUNNING", got)
	}

	// down stops the angel.
	code, stdout, stderr = run("down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	down := angelData(t, stdout)
	if down["was_running"] != true || angelInt(t, down, "pid") != pid {
		t.Errorf("down data = %v, want was_running with pid %d", down, pid)
	}

	// Afterwards: status exits 4, the proxy port is closed, the state is gone.
	if code, stdout, stderr = run("status"); code != cli.ExitNoAngel {
		t.Errorf("status after down exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoAngel, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dialer net.Dialer
	if c, err := dialer.DialContext(ctx, "tcp", listen); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts connections after down", listen)
	}
	if angelStateExists(h.dir) {
		t.Error("the state file is still there after down")
	}
}

// angelLineWriter records its output and closes first once the first line
// is complete.
type angelLineWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	first chan struct{}
	done  bool
}

func newAngelLineWriter() *angelLineWriter {
	return &angelLineWriter{first: make(chan struct{})}
}

func (w *angelLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if !w.done && bytes.IndexByte(w.buf.Bytes(), '\n') >= 0 {
		w.done = true
		close(w.first)
	}
	return n, err
}

func (w *angelLineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestE2EAngelForegroundRegisters checks that a foreground up is a full
// instance: status sees it, down stops it, and it exits 0 on every OS.
func TestE2EAngelForegroundRegisters(t *testing.T) {
	h := newAngelHome(t)

	cmd := exec.Command(netivriBin, "up", "--foreground", "--port", "0")
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+h.dir)
	stdout, stderr := newAngelLineWriter(), newAngelLineWriter()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill() // own child, still running
			<-done
		}
	})

	select {
	case <-stdout.first:
	case <-done:
		t.Fatalf("foreground up exited early: %v (stderr %q)", waitErr, stderr.String())
	case <-time.After(angelFirstLineTimeout):
		t.Fatalf("no first line within %v (stderr %q)", angelFirstLineTimeout, stderr.String())
	}
	line, _, _ := strings.Cut(stdout.String(), "\n")
	started := angelData(t, line)
	pid := angelInt(t, started, "pid")
	listen, _ := started["listen"].(string)
	if started["foreground"] != true || listen == "" {
		t.Fatalf("up data = %v", started)
	}

	code, out, errOut := angelRun(t, h.dir, "status")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, out, errOut)
	}
	status := angelData(t, out)
	if angelInt(t, status, "pid") != pid || status["listen"] != listen || status["foreground"] != true {
		t.Errorf("status = %v, want pid %d, listen %q, foreground true", status, pid, listen)
	}

	code, out, errOut = angelRun(t, h.dir, "down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, out, errOut)
	}
	if angelData(t, out)["was_running"] != true {
		t.Errorf("down stdout = %q, want was_running true", out)
	}

	select {
	case <-done:
		if waitErr != nil {
			t.Errorf("foreground process exit = %v, want 0", waitErr)
		}
	case <-time.After(angelExitTimeout):
		t.Fatal("the foreground process did not exit after down")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("foreground stderr = %q, want empty", got)
	}
}

// TestE2EAngelStaleState: a state file of a dead angel gives exit 4, is
// removed, and does not block the next up.
func TestE2EAngelStaleState(t *testing.T) {
	h := newAngelHome(t)

	// A port nothing listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	dead := l.Addr().String()
	_ = l.Close()

	stale, err := json.Marshal(map[string]any{
		"pid": 999999999, "listen": "127.0.0.1:7890", "control": dead,
		"token": "stale-token", "foreground": false,
		"started_at": "2026-01-01T00:00:00Z", "version": "stale",
		"auth": "none", "protocols": []string{"socks5"}, "route": "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "run"), 0o700); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, angelStateRel), stale, 0o600); err != nil {
		t.Fatalf("write stale state: %v", err)
	}

	code, stdout, stderr := angelRun(t, h.dir, "status")
	if code != cli.ExitNoAngel {
		t.Fatalf("status exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoAngel, stdout, stderr)
	}
	if angelStateExists(h.dir) {
		t.Error("the stale state file was not removed")
	}

	code, stdout, stderr = angelRun(t, h.dir, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up after stale state exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	up := angelData(t, stdout)
	h.pid = angelInt(t, up, "pid")
	if up["already_running"] != false {
		t.Errorf("up data = %v, want already_running false", up)
	}
}

// TestE2EAngelBusyPort: PORT_IN_USE reaches the parent through the handshake
// and no angel is left behind.
func TestE2EAngelBusyPort(t *testing.T) {
	h := newAngelHome(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port

	code, stdout, stderr := angelRun(t, h.dir, "up", "--port", strconv.Itoa(busy))
	if code != cli.ExitConflict {
		t.Fatalf("up exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitConflict, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "PORT_IN_USE" {
		t.Errorf("code = %q, want PORT_IN_USE", got)
	}
	if angelStateExists(h.dir) {
		t.Error("a state file was left behind")
	}
	if code, _, _ := angelRun(t, h.dir, "status"); code != cli.ExitNoAngel {
		t.Errorf("status exit = %d, want %d", code, cli.ExitNoAngel)
	}
}
