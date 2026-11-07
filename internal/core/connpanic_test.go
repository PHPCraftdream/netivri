package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/buf"
)

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

func TestWatchConnWithoutPanicReportsNothing(t *testing.T) {
	inner := &scriptConn{}
	w, sink := watched(t, inner)
	if err := w.WriteBuffer(buf.As([]byte("ok"))); err != nil {
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
