//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type integrationDialer struct{ count atomic.Int32 }

func (d *integrationDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.count.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (*integrationDialer) ListenPacket(context.Context, string, string, netip.AddrPort) (net.PacketConn, error) {
	return nil, io.ErrClosedPipe
}

func TestPoolDialerReusesWarmSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	dialer := &integrationDialer{}
	ctx, cancel := context.WithCancel(context.Background())
	pool := &poolDialer{addr: listener.Addr().String(), size: 1, ttl: time.Minute, underlying: dialer, wake: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel, owner: &warmer{}}
	pool.start()
	t.Cleanup(func() { pool.close() })
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		pool.mu.Lock()
		ready := len(pool.idle) == 1
		pool.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("refill did not park a connection")
		}
	}
	conn, err := pool.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err = conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 1)
	if _, err = io.ReadFull(conn, got); err != nil || got[0] != 'x' {
		t.Fatalf("echo %q: %v", got, err)
	}
	if pool.owner.poolHits.Load() == 0 {
		t.Fatal("warm socket was not reused")
	}
}

func TestWarmStartActualSOCKSProbes(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	startNodeInstance(t, nil, nodeMap(addr))
	lifecycleMu.Lock()
	w, err := startWarm(WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, nodeMap(addr))
	lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for w.Stats().Probes == 0 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("probe did not finish")
		}
	}
	s := w.Stats()
	if s.ProbeFailures == 0 || len(s.Nodes) == 0 || s.Nodes[0].Alive {
		t.Fatalf("unexpected dead probe stats: %#v", s)
	}
}

func TestPoolAdapterRoundTripAndClose(t *testing.T) {
	f, nodeAddr := startFakeUpstream(t)
	inst := startNodeInstance(t, nil, nodeMap(nodeAddr))
	probeTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(probeTarget.Close)
	f.mapDomain("probe.invalid", probeTarget.Listener.Addr().String())
	lifecycleMu.Lock()
	w, err := startWarm(WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/", PoolSize: 1}, nodeMap(nodeAddr))
	lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		w.pool.mu.Lock()
		ready := len(w.pool.idle) > 0
		w.pool.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("pool did not refill")
		}
	}
	backend, _ := countEchoServer(t)
	c := socks5ConnectNoAuth(t, inst.Addr(), backend)
	echoRoundTrip(t, c, c)
	_ = c.Close()
	if w.Stats().PoolHits == 0 {
		t.Fatalf("pool miss: %#v", w.Stats())
	}
	w.Close()
	w.pool.mu.Lock()
	remaining := len(w.pool.idle)
	w.pool.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pool retained %d sockets after Close", remaining)
	}
	if len(f.recorded()) == 0 {
		t.Fatal("upstream did not see node roundtrip")
	}
}

func TestPoolDialerDiscardsClosedPeer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	dialer := &integrationDialer{}
	ctx, cancel := context.WithCancel(context.Background())
	pool := &poolDialer{addr: listener.Addr().String(), size: 1, underlying: dialer, wake: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel, owner: &warmer{}}
	pool.start()
	defer pool.close()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		pool.mu.Lock()
		ready := len(pool.idle) == 1
		pool.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("socket not refilled")
		}
	}
	select {
	case remote := <-accepted:
		_ = remote.Close()
	case <-time.After(time.Second):
		t.Fatal("upstream did not accept pool connection")
	}
	// The FIN of the closed peer needs a moment to arrive: re-run the expiry
	// check until the idle socket is discarded.
	discardDeadline := time.NewTimer(3 * time.Second)
	defer discardDeadline.Stop()
	discardPoll := time.NewTicker(5 * time.Millisecond)
	defer discardPoll.Stop()
	for pool.owner.poolDiscards.Load() == 0 {
		pool.expire()
		select {
		case <-discardPoll.C:
		case <-discardDeadline.C:
			t.Fatal("closed idle peer was not discarded")
		}
	}
}
