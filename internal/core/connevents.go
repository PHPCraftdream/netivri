package core

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/common/buf"
	C "github.com/metacubex/mihomo/constant"
	N "github.com/metacubex/sing/common/network"
)

// Kinds of ConnEvent.
const (
	// ConnDialFailed: a dial through a node failed; Why is the kind of error.
	ConnDialFailed = "dial_failed"
	// ConnStreamEnded: a connection through a node ended; Why is the kind of
	// the first error that broke it ("" when it ended without one).
	ConnStreamEnded = "stream_ended"
	// ConnSiteMoved: a site of an account was bound to another node; From is
	// the node it left, Label the node it goes to and Why is MoveNodeDown or
	// MoveBetterTier.
	ConnSiteMoved = "site_moved"
)

// Why of a ConnSiteMoved.
const (
	MoveNodeDown   = "node down"
	MoveBetterTier = "better tier"
)

// ConnEvent is one thing that happened to a connection of an account, for
// Options.OnConnEvent. It never holds a site, an address or an error text:
// only the account, the node labels and a kind from a fixed list.
type ConnEvent struct {
	Kind  string
	Login string
	Label string // the node
	From  string // ConnSiteMoved: the node the site left
	Why   string
	// ConnStreamEnded only: how long the connection lived and the bytes that
	// went up to and came back from the node.
	Lifetime time.Duration
	Up, Down int64
}

// dialErrKind names the kind of a dial or stream error from a fixed list (the
// kinds of a failed probe, with every unknown error just "error"), so that a
// log line never carries the text of an error, which may name a destination.
func dialErrKind(err error) string {
	kind := probeFailKind(err)
	if strings.HasPrefix(kind, "error") {
		return "error"
	}
	return kind
}

// emit reports an event; the group has no sink when events is nil.
func (g *stickyGroup) emit(ev ConnEvent) {
	if g.events != nil {
		g.events(ev)
	}
}

// watch wraps the connection a node returned so that its end is reported, or
// returns it as it is when nobody listens.
func (g *stickyGroup) watch(conn C.Conn, login, label string) C.Conn {
	if g.events == nil {
		return conn
	}
	return &watchConn{Conn: conn, events: g.events, login: login, label: label, start: time.Now()}
}

// watchConn reports one ConnStreamEnded when the connection is closed: the
// bytes that moved, how long it lived and the kind of the first error that
// came from the node's side. A clean end (EOF), the closing of the
// connection by the proxy itself and the read deadline that the relay sets to
// interrupt the other direction are not errors.
type watchConn struct {
	C.Conn
	events       func(ConnEvent)
	login, label string
	start        time.Time

	up, down    atomic.Int64
	interrupted atomic.Bool
	failure     atomic.Pointer[string] // the first error kind
	once        sync.Once
}

func (w *watchConn) Read(b []byte) (int, error) {
	n, err := w.Conn.Read(b)
	w.down.Add(int64(n))
	w.fail(err)
	return n, err
}

func (w *watchConn) Write(b []byte) (int, error) {
	n, err := w.Conn.Write(b)
	w.up.Add(int64(n))
	w.fail(err)
	return n, err
}

func (w *watchConn) ReadBuffer(b *buf.Buffer) error {
	before := b.Len()
	err := w.Conn.ReadBuffer(b)
	if got := b.Len() - before; got > 0 {
		w.down.Add(int64(got))
	}
	w.fail(err)
	return err
}

func (w *watchConn) WriteBuffer(b *buf.Buffer) error {
	size := b.Len()
	err := w.Conn.WriteBuffer(b)
	if err == nil {
		w.up.Add(int64(size))
	}
	w.fail(err)
	return err
}

// SetReadDeadline and SetDeadline notice a deadline that is already due: the
// relay sets one to stop the reader of the other direction, and the timeout
// that follows is the end of the connection, not a fault of the node.
func (w *watchConn) SetReadDeadline(t time.Time) error {
	w.noteDeadline(t)
	return w.Conn.SetReadDeadline(t)
}

func (w *watchConn) SetDeadline(t time.Time) error {
	w.noteDeadline(t)
	return w.Conn.SetDeadline(t)
}

func (w *watchConn) noteDeadline(t time.Time) {
	if !t.IsZero() && time.Until(t) < time.Second {
		w.interrupted.Store(true)
	}
}

// Upstream lets sing and Mihomo see through the wrapper: the headroom a
// protocol layer below needs in front of the data (VLESS vision asks for 2
// bytes, and a buffer without it panics) is found by walking the upstream
// chain, as is a handshake report. The wrapper is deliberately not
// "replaceable", so a copy still goes through Read and Write and is counted.
func (w *watchConn) Upstream() any { return w.Conn }

// ReadCached hands on the bytes the connection below already holds (the
// copy loops ask the source for them first); nil when there are none.
func (w *watchConn) ReadCached() *buf.Buffer {
	if cached, ok := w.Conn.(N.CachedReader); ok {
		if b := cached.ReadCached(); b != nil {
			w.down.Add(int64(b.Len()))
			return b
		}
	}
	return nil
}

func (w *watchConn) Close() error {
	err := w.Conn.Close()
	w.once.Do(func() {
		ev := ConnEvent{Kind: ConnStreamEnded, Login: w.login, Label: w.label,
			Lifetime: time.Since(w.start), Up: w.up.Load(), Down: w.down.Load()}
		if kind := w.failure.Load(); kind != nil {
			ev.Why = *kind
		}
		w.events(ev)
	})
	return err
}

// fail remembers the kind of the first error that is a fault of the node's
// side of the connection.
func (w *watchConn) fail(err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return
	}
	var netErr net.Error
	if w.interrupted.Load() && errors.As(err, &netErr) && netErr.Timeout() {
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
		return
	}
	kind := dialErrKind(err)
	w.failure.CompareAndSwap(nil, &kind)
}
