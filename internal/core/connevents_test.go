package core

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/buf"
	C "github.com/metacubex/mihomo/constant"
)

// scriptConn is a C.Conn whose Read and Write return the scripted errors.
type scriptConn struct {
	fakeConn
	readErr, writeErr error
	deadlines         []time.Time
	closes            int
}

func (c *scriptConn) Read(b []byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}
	return copy(b, "hello"), nil
}
func (c *scriptConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(b), nil
}
func (c *scriptConn) ReadBuffer(b *buf.Buffer) error {
	if c.readErr != nil {
		return c.readErr
	}
	_, _ = b.Write([]byte("0123456789"))
	return nil
}
func (c *scriptConn) WriteBuffer(b *buf.Buffer) error { return c.writeErr }
func (c *scriptConn) SetReadDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	return nil
}
func (c *scriptConn) SetDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	return nil
}
func (c *scriptConn) Close() error { c.closes++; return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type eventSink struct {
	mu     sync.Mutex
	events []ConnEvent
}

func (s *eventSink) add(ev ConnEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *eventSink) all() []ConnEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ConnEvent(nil), s.events...)
}

func watched(t *testing.T, inner *scriptConn) (*watchConn, *eventSink) {
	t.Helper()
	sink := &eventSink{}
	g := &stickyGroup{events: sink.add}
	w, ok := g.watch(inner, "alice", "own/a").(*watchConn)
	if !ok {
		t.Fatal("watch did not wrap the connection")
	}
	return w, sink
}

