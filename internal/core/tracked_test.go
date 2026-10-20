package core

import (
	"io"
	"net"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// fakeTunnel blocks in HandleTCPConn until the connection ends (a read
// error), like Mihomo's tunnel piping a connection.
type fakeTunnel struct {
	entered chan struct{}
}

func (f *fakeTunnel) HandleTCPConn(conn net.Conn, _ *C.Metadata) {
	f.entered <- struct{}{}
	_, _ = io.Copy(io.Discard, conn)
}

func (f *fakeTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (f *fakeTunnel) NatTable() C.NatTable                     { return nil }

func TestTrackedTunnelCloseAllClosesConnsAndWaits(t *testing.T) {
	ft := &fakeTunnel{entered: make(chan struct{}, 1)}
	tt := newTrackedTunnel(ft)

	server, client := net.Pipe()
	defer client.Close()
	handlerDone := make(chan struct{})
	go func() {
		tt.HandleTCPConn(server, nil)
		close(handlerDone)
	}()
	<-ft.entered // the handler is registered and blocked

	start := time.Now()
	tt.closeAll(5 * time.Second)
	if elapsed := time.Since(start); elapsed >= 4*time.Second {
		t.Fatalf("closeAll took %v: it must return once the closed connection ends the handler", elapsed)
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("handler still running after closeAll returned")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("the tracked connection was not closed")
	}
}

func TestTrackedTunnelRefusesNewConnsAfterClose(t *testing.T) {
	ft := &fakeTunnel{entered: make(chan struct{}, 1)}
	tt := newTrackedTunnel(ft)
	tt.closeAll(time.Second)

	server, client := net.Pipe()
	defer client.Close()
	tt.HandleTCPConn(server, nil) // must return immediately without entering the inner tunnel
	select {
	case <-ft.entered:
		t.Fatal("a connection was handled after closeAll")
	default:
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("the refused connection was not closed")
	}
}

// A handler that ignores the closed connection must not hang closeAll
// beyond its timeout.
func TestTrackedTunnelCloseAllTimeout(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	tt := newTrackedTunnel(blockingTunnel{release: release, entered: entered})

	server, client := net.Pipe()
	defer client.Close()
	go tt.HandleTCPConn(server, nil)
	<-entered // registered before the inner tunnel is entered

	start := time.Now()
	tt.closeAll(100 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("closeAll took %v, want about its 100ms timeout", elapsed)
	}
	close(release) // let the stuck handler finish
}

type blockingTunnel struct{ release, entered chan struct{} }

func (b blockingTunnel) HandleTCPConn(net.Conn, *C.Metadata) {
	b.entered <- struct{}{}
	<-b.release
}
func (b blockingTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (b blockingTunnel) NatTable() C.NatTable                     { return nil }
