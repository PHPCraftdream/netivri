//go:build e2e

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

const (
	e2eFirstLineTimeout = 20 * time.Second
	e2eStopGrace        = 10 * time.Second
	e2eKillGrace        = 5 * time.Second
	e2eIOTimeout        = 20 * time.Second
)

var netivriBin string

// TestMain builds the real binary once into a temp dir. The cwd of the
// test binary is the package directory, so a plain `go build .` works.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "netivri-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create temp dir: %v\n", err)
		os.Exit(1)
	}
	name := "netivri"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	netivriBin = filepath.Join(tmp, name)
	pkgDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: getwd: %v\n", err)
		_ = os.RemoveAll(tmp)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-trimpath", "-o", netivriBin, ".")
	build.Dir = pkgDir
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build failed: %v\n%s\n", err, out)
		_ = os.RemoveAll(tmp)
		os.Exit(1)
	}
	// The output is Ktav by default; these tests parse JSON. e2e_output_e2e_test.go
	// runs the binary without this variable.
	_ = os.Setenv("NETIVRI_OUTPUT", "json")
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// upStatus is the first stdout line of `netivri up`.
type upStatus struct {
	SchemaVersion int        `json:"schema_version"`
	OK            bool       `json:"ok"`
	Data          upRespData `json:"data"`
	Error         *cli.Error `json:"error"`
}

type upRespData struct {
	Angel     string   `json:"angel"`
	PID       int      `json:"pid"`
	Listen    string   `json:"listen"`
	Protocols []string `json:"protocols"`
	Auth      string   `json:"auth"`
}

// lineResult carries the first stdout line read to the test goroutine,
// so t.Fatalf is never called from a reader goroutine.
type lineResult struct {
	line string
	err  error
}

// lockedBuffer is a bytes.Buffer safe for concurrent Write and String:
// drain goroutines write while the test may read on a failure path.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// upProc is a running `netivri up --foreground` process.
type upProc struct {
	t         *testing.T
	cmd       *exec.Cmd
	status    upStatus
	FirstLine string // raw first stdout line, including its newline
	stdoutBuf *lockedBuffer
	stderrBuf *lockedBuffer
	stopOnce  sync.Once
	waitCh    chan error
	stdoutEOF chan struct{}
	stderrEOF chan struct{}
	stdoutR   *os.File
	stderrR   *os.File
	exitMu    sync.Mutex
	exited    bool
	exitErr   error
}

var (
	upProcsMu sync.Mutex
	upProcs   = map[string]*upProc{} // home -> the latest angel started for it
)

// upProcReport says whether the angel started for home is still running
// (else how it exited) and shows the tail of its stdout and stderr without
// links or credentials, to name why the angel stopped answering.
func upProcReport(home string) string {
	upProcsMu.Lock()
	p := upProcs[home]
	upProcsMu.Unlock()
	if p == nil {
		return "angel process: not started by this test"
	}
	p.exitMu.Lock()
	state := "angel process: running"
	if p.exited {
		state = "angel process: exited"
	}
	p.exitMu.Unlock()
	return state + "\n" + e2eTail("stderr", p.Stderr(), 15) + "\n" + e2eTail("stdout", p.Stdout(), 5)
}

