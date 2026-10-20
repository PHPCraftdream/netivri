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
	daemonE2ETimeout       = 30 * time.Second
	daemonFirstLineTimeout = 20 * time.Second
	daemonExitTimeout      = 15 * time.Second
)

// daemonStateRel is the state file path relative to the home directory.
var daemonStateRel = filepath.Join("run", "netivri.json")

// daemonStateFile is the part of run/netivri.json the tests need.
type daemonStateFile struct {
	PID     int    `json:"pid"`
	Listen  string `json:"listen"`
	Control string `json:"control"`
	Token   string `json:"token"`
}

func daemonReadState(home string) (daemonStateFile, error) {
	var st daemonStateFile
	data, err := os.ReadFile(filepath.Join(home, daemonStateRel))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func daemonStateExists(home string) bool {
	_, err := os.Stat(filepath.Join(home, daemonStateRel))
	return err == nil
}

// daemonExec runs the binary to completion. Problems that are not a non-zero
// exit are returned as errors instead of failing the test, so cleanups can use
// it too.
func daemonExec(home string, args ...string) (code int, stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonE2ETimeout)
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

func daemonRun(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	code, stdout, stderr, err := daemonExec(home, args...)
	if err != nil {
		t.Fatalf("netivri %v: %v (stdout %q, stderr %q)", args, err, stdout, stderr)
	}
	return code, stdout, stderr
}

// daemonHome is a temp state directory whose daemon, if any, is stopped at
// the end of the test.
type daemonHome struct {
	dir string
	pid int // daemon pid taken from the `up` output, for the last-resort kill
}

func newDaemonHome(t *testing.T) *daemonHome {
	t.Helper()
	h := &daemonHome{dir: t.TempDir()}
	t.Cleanup(func() {
		code, _, _, err := daemonExec(h.dir, "down")
		downOK := err == nil && code == 0
		pid := h.pid
		if st, rerr := daemonReadState(h.dir); rerr == nil {
			pid = st.PID
			downOK = false // the state file survived: the daemon is still there
		}
		if !downOK && pid > 0 {
			// Last resort, own child only: the pid the daemon reported.
			if p, ferr := os.FindProcess(pid); ferr == nil {
				_ = p.Kill()
			}
		}
	})
	return h
}

// daemonData decodes the success data object of one stdout envelope.
func daemonData(t *testing.T, stdout string) map[string]any {
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

// daemonInt reads a JSON number from a decoded data object.
func daemonInt(t *testing.T, data map[string]any, key string) int {
	t.Helper()
	v, ok := data[key].(float64)
	if !ok {
		t.Fatalf("data[%q] = %v, want a number", key, data[key])
	}
	return int(v)
}

func TestE2EDaemonStatusNotRunning(t *testing.T) {
	code, stdout, stderr := daemonRun(t, t.TempDir(), "status")
	if code != cli.ExitNoDaemon {
		t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoDaemon, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "DAEMON_NOT_RUNNING" {
		t.Errorf("code = %q, want DAEMON_NOT_RUNNING", got)
	}
}

func TestE2EDaemonDownNotRunning(t *testing.T) {
	code, stdout, stderr := daemonRun(t, t.TempDir(), "down")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	data := daemonData(t, stdout)
	if data["daemon"] != "stopped" || data["was_running"] != false {
		t.Errorf("data = %v", data)
	}
}

// TestE2EDaemonLifecycle covers up, status, a proxied round trip, idempotent
// up, the conflict on a different explicit port, and down.
func TestE2EDaemonLifecycle(t *testing.T) {
	h := newDaemonHome(t)

	// token is learned from the state file after the first up; from then on no
	// command output may contain it, and a failure message never prints it.
	var token string
	run := func(args ...string) (int, string, string) {
		code, stdout, stderr := daemonRun(t, h.dir, args...)
		if token != "" {
			if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
				t.Errorf("the control token leaked into the output of netivri %v", args)
				stdout = strings.ReplaceAll(stdout, token, "<token>")
				stderr = strings.ReplaceAll(stderr, token, "<token>")
			}
		}
		return code, stdout, stderr
	}

	// up --port 0 returns promptly while the daemon keeps running.
	code, stdout, stderr := run("up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	up := daemonData(t, stdout)
	pid := daemonInt(t, up, "pid")
	h.pid = pid
	listen, _ := up["listen"].(string)
	if up["already_running"] != false || up["foreground"] != false || listen == "" {
		t.Fatalf("up data = %v", up)
	}
	st, err := daemonReadState(h.dir)
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
	status := daemonData(t, stdout)
	if daemonInt(t, status, "pid") != pid || status["listen"] != listen {
		t.Errorf("status = %v, want pid %d and listen %q", status, pid, listen)
	}
	if status["foreground"] != false || status["auth"] != "none" || status["route"] != "direct" {
		t.Errorf("status = %v, want foreground false, auth none, route direct", status)
	}
	if _, ok := status["uptime_seconds"]; !ok {
		t.Error("status has no uptime_seconds")
	}

	// The proxy really works through the daemon.
	conn := socks5NoAuthConnect(t, listen, echoServer(t))
	echoRoundTrip(t, conn, conn)

	// A second up is idempotent.
	code, stdout, stderr = run("up", "--port", "0")
	if code != 0 {
		t.Fatalf("second up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	again := daemonData(t, stdout)
	if again["already_running"] != true || daemonInt(t, again, "pid") != pid {
		t.Errorf("second up data = %v, want already_running with pid %d", again, pid)
	}

	// A different explicit port conflicts with the running daemon.
	code, stdout, stderr = run("up", "--port", strconv.Itoa(freePort(t)))
	if code != cli.ExitConflict {
		t.Fatalf("conflicting up exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitConflict, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "ALREADY_RUNNING" {
		t.Errorf("code = %q, want ALREADY_RUNNING", got)
	}

	// down stops the daemon.
	code, stdout, stderr = run("down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	down := daemonData(t, stdout)
	if down["was_running"] != true || daemonInt(t, down, "pid") != pid {
		t.Errorf("down data = %v, want was_running with pid %d", down, pid)
	}

	// Afterwards: status exits 4, the proxy port is closed, the state is gone.
	if code, stdout, stderr = run("status"); code != cli.ExitNoDaemon {
		t.Errorf("status after down exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoDaemon, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dialer net.Dialer
	if c, err := dialer.DialContext(ctx, "tcp", listen); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts connections after down", listen)
	}
	if daemonStateExists(h.dir) {
		t.Error("the state file is still there after down")
	}
}

// daemonLineWriter records its output and closes first once the first line
// is complete.
type daemonLineWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	first chan struct{}
	done  bool
}

func newDaemonLineWriter() *daemonLineWriter {
	return &daemonLineWriter{first: make(chan struct{})}
}

func (w *daemonLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if !w.done && bytes.IndexByte(w.buf.Bytes(), '\n') >= 0 {
		w.done = true
		close(w.first)
	}
	return n, err
}

func (w *daemonLineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestE2EDaemonForegroundRegisters checks that a foreground up is a full
// instance: status sees it, down stops it, and it exits 0 on every OS.
func TestE2EDaemonForegroundRegisters(t *testing.T) {
	h := newDaemonHome(t)

	cmd := exec.Command(netivriBin, "up", "--foreground", "--port", "0")
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+h.dir)
	stdout, stderr := newDaemonLineWriter(), newDaemonLineWriter()
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
	case <-time.After(daemonFirstLineTimeout):
		t.Fatalf("no first line within %v (stderr %q)", daemonFirstLineTimeout, stderr.String())
	}
	line, _, _ := strings.Cut(stdout.String(), "\n")
	started := daemonData(t, line)
	pid := daemonInt(t, started, "pid")
	listen, _ := started["listen"].(string)
	if started["foreground"] != true || listen == "" {
		t.Fatalf("up data = %v", started)
	}

	code, out, errOut := daemonRun(t, h.dir, "status")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, out, errOut)
	}
	status := daemonData(t, out)
	if daemonInt(t, status, "pid") != pid || status["listen"] != listen || status["foreground"] != true {
		t.Errorf("status = %v, want pid %d, listen %q, foreground true", status, pid, listen)
	}

	code, out, errOut = daemonRun(t, h.dir, "down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, out, errOut)
	}
	if daemonData(t, out)["was_running"] != true {
		t.Errorf("down stdout = %q, want was_running true", out)
	}

	select {
	case <-done:
		if waitErr != nil {
			t.Errorf("foreground process exit = %v, want 0", waitErr)
		}
	case <-time.After(daemonExitTimeout):
		t.Fatal("the foreground process did not exit after down")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("foreground stderr = %q, want empty", got)
	}
}

// TestE2EDaemonStaleState: a state file of a dead daemon gives exit 4, is
// removed, and does not block the next up.
func TestE2EDaemonStaleState(t *testing.T) {
	h := newDaemonHome(t)

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
	if err := os.WriteFile(filepath.Join(h.dir, daemonStateRel), stale, 0o600); err != nil {
		t.Fatalf("write stale state: %v", err)
	}

	code, stdout, stderr := daemonRun(t, h.dir, "status")
	if code != cli.ExitNoDaemon {
		t.Fatalf("status exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitNoDaemon, stdout, stderr)
	}
	if daemonStateExists(h.dir) {
		t.Error("the stale state file was not removed")
	}

	code, stdout, stderr = daemonRun(t, h.dir, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up after stale state exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	up := daemonData(t, stdout)
	h.pid = daemonInt(t, up, "pid")
	if up["already_running"] != false {
		t.Errorf("up data = %v, want already_running false", up)
	}
}

// TestE2EDaemonBusyPort: PORT_IN_USE reaches the parent through the handshake
// and no daemon is left behind.
func TestE2EDaemonBusyPort(t *testing.T) {
	h := newDaemonHome(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port

	code, stdout, stderr := daemonRun(t, h.dir, "up", "--port", strconv.Itoa(busy))
	if code != cli.ExitConflict {
		t.Fatalf("up exit = %d, want %d (stdout %q, stderr %q)", code, cli.ExitConflict, stdout, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "PORT_IN_USE" {
		t.Errorf("code = %q, want PORT_IN_USE", got)
	}
	if daemonStateExists(h.dir) {
		t.Error("a state file was left behind")
	}
	if code, _, _ := daemonRun(t, h.dir, "status"); code != cli.ExitNoDaemon {
		t.Errorf("status exit = %d, want %d", code, cli.ExitNoDaemon)
	}
}