func TestWatchConnCleanEndCountsBytes(t *testing.T) {
	inner := &scriptConn{}
	w, sink := watched(t, inner)
	b := make([]byte, 16)
	if n, err := w.Read(b); n != 5 || err != nil {
		t.Fatalf("Read = %d, %v", n, err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	rb := buf.New()
	defer rb.Release()
	if err := w.ReadBuffer(rb); err != nil {
		t.Fatal(err)
	}
	wb := buf.As([]byte("0123"))
	if err := w.WriteBuffer(wb); err != nil {
		t.Fatal(err)
	}
	inner.readErr = io.EOF
	if _, err := w.Read(b); !errors.Is(err, io.EOF) {
		t.Fatalf("Read at the end = %v", err)
	}
	_ = w.Close()
	_ = w.Close() // closing twice reports once

	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("%d events, want 1: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Kind != ConnStreamEnded || ev.Login != "alice" || ev.Label != "own/a" || ev.Why != "" {
		t.Errorf("event = %+v, want a clean end of alice via own/a", ev)
	}
	if ev.Down != 5+10 || ev.Up != 3+4 {
		t.Errorf("bytes down %d up %d, want 15 and 7", ev.Down, ev.Up)
	}
	if inner.closes != 2 {
		t.Errorf("the inner connection was closed %d times, want every Close passed on", inner.closes)
	}
}

func TestWatchConnNamesTheFirstError(t *testing.T) {
	inner := &scriptConn{readErr: errors.New("read tcp 10.0.0.1:1->10.0.0.2:443: connection reset by peer")}
	w, sink := watched(t, inner)
	_, _ = w.Read(make([]byte, 4))
	inner.writeErr = errors.New("write: broken pipe")
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	got := sink.all()
	if len(got) != 1 || got[0].Why != "connection reset" {
		t.Fatalf("events = %+v, want one ended by 'connection reset' (the first error)", got)
	}
}

func TestWatchConnIgnoresTheEndsThatAreNotTheNodesFault(t *testing.T) {
	for name, err := range map[string]error{
		"EOF":                    io.EOF,
		"closed by the proxy":    net.ErrClosed,
		"closed pipe":            io.ErrClosedPipe,
		"closed network message": errors.New("read tcp: use of closed network connection"),
	} {
		inner := &scriptConn{readErr: err}
		w, sink := watched(t, inner)
		_, _ = w.Read(make([]byte, 4))
		_ = w.Close()
		if got := sink.all(); len(got) != 1 || got[0].Why != "" {
			t.Errorf("%s: events = %+v, want a clean end", name, got)
		}
	}
}

func TestWatchConnDeadlineToInterruptIsNotATimeout(t *testing.T) {
	// A real timeout counts.
	inner := &scriptConn{readErr: timeoutErr{}}
	w, sink := watched(t, inner)
	_, _ = w.Read(make([]byte, 4))
	_ = w.Close()
	if got := sink.all(); len(got) != 1 || got[0].Why != "timeout" {
		t.Fatalf("a timeout on a connection nobody interrupted: %+v", got)
	}

	// The relay sets a deadline in the past to stop the reader of the other
	// direction; the timeout that follows is the end of the connection.
	inner = &scriptConn{readErr: timeoutErr{}}
	w, sink = watched(t, inner)
	_ = w.SetReadDeadline(time.Now())
	_, _ = w.Read(make([]byte, 4))
	_ = w.Close()
	if got := sink.all(); len(got) != 1 || got[0].Why != "" {
		t.Fatalf("a timeout after the interrupting deadline counted: %+v", got)
	}
	if len(inner.deadlines) != 1 {
		t.Errorf("the deadline was not passed on: %v", inner.deadlines)
	}

	// A deadline far in the future is an ordinary idle timeout.
	inner = &scriptConn{readErr: timeoutErr{}}
	w, sink = watched(t, inner)
	_ = w.SetDeadline(time.Now().Add(time.Hour))
	_, _ = w.Read(make([]byte, 4))
	_ = w.Close()
	if got := sink.all(); len(got) != 1 || got[0].Why != "timeout" {
		t.Fatalf("a timeout under a far deadline: %+v", got)
	}
}

func TestWatchOnlyWrapsWhenSomeoneListens(t *testing.T) {
	inner := &scriptConn{}
	g := &stickyGroup{}
	if got := g.watch(inner, "alice", "own/a"); got != C.Conn(inner) {
		t.Fatal("a connection was wrapped with no sink")
	}
}

func TestDialErrKindIsAFixedWord(t *testing.T) {
	for in, want := range map[string]string{
		"dial tcp 10.0.0.1:443: i/o timeout":      "timeout",
		"connect: connection refused":             "connection refused",
		"dial tcp secret-host: weird failure xyz": "error",
	} {
		if got := dialErrKind(errors.New(in)); got != want {
			t.Errorf("dialErrKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func eventGroup(t *testing.T, members []*fakeMember) (*stickyGroup, *eventSink, *healthTable) {
	t.Helper()
	g, _, health := newTestGroup(t, members, nil)
	sink := &eventSink{}
	g.events = sink.add
	return g, sink, health
}

func TestDialFailureIsReported(t *testing.T) {
	m := &fakeMember{name: "own/a", err: errors.New("connect: connection refused")}
	g, sink, _ := eventGroup(t, []*fakeMember{m})
	if _, err := g.DialContext(context.Background(), dialMetadata("alice", "site.example")); err == nil {
		t.Fatal("dial through a dead member succeeded")
	}
	got := sink.all()
	if len(got) != 1 || got[0].Kind != ConnDialFailed || got[0].Login != "alice" || got[0].Label != "own/a" || got[0].Why != "connection refused" {
		t.Fatalf("events = %+v, want one dial failure of alice via own/a (connection refused)", got)
	}
}

func TestSuccessfulDialIsWrappedAndReportedOnClose(t *testing.T) {
	m := &fakeMember{name: "own/a"}
	g, sink, _ := eventGroup(t, []*fakeMember{m})
	conn, err := g.DialContext(context.Background(), dialMetadata("alice", "site.example"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.all()) != 0 {
		t.Fatalf("an event before the connection ended: %+v", sink.all())
	}
	_ = conn.Close()
	got := sink.all()
	if len(got) != 1 || got[0].Kind != ConnStreamEnded || got[0].Label != "own/a" {
		t.Fatalf("events = %+v, want the end of the connection via own/a", got)
	}
	if chains := conn.Chains(); len(chains) == 0 || chains[len(chains)-1] != "netivri-group-0" {
		t.Errorf("the wrapper lost the chain: %v", chains)
	}
}

func TestMovesOfSitesAreReported(t *testing.T) {
	g, table, health, clock := tierReturnGroup(t, []int{0, 1}, time.Minute)
	sink := &eventSink{}
	g.events = sink.add
	_ = table

	nodeDown(health, "n0")
	chosen(t, g, "one.example") // a new binding is no move
	if len(sink.all()) != 0 {
		t.Fatalf("a new binding was reported: %+v", sink.all())
	}
	nodeUp(health, "n0")
	clock.advance(2 * time.Minute)
	chosen(t, g, "one.example") // back to the better tier
	got := sink.all()
	if len(got) != 1 || got[0].Kind != ConnSiteMoved || got[0].From != "n1" || got[0].Label != "n0" || got[0].Why != MoveBetterTier {
		t.Fatalf("events = %+v, want one move n1 -> n0 (better tier)", got)
	}

	nodeDown(health, "n0")
	chosen(t, g, "one.example") // the node went down under the binding
	got = sink.all()
	if len(got) != 2 || got[1].From != "n0" || got[1].Label != "n1" || got[1].Why != MoveNodeDown {
		t.Fatalf("events = %+v, want a second move n0 -> n1 (node down)", got)
	}
	if got[1].Login != "alice" {
		t.Errorf("login = %q, want alice", got[1].Login)
	}
}
