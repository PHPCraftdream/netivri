package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/buf"
	C "github.com/metacubex/mihomo/constant"
)

type panicReadConn struct{ *scriptConn }

func (c panicReadConn) Read([]byte) (int, error) {
	c.panicRead()
	return 0, nil
}

type panicWriteConn struct{ *scriptConn }

func (c panicWriteConn) Write([]byte) (int, error) {
	c.panicWrite()
	return 0, nil
}

// overflow panics the way vision.ApplyPadding did: a header written in front
// of a buffer that has no room there.
func overflow() { buf.As([]byte("x")).ExtendHeader(2) }

func TestWatchConnWriteBufferPanicBecomesAnErrorAndAnEvent(t *testing.T) {
	inner := &scriptConn{panicWrite: overflow}
	w, sink := watched(t, inner)

	err := w.WriteBuffer(buf.As([]byte("data")))
	if !errors.Is(err, errConnPanicked) {
		t.Fatalf("WriteBuffer = %v, want errConnPanicked", err)
	}
	_ = w.Close()

	var panicked []ConnEvent
	for _, ev := range sink.all() {
		if ev.Kind == ConnPanicked {
			panicked = append(panicked, ev)
		}
	}
	if len(panicked) != 1 {
		t.Fatalf("panic events = %d, want 1: %+v", len(panicked), sink.all())
	}
	ev := panicked[0]
	if ev.Login != "alice" || ev.Label != "own/a" {
		t.Fatalf("event = %+v", ev)
	}
	for _, part := range []string{"buffer overflow", "ExtendHeader", "overflow"} {
		if !strings.Contains(ev.Why, part) {
			t.Errorf("Why %q lacks %q", ev.Why, part)
		}
	}
	if strings.Contains(ev.Why, "guard") || strings.Contains(ev.Why, `\`) || strings.Contains(ev.Why, "/") {
		t.Errorf("Why names the guard or a directory: %q", ev.Why)
	}
	// the connection still ends with its own event, counted as broken
	var ended *ConnEvent
	for _, e := range sink.all() {
		if e.Kind == ConnStreamEnded {
			ended = &e
		}
	}
	if ended == nil || ended.Why == "" {
		t.Fatalf("stream_ended = %+v, want a broken end", ended)
	}
}

func TestWatchConnReadBufferPanicBecomesAnError(t *testing.T) {
	inner := &scriptConn{panicRead: func() { panic("boom") }}
	w, sink := watched(t, inner)
	b := buf.New()
	defer b.Release()
	if err := w.ReadBuffer(b); !errors.Is(err, errConnPanicked) {
		t.Fatalf("ReadBuffer = %v", err)
	}
	var got string
	for _, ev := range sink.all() {
		if ev.Kind == ConnPanicked {
			got = ev.Why
		}
	}
	if !strings.HasPrefix(got, "boom at ") {
		t.Fatalf("Why = %q, want 'boom at …'", got)
	}
}

func TestWatchConnReadWritePanicReturnsNoBytesAndReportsOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		panic func()
		call  func(*watchConn) (int, error)
	}{
		{
			name:  "read",
			panic: func() { panic("read boom") },
			call: func(w *watchConn) (int, error) {
				return w.Read(make([]byte, 8))
			},
		},
		{
			name:  "write",
			panic: func() { panic("write boom") },
			call: func(w *watchConn) (int, error) {
				return w.Write([]byte("payload"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &scriptConn{}
			var conn C.Conn
			if tc.name == "read" {
				inner.panicRead = tc.panic
				conn = panicReadConn{inner}
			} else {
				inner.panicWrite = tc.panic
				conn = panicWriteConn{inner}
			}
			sink := &eventSink{}
			g := &stickyGroup{events: sink.add}
			w, ok := g.watch(conn, "alice", "own/a", 0, nil).(*watchConn)
			if !ok {
				t.Fatal("watch did not wrap the connection")
			}
			n, err := tc.call(w)
			if n != 0 || !errors.Is(err, errConnPanicked) {
				t.Fatalf("operation = %d, %v; want 0, errConnPanicked", n, err)
			}
			if w.up.Load() != 0 || w.down.Load() != 0 {
				t.Fatalf("counts up=%d down=%d, want zero", w.up.Load(), w.down.Load())
			}
			_ = w.Close()
			var panics, ended int
			var end ConnEvent
			for _, ev := range sink.all() {
				switch ev.Kind {
				case ConnPanicked:
					panics++
				case ConnStreamEnded:
					ended++
					end = ev
				}
			}
			if panics != 1 {
				t.Fatalf("panic events = %d, want 1: %+v", panics, sink.all())
			}
			if ended != 1 || end.Why == "" {
				t.Fatalf("stream end = %+v, count %d, want one broken end", end, ended)
			}
			if inner.closes != 1 {
				t.Fatalf("inner closes = %d, want 1", inner.closes)
			}
		})
	}
}

func TestWatchConnWithoutPanicReportsNothing(t *testing.T) {
	inner := &scriptConn{}
	w, sink := watched(t, inner)
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Read(make([]byte, 8)); n != 5 || err != nil {
		t.Fatalf("Read = %d, %v; want 5, nil", n, err)
	}
	if err := w.WriteBuffer(buf.As([]byte("ok"))); err != nil {
		t.Fatal(err)
	}
	b := buf.New()
	defer b.Release()
	if err := w.ReadBuffer(b); err != nil {
		t.Fatal(err)
	}
	for _, ev := range sink.all() {
		if ev.Kind == ConnPanicked {
			t.Fatalf("unexpected panic event %+v", ev)
		}
	}
}

func TestPanicSummaryCutsALongValue(t *testing.T) {
	long := strings.Repeat("y", 500) + "\nsecond line"
	got := panicSummary(long)
	if strings.Contains(got, "\n") {
		t.Fatalf("not one line: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("y", panicTextMax)+"...") {
		t.Fatalf("not cut at %d: %q", panicTextMax, got[:200])
	}
}
