package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

type probeHop struct {
	err  error
	conn net.Conn
	call int
}

func (h *probeHop) Key() string { return "probe" }
func (h *probeHop) DialContext(context.Context, string, string) (net.Conn, error) {
	h.call++
	return h.conn, h.err
}

type probeConn struct {
	closed     bool
	closeCount int
}

func (c *probeConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *probeConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *probeConn) Close() error {
	c.closed = true
	c.closeCount++
	return nil
}
func (c *probeConn) LocalAddr() net.Addr              { return probeAddr("local") }
func (c *probeConn) RemoteAddr() net.Addr             { return probeAddr("remote") }
func (c *probeConn) SetDeadline(time.Time) error      { return nil }
func (c *probeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *probeConn) SetWriteDeadline(time.Time) error { return nil }

type probeAddr string

func (a probeAddr) Network() string { return "memory" }
func (a probeAddr) String() string  { return string(a) }

func TestProberOutcomesAndClosesSuccessfulConnection(t *testing.T) {
	prober := New("tcp", "target:443")
	conn := &probeConn{}
	hop := &probeHop{conn: conn}
	if got := prober.Probe(context.Background(), hop); got != pool.ProbeSuccess {
		t.Fatalf("success outcome = %v", got)
	}
	if !conn.closed || conn.closeCount != 1 {
		t.Fatalf("successful probe connection closed=%v count=%d, want true/1", conn.closed, conn.closeCount)
	}
	if hop.call != 1 {
		t.Fatalf("dial calls = %d, want one", hop.call)
	}
	for _, test := range []struct {
		name string
		err  error
		want pool.ProbeOutcome
	}{
		{name: "connect hard", err: &dial.StageError{Stage: dial.Connect, Err: errors.New("unreachable")}, want: pool.ProbeHardFailure},
		{name: "handshake hard", err: &dial.StageError{Stage: dial.Handshake}, want: pool.ProbeHardFailure},
		{name: "next unreachable handshake inconclusive", err: &dial.StageError{Stage: dial.Handshake, NextUnreachable: true}, want: pool.ProbeInconclusive},
		{name: "target inconclusive", err: &dial.StageError{Stage: dial.Target, NextUnreachable: true}, want: pool.ProbeInconclusive},
		{name: "refusal inconclusive", err: &dial.StageError{Stage: dial.Refusal}, want: pool.ProbeInconclusive},
		{name: "unknown stage inconclusive", err: &dial.StageError{Stage: dial.Unknown}, want: pool.ProbeInconclusive},
		{name: "raw error inconclusive", err: errors.New("network failure"), want: pool.ProbeInconclusive},
		{name: "canceled stage inconclusive", err: &dial.StageError{Stage: dial.Handshake, Err: fmt.Errorf("wrapped: %w", context.Canceled)}, want: pool.ProbeInconclusive},
		{name: "deadline stage inconclusive", err: &dial.StageError{Stage: dial.Handshake, Err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)}, want: pool.ProbeInconclusive},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := prober.Probe(context.Background(), &probeHop{err: test.err}); got != test.want {
				t.Fatalf("outcome = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProberNilConnectionIsInconclusive(t *testing.T) {
	hop := &probeHop{}
	if got := New("tcp", "target:443").Probe(context.Background(), hop); got != pool.ProbeInconclusive {
		t.Fatalf("nil connection outcome = %v, want inconclusive", got)
	}
	if hop.call != 1 {
		t.Fatalf("dial calls = %d, want one", hop.call)
	}
}

func TestProberClosesConnectionReturnedWithError(t *testing.T) {
	conn := &probeConn{}
	hop := &probeHop{conn: conn, err: &dial.StageError{Stage: dial.Handshake, NextUnreachable: true}}
	if got := New("tcp", "target:443").Probe(context.Background(), hop); got != pool.ProbeInconclusive {
		t.Fatalf("outcome = %v, want inconclusive", got)
	}
	if !conn.closed || conn.closeCount != 1 {
		t.Fatalf("error connection closed=%v count=%d, want true/1", conn.closed, conn.closeCount)
	}
}

func TestProberCanceledContextIsInconclusive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hop := &probeHop{}
	if got := New("tcp", "target:443").Probe(ctx, hop); got != pool.ProbeInconclusive {
		t.Fatalf("outcome = %v, want inconclusive", got)
	}
	if hop.call != 0 {
		t.Fatalf("dial called %d times with canceled context", hop.call)
	}
}