var e2eURLPattern = regexp.MustCompile(`\S*://\S+`)
var e2eUUIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
var e2eTokenPattern = regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`)

func e2eRedactDiagnostic(text string) string {
	text = e2eURLPattern.ReplaceAllString(text, "[redacted]")
	text = e2eUUIDPattern.ReplaceAllString(text, "[redacted]")
	text = e2eTokenPattern.ReplaceAllString(text, "[redacted]")
	lower := strings.ToLower(text)
	if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(text, "@") {
		return "[redacted]"
	}
	return text
}
func TestE2ERedactDiagnosticText(t *testing.T) {
	for _, value := range []string{
		"https://token.example/path",
		"uuid=00000000-0000-4000-8000-000000000000",
		"key=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-",
		"password=hidden",
		"alice@example.invalid",
	} {
		got := e2eRedactDiagnostic(value)
		for _, secret := range []string{"token.example", "00000000-0000", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "hidden", "alice@example"} {
			if strings.Contains(got, secret) {
				t.Fatal("diagnostic redaction retained sensitive text")
			}
		}
	}
	if got := e2eRedactDiagnostic("server stopped"); got != "server stopped" {
		t.Fatalf("ordinary diagnostic text changed: %q", got)
	}
}

// e2eTail returns the last n redacted lines of text.
func e2eTail(name, text string, n int) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, e2eRedactDiagnostic(line))
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return name + " tail:\n" + strings.Join(kept, "\n")
}

// startUp spawns `netivri up --foreground <args...>` with
// NETIVRI_HOME=home, reads and decodes the first stdout line and
// registers Stop as cleanup. Sequential use only: no t.Parallel.
func startUp(t *testing.T, home string, args ...string) *upProc {
	t.Helper()
	full := append([]string{"up", "--foreground"}, args...)
	cmd := exec.Command(netivriBin, full...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		t.Fatalf("stderr pipe: %v", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		t.Fatalf("start %s: %v", netivriBin, err)
	}
	p := &upProc{
		t:         t,
		cmd:       cmd,
		stdoutBuf: &lockedBuffer{},
		stderrBuf: &lockedBuffer{},
		waitCh:    make(chan error, 1),
		stdoutEOF: make(chan struct{}),
		stderrEOF: make(chan struct{}),
		stdoutR:   stdoutR,
		stderrR:   stderrR,
	}
	// Cleanup registered immediately after Start: any failure below must
	// not leave the child running.
	t.Cleanup(p.Stop)
	upProcsMu.Lock()
	upProcs[home] = p
	upProcsMu.Unlock()
	// The parent keeps only the read ends.
	if err := stdoutW.Close(); err != nil {
		t.Errorf("close stdout write end: %v", err)
	}
	if err := stderrW.Close(); err != nil {
		t.Errorf("close stderr write end: %v", err)
	}
	go func() {
		defer close(p.stderrEOF)
		_, _ = io.Copy(p.stderrBuf, stderrR)
	}()
	lineCh := make(chan lineResult, 1)
	go func() {
		defer close(p.stdoutEOF)
		br := bufio.NewReader(stdoutR)
		line, err := br.ReadString('\n')
		lineCh <- lineResult{line: line, err: err}
		if err != nil {
			return
		}
		// Seed the captured stdout with the status line so Stdout()
		// is the complete stdout of the process.
		_, _ = p.stdoutBuf.Write([]byte(line))
		_, _ = io.Copy(p.stdoutBuf, br)
	}()
	go func() {
		err := cmd.Wait()
		p.exitMu.Lock()
		p.exited, p.exitErr = true, err
		p.exitMu.Unlock()
		p.waitCh <- err
	}()
	select {
	case res := <-lineCh:
		if res.err != nil {
			// Stop first: kill the child and close the pipe read ends
			// so no descriptor or drain goroutine outlives the failure.
			p.Stop()
			t.Fatalf("read first stdout line: %v (stderr: %s)", res.err, p.stderrBuf.String())
		}
		var st upStatus
		if err := json.Unmarshal([]byte(res.line), &st); err != nil {
			p.Stop()
			t.Fatalf("decode status line %q: %v", res.line, err)
		}
		p.status = st
		p.FirstLine = res.line
	case <-time.After(e2eFirstLineTimeout):
		p.Stop()
		t.Fatalf("no first stdout line within %v (stderr so far: %s)", e2eFirstLineTimeout, p.stderrBuf.String())
	}
	return p
}

// Status returns the decoded first stdout line.
func (p *upProc) Status() upStatus { return p.status }

// Stdout returns everything the process wrote to stdout.
func (p *upProc) Stdout() string { return p.stdoutBuf.String() }

// Stderr returns everything the process wrote to stderr.
func (p *upProc) Stderr() string { return p.stderrBuf.String() }

// Stop terminates the process: SIGTERM on POSIX (exit code must be 0),
// Kill on Windows. Idempotent; registered as cleanup by startUp.
func (p *upProc) Stop() {
	p.stopOnce.Do(func() {
		if p.cmd.Process != nil {
			if runtime.GOOS == "windows" {
				_ = p.cmd.Process.Kill()
			} else {
				_ = p.cmd.Process.Signal(syscall.SIGTERM)
			}
		}
		select {
		case err := <-p.waitCh:
			p.checkExit(err)
		case <-time.After(e2eStopGrace):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			select {
			case err := <-p.waitCh:
				p.checkExit(err)
				p.t.Error("netivri did not exit within the grace period after SIGTERM")
			case <-time.After(e2eKillGrace):
				p.t.Error("netivri did not exit after being killed")
			}
		}
		// The process has exited and the parent closed its write ends, so
		// the drain goroutines reach EOF on their own once every buffered
		// byte is read: wait for them BEFORE closing the read ends,
		// otherwise unread output would be discarded.
		names := []string{"stdout", "stderr"}
		for i, done := range []chan struct{}{p.stdoutEOF, p.stderrEOF} {
			select {
			case <-done:
			case <-time.After(e2eKillGrace):
				p.t.Errorf("draining %s did not finish; output assertions would see partial data", names[i])
			}
		}
		_ = p.stdoutR.Close()
		_ = p.stderrR.Close()
	})
}

func (p *upProc) checkExit(err error) {
	if runtime.GOOS == "windows" {
		return // no graceful signal exists; the kill exit code is not 0
	}
	if err != nil {
		p.t.Errorf("netivri exited non-zero after SIGTERM: %v", err)
	}
}

// runNetivri runs the binary to completion (failure cases) and returns
// the exit code, stdout and stderr.
func runNetivri(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eFirstLineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, append([]string{"up", "--foreground"}, args...)...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("netivri did not exit within %v (stdout: %q, stderr: %q)",
			e2eFirstLineTimeout, outBuf.String(), errBuf.String())
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run netivri: %v", err)
	}
	return code, outBuf.String(), errBuf.String()
}

// errEnvelope is the stderr JSON of a failed command.
type errEnvelope struct {
	SchemaVersion int        `json:"schema_version"`
	OK            bool       `json:"ok"`
	Error         *cli.Error `json:"error"`
}

// decodeErrEnvelope parses and sanity-checks one stderr error envelope.
func decodeErrEnvelope(t *testing.T, stderr string) errEnvelope {
	t.Helper()
	var env errEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("decode stderr envelope %q: %v", stderr, err)
	}
	if env.OK {
		t.Fatalf("stderr envelope reports ok=true: %q", stderr)
	}
	if env.Error == nil {
		t.Fatalf("stderr envelope has no error object: %q", stderr)
	}
	if env.SchemaVersion != cli.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", env.SchemaVersion, cli.SchemaVersion)
	}
	return env
}

// assertStatusSuccess checks the common shape of the up status line.
func assertStatusSuccess(t *testing.T, st upStatus) {
	t.Helper()
	if !st.OK {
		t.Fatalf("status ok = false, error = %+v", st.Error)
	}
	if st.SchemaVersion != cli.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", st.SchemaVersion, cli.SchemaVersion)
	}
	if st.Data.Angel != "running" {
		t.Fatalf("angel = %q, want running", st.Data.Angel)
	}
	if st.Data.PID <= 0 {
		t.Fatalf("pid = %d, want > 0", st.Data.PID)
	}
	if st.Data.Listen == "" {
		t.Fatal("listen is empty")
	}
}

// writeConfig writes config.ktav under home with the mutations applied
// to the default configuration.
func writeConfig(t *testing.T, home string, mutate func(*config.Config)) {
	t.Helper()
	cfg := config.Default()
	mutate(cfg)
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// loadConfigFile reads config.ktav back through config.Load.
func loadConfigFile(t *testing.T, home string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// assertNoConfigFile asserts config.ktav was not created under home.
func assertNoConfigFile(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(config.ConfigPath(home)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config.ktav exists after up without config (stat err = %v), want absent", err)
	}
}

// assertOneLine asserts stdout is exactly one JSON line.
func assertOneLine(t *testing.T, stdout string) {
	t.Helper()
	if !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, want exactly one line", stdout)
	}
}

// assertNoSecrets asserts none of the secrets appear in stdout+stderr.
func assertNoSecrets(t *testing.T, stdout, stderr string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(stdout, s) || strings.Contains(stderr, s) {
			t.Fatalf("output leaks a secret (length %d)", len(s))
		}
	}
}

// freePort reserves and immediately releases a loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// ---- loopback servers and hand-written proxy clients ----

// connSet tracks accepted connections so cleanup can close them all.
type connSet struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnSet() *connSet { return &connSet{conns: make(map[net.Conn]struct{})} }

func (s *connSet) add(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *connSet) remove(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *connSet) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
}

// echoServer starts a TCP echo backend on 127.0.0.1:0. Its cleanup
// closes the listener and every live connection, then waits (bounded)
// for the accept loop and all per-connection handlers, so no goroutine
// outlives the test and -race stays clean.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	set := newConnSet()
	var wg sync.WaitGroup
	var mu sync.Mutex
	closed := false
	done := make(chan struct{})
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		_ = l.Close()
		set.closeAll()
		// The accept loop holds a count until it returns, so no Add can race
		// this Wait: handlers are only added under mu while !closed.
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Errorf("echo server goroutines did not stop within %v", e2eKillGrace)
		}
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = c.Close()
				return
			}
			set.add(c)
			wg.Add(1)
			mu.Unlock()
			go func(c net.Conn) {
				defer wg.Done()
				defer set.remove(c)
				_ = c.SetDeadline(time.Now().Add(e2eIOTimeout))
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(c)
		}
	}()
	return l.Addr().String()
}

func dialProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, e2eIOTimeout)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(e2eIOTimeout)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	return c
}

func readFull(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func echoRoundTrip(t *testing.T, w io.Writer, r io.Reader) {
	t.Helper()
	payload := []byte("netivri-e2e-echo-payload")
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	got := readFull(t, r, len(payload))
	if string(got) != string(payload) {
		t.Fatalf("echo round-trip: got %q, want %q", got, payload)
	}
}

// socks5Greet sends the method negotiation and returns the chosen
// method byte.
func socks5Greet(t *testing.T, c net.Conn, methods ...byte) byte {
	t.Helper()
	buf := []byte{5, byte(len(methods))}
	buf = append(buf, methods...)
	if _, err := c.Write(buf); err != nil {
		t.Fatalf("greeting write: %v", err)
	}
	reply := readFull(t, c, 2)
	if reply[0] != 5 {
		t.Fatalf("greeting reply ver = %#x, want 5", reply[0])
	}
	return reply[1]
}

// socks5AuthReply performs the RFC 1929 subnegotiation and returns the
// status byte (0 = accepted).
func socks5AuthReply(t *testing.T, c net.Conn, user, pass string) byte {
	t.Helper()
	buf := []byte{1, byte(len(user))}
	buf = append(buf, user...)
	buf = append(buf, byte(len(pass)))
	buf = append(buf, pass...)
	if _, err := c.Write(buf); err != nil {
		t.Fatalf("socks5 auth write: %v", err)
	}
	reply := readFull(t, c, 2)
	if reply[0] != 1 {
		t.Fatalf("auth reply ver = %#x, want 1", reply[0])
	}
	return reply[1]
}

// socks5ConnectRequest sends CONNECT to target and consumes the reply.
func socks5ConnectRequest(t *testing.T, c net.Conn, target string) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	ip := net.ParseIP(host)
	req := []byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}
	if ip4 := ip.To4(); ip4 != nil {
		copy(req[4:8], ip4)
	} else {
		t.Fatalf("target %q is not an IPv4 literal", target)
	}
	req[8], req[9] = byte(port>>8), byte(port)
	if _, err := c.Write(req); err != nil {
		t.Fatalf("socks5 request write: %v", err)
	}
	reply := readFull(t, c, 4)
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("socks5 connect reply = %v, want rep 0x00", reply)
	}
	switch reply[3] {
	case 1:
		readFull(t, c, 4+2)
	case 3:
		n := readFull(t, c, 1)[0]
		readFull(t, c, int(n)+2)
	case 4:
		readFull(t, c, 16+2)
	default:
		t.Fatalf("socks5 connect reply atyp = %#x", reply[3])
	}
}

// socks5NoAuthConnect runs a full no-auth SOCKS5 CONNECT.
func socks5NoAuthConnect(t *testing.T, addr, target string) net.Conn {
	t.Helper()
	c := dialProxy(t, addr)
	if m := socks5Greet(t, c, 0); m != 0x00 {
		t.Fatalf("greeting method = %#x, want 0x00", m)
	}
	socks5ConnectRequest(t, c, target)
	return c
}

// socks5AuthConnect runs a full SOCKS5 CONNECT with RFC 1929 auth.
func socks5AuthConnect(t *testing.T, addr, user, pass, target string) net.Conn {
	t.Helper()
	c := dialProxy(t, addr)
	if m := socks5Greet(t, c, 0, 2); m != 0x02 {
		t.Fatalf("greeting method = %#x, want 0x02 (server demands auth)", m)
	}
	if code := socks5AuthReply(t, c, user, pass); code != 0 {
		t.Fatalf("socks5 auth rejected with status %#x", code)
	}
	socks5ConnectRequest(t, c, target)
	return c
}

// httpConnect sends raw HTTP CONNECT with optional Basic
// Proxy-Authorization and returns the conn, a buffered reader holding
// any bytes after the headers, and the status line.
func httpConnect(t *testing.T, addr, target, basicUser, basicPass string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	c := dialProxy(t, addr)
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if basicUser != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(basicUser + ":" + basicPass))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatalf("CONNECT write: %v", err)
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("CONNECT response: %v", err)
	}
	return c, br, line
}

// drainHTTPHeaders reads response headers up to the blank line.
func drainHTTPHeaders(t *testing.T, br *bufio.Reader) {
	t.Helper()
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("CONNECT headers: %v", err)
		}
		if h == "\r\n" {
			return
		}
	}
}
