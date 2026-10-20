//go:build integration

package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && (host == "127.0.0.1" || host == "::1")
}

func requireStatus(t *testing.T, _ *Server, c Client, wantPID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Daemon != "running" || st.PID != wantPID {
		t.Errorf("status daemon=%q pid=%d", st.Daemon, st.PID)
	}
}

func TestControlServerLive(t *testing.T) {
	s, err := StartServer("secret", func() Status { return Status{Daemon: "running", PID: 99, Listen: "127.0.0.1:7890"} }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !isLoopbackAddr(s.Addr()) {
		t.Fatalf("addr=%s", s.Addr())
	}
	c := Client{Addr: s.Addr(), Token: "secret"}
	requireStatus(t, s, c, 99)
	if _, err := (Client{Addr: s.Addr(), Token: "bad"}).Status(context.Background()); !errors.Is(err, ErrControlRejected) {
		t.Error("wrong token was not rejected")
	}
	called := make(chan struct{})
	s2, err := StartServer("t2", func() Status { return Status{} }, func() { close(called) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (Client{Addr: s2.Addr(), Token: "t2"}).Stop(ctx); err != nil {
		t.Fatal("stop request failed")
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("stop callback was not invoked")
	}
}

func TestControlServerClosed(t *testing.T) {
	s, err := StartServer("secret", func() Status { return Status{} }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	addr := s.Addr()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := (Client{Addr: addr, Token: "secret"}).Status(ctx); err == nil {
		t.Error("Status succeeded after Close")
	}
}

func TestProbeUnreachableRemovesStaleState(t *testing.T) {
	home := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	st := testState()
	st.Control = addr
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), home); !errors.Is(err, ErrNotRunning) {
		t.Error("Probe did not report not running")
	}
	if stateExists(home) {
		t.Error("state remains")
	}
}

func TestProbePidMismatch(t *testing.T) {
	s, err := StartServer("secret", func() Status { return Status{Daemon: "running", PID: 12345} }, func() {})
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
	if _, err := Probe(context.Background(), home); !errors.Is(err, ErrNotRunning) {
		t.Error("Probe did not report not running")
	}
	if stateExists(home) {
		t.Error("state remains")
	}
}

func TestProbeLiveDaemon(t *testing.T) {
	// The live answer differs from the state file in listen and auth: the
	// caller must get both, unmixed.
	live := Status{Daemon: "running", PID: 4321, Listen: "127.0.0.1:7891", Auth: "enabled"}
	s, err := StartServer("secret", func() Status { return live }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	home := t.TempDir()
	st := testState() // PID 4321, listen 127.0.0.1:7890, auth none
	st.Control = s.Addr()
	st.Token = "secret"
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	gotState, gotLive, err := ProbeLive(context.Background(), home)
	if err != nil {
		t.Fatalf("ProbeLive: %v", err)
	}
	if gotState.Listen != "127.0.0.1:7890" || gotState.Auth != "none" || gotState.Token != "secret" {
		t.Errorf("state = listen %q auth %q, want the state file values", gotState.Listen, gotState.Auth)
	}
	if gotLive.Listen != "127.0.0.1:7891" || gotLive.Auth != "enabled" || gotLive.PID != 4321 {
		t.Errorf("live = %+v, want the live answer", gotLive)
	}
}

func TestClientWithoutAuthRejectedLive(t *testing.T) {
	s, err := StartServer("secret", func() Status { return Status{} }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", resp.StatusCode)
	}
}
