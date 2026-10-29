package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

type connLogFixture struct {
	log   *connLog
	lines []string
}

func newConnLogFixture() *connLogFixture {
	f := &connLogFixture{}
	f.log = newConnLog(
		func(format string, args ...any) { f.lines = append(f.lines, "INFO "+fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { f.lines = append(f.lines, "WARN "+fmt.Sprintf(format, args...)) })
	f.log.setNodes([]stats.NodeInfo{
		{Label: "main/us", Provider: "main", Country: "US", Name: "United States"},
		{Label: "own/nl", Provider: "own", Country: "NL"},
	})
	return f
}

func (f *connLogFixture) dialFail(login, label, why string, n int) {
	for i := 0; i < n; i++ {
		f.log.event(core.ConnEvent{Kind: core.ConnDialFailed, Login: login, Label: label, Why: why})
	}
}

func (f *connLogFixture) ended(login, label, why string, down int64, n int) {
	for i := 0; i < n; i++ {
		f.log.event(core.ConnEvent{Kind: core.ConnStreamEnded, Login: login, Label: label, Why: why, Down: down, Lifetime: time.Second})
	}
}

func TestConnLogSilentWhenNothingWentWrong(t *testing.T) {
	f := newConnLogFixture()
	f.ended("alice", "main/us", "", 1000, 50)
	f.log.flush()
	if len(f.lines) != 0 {
		t.Fatalf("lines for a quiet period: %q", f.lines)
	}
}

func TestConnLogDialFailuresAreOneLinePerAccountAndNode(t *testing.T) {
	f := newConnLogFixture()
	f.dialFail("alice", "main/us", "timeout", 2)
	f.dialFail("alice", "main/us", "connection refused", 1)
	f.dialFail("bob", "own/nl", "timeout", 1)
	f.log.flush()
	want := []string{
		"WARN account alice via main/us (United States, country US): 3 dials failed (connection refused x1, timeout x2)",
		"INFO account bob via own/nl (country NL): 1 dial failed (timeout x1)",
	}
	if strings.Join(f.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(f.lines, "\n"), strings.Join(want, "\n"))
	}
	f.lines = nil
	f.log.flush()
	if len(f.lines) != 0 {
		t.Errorf("the period was not reset: %q", f.lines)
	}
}

func TestConnLogBrokenConnections(t *testing.T) {
	f := newConnLogFixture()
	f.ended("alice", "main/us", "", 500, 90)
	f.ended("alice", "main/us", "", 0, 3) // closed without any data, not an error
	f.ended("alice", "main/us", "connection reset", 10, 4)
	f.ended("alice", "main/us", "timeout", 10, 1)
	f.log.flush()
	want := "WARN account alice via main/us (United States, country US): 98 connections ended, 5 broken (connection reset x4, timeout x1), 3 closed without any data"
	if len(f.lines) != 1 || f.lines[0] != want {
		t.Fatalf("lines = %q, want [%q]", f.lines, want)
	}

	// A single broken connection among many is INFO, and so is a handful
	// that is below a tenth of the connections.
	g := newConnLogFixture()
	g.ended("alice", "main/us", "", 500, 100)
	g.ended("alice", "main/us", "connection reset", 10, 1)
	g.log.flush()
	if len(g.lines) != 1 || !strings.HasPrefix(g.lines[0], "INFO ") {
		t.Fatalf("lines = %q, want one INFO", g.lines)
	}
	h := newConnLogFixture()
	h.ended("alice", "main/us", "", 500, 100)
	h.ended("alice", "main/us", "connection reset", 10, 5)
	h.log.flush()
	if len(h.lines) != 1 || !strings.HasPrefix(h.lines[0], "INFO ") {
		t.Fatalf("5 of 105 broken is under a tenth: %q", h.lines)
	}
}

func TestConnLogMovedSites(t *testing.T) {
	f := newConnLogFixture()
	for i := 0; i < 3; i++ {
		f.log.event(core.ConnEvent{Kind: core.ConnSiteMoved, Login: "alice", From: "main/us", Label: "own/nl", Why: core.MoveNodeDown})
	}
	f.log.event(core.ConnEvent{Kind: core.ConnSiteMoved, Login: "alice", From: "own/nl", Label: "main/us", Why: core.MoveBetterTier})
	f.log.flush()
	want := []string{
		"WARN account alice: 3 sites moved from main/us (United States, country US) to own/nl (country NL): node down",
		"INFO account alice: 1 site moved from own/nl (country NL) to main/us (United States, country US): better tier",
	}
	if strings.Join(f.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(f.lines, "\n"), strings.Join(want, "\n"))
	}
}

type fakeAuth struct{ users map[string]string }

func (a fakeAuth) Verify(user, pass string) bool { return a.users[user] == pass && pass != "" }
func (a fakeAuth) Users() []string {
	out := make([]string, 0, len(a.users))
	for u := range a.users {
		out = append(out, u)
	}
	return out
}

func TestConnLogFailedLoginsNameOnlyConfiguredAccounts(t *testing.T) {
	f := newConnLogFixture()
	auth := f.log.wrapAuth(fakeAuth{users: map[string]string{"alice": "secret"}})
	if !auth.Verify("alice", "secret") {
		t.Fatal("a right login was refused")
	}
	for i := 0; i < 2; i++ {
		auth.Verify("alice", "guess")
	}
	auth.Verify("hunter2-typed-as-a-login", "x")
	auth.Verify("root", "x")
	f.log.flush()
	if len(f.lines) != 1 {
		t.Fatalf("lines = %q, want one", f.lines)
	}
	if want := "WARN 4 proxy logins failed (alice x2, unknown logins x2)"; f.lines[0] != want {
		t.Fatalf("line = %q, want %q", f.lines[0], want)
	}
	for _, leak := range []string{"hunter2", "root", "guess", "secret"} {
		if strings.Contains(f.lines[0], leak) {
			t.Errorf("the line leaks %q: %q", leak, f.lines[0])
		}
	}
	if got := auth.Users(); len(got) != 1 || got[0] != "alice" {
		t.Errorf("Users() = %v", got)
	}
	if f.log.wrapAuth(nil) != nil {
		t.Error("a nil authenticator (no login required) must stay nil")
	}
}

func TestConnLogRunFlushesOnTickAndAtTheEnd(t *testing.T) {
	f := newConnLogFixture()
	lines := make(chan string, 64)
	write := func(format string, args ...any) { lines <- fmt.Sprintf(format, args...) }
	f.log.info, f.log.warn = write, write
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.log.run(ctx, 20*time.Millisecond)
		close(done)
	}()
	f.dialFail("alice", "main/us", "timeout", 3)
	select {
	case line := <-lines:
		if !strings.Contains(line, "account alice via main/us") {
			t.Fatalf("line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no report within a few periods")
	}

	f.dialFail("bob", "own/nl", "timeout", 3)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the end")
	}
	// Whether a tick or the final flush wrote it, bob's period is in the log.
	close(lines)
	found := false
	for line := range lines {
		if strings.Contains(line, "account bob via own/nl") {
			found = true
		}
	}
	if !found {
		t.Fatal("the last period was not written when run ended")
	}
}
