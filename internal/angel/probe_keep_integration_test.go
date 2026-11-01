//go:build integration

package angel

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// TestProbeLiveKeepRunning reports the live answer like ProbeLive, but the
// state file survives every probe.
func TestProbeLiveKeepRunning(t *testing.T) {
	live := Status{Angel: "running", PID: 4321, Listen: "127.0.0.1:7891"}
	s, err := StartServer("secret", func() Status { return live }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	home := t.TempDir()
	st := testState()
	st.Control = s.Addr()
	st.Token = "secret"
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	gotState, gotLive, err := ProbeLiveKeep(context.Background(), home)
	if err != nil {
		t.Fatalf("ProbeLiveKeep: %v", err)
	}
	if gotState.Control != s.Addr() || gotLive.PID != 4321 || gotLive.Listen != "127.0.0.1:7891" {
		t.Errorf("state %+v live %+v", gotState, gotLive)
	}
	if !stateExists(home) {
		t.Error("ProbeLiveKeep removed the state file on success")
	}
}

// TestProbeLiveKeepControlTimeoutKeepsState is the watch's transient case:
// the state file points at a control endpoint that accepts but never
// answers. The probe fails as a timeout, keeps the state file and is not
// ErrNotRunning. Every accepted connection is closed at the end and the
// accept goroutine's exit is awaited.
func TestProbeLiveKeepControlTimeoutKeepsState(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	requestSeen := make(chan struct{})
	var once sync.Once
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func(c net.Conn) {
				// Signal after at least the request line arrived, so the
				// test knows the hanging endpoint really got the request.
				line, _ := bufio.NewReader(c).ReadString('\n')
				if line != "" {
					once.Do(func() { close(requestSeen) })
				}
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		conns = nil
		mu.Unlock()
		select {
		case <-acceptDone:
		case <-time.After(5 * time.Second):
			t.Error("the accept goroutine did not exit")
		}
	})
	home := t.TempDir()
	st := testState()
	st.Control = l.Addr().String()
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err = ProbeLiveKeep(ctx, home)
	if err == nil {
		t.Fatal("a hanging control endpoint did not fail the probe")
	}
	select {
	case <-requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the hanging endpoint never received the request")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("err = %v, want a timeout error", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("a control timeout became ErrNotRunning: %v", err)
	}
	if !stateExists(home) {
		t.Error("the transient failure removed the state file")
	}
}

// TestProbeLiveKeepConnRefusedKeepsState: nothing listens on the recorded
// control port (the angel crashed and left its state file). That is
// ErrNotRunning for the watch, and the state file stays.
func TestProbeLiveKeepConnRefusedKeepsState(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	st := testState()
	st.Control = addr
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = ProbeLiveKeep(ctx, home)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("refused control port: err = %v, want ErrNotRunning", err)
	}
	if !stateExists(home) {
		t.Error("the refused control port removed the state file")
	}
}

// TestProbeLiveKeepPidMismatchKeepsState: the control endpoint answers for
// another angel, so the angel the state file names is gone. That is
// ErrNotRunning for the watch, and the state file stays (cleanup is
// ProbeLive's job, not the watch's).
func TestProbeLiveKeepPidMismatchKeepsState(t *testing.T) {
	s, err := StartServer("secret", func() Status { return Status{Angel: "running", PID: 9999} }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	home := t.TempDir()
	st := testState() // PID 4321
	st.Control = s.Addr()
	st.Token = "secret"
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	_, _, err = ProbeLiveKeep(context.Background(), home)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("pid mismatch: err = %v, want ErrNotRunning", err)
	}
	if !stateExists(home) {
		t.Error("the pid mismatch removed the state file")
	}
}

func TestProbeLiveKeepStateMissing(t *testing.T) {
	_, _, err := ProbeLiveKeep(context.Background(), t.TempDir())
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("missing state: err = %v, want ErrNotRunning", err)
	}
}

// TestProbeLiveControlTimeoutKeepsState is the one-shot probe's version of
// the watch's transient case: a control endpoint that accepts but never
// answers (a live angel busy restarting its core) is ErrControlTimeout, not
// ErrNotRunning, and the state file survives. Before, the first slow answer
// deleted the state file of a live angel and status, down and up lost it.
func TestProbeLiveControlTimeoutKeepsState(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		conns = nil
		mu.Unlock()
		select {
		case <-acceptDone:
		case <-time.After(5 * time.Second):
			t.Error("the accept goroutine did not exit")
		}
	})
	home := t.TempDir()
	st := testState()
	st.Control = l.Addr().String()
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err = ProbeLive(ctx, home)
	if !errors.Is(err, ErrControlTimeout) {
		t.Fatalf("hanging control endpoint: err = %v, want ErrControlTimeout", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("a control timeout became ErrNotRunning: %v", err)
	}
	if !stateExists(home) {
		t.Error("a control timeout removed the state file of a live angel")
	}
}

// TestProbeLiveConnRefusedRemovesState: nothing listens on the recorded
// control port, the angel is gone and its stale state file is cleaned up.
func TestProbeLiveConnRefusedRemovesState(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	st := testState()
	st.Control = addr
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = ProbeLive(ctx, home)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("refused control port: err = %v, want ErrNotRunning", err)
	}
	if stateExists(home) {
		t.Error("the stale state file of a gone angel was not removed")
	}
}
