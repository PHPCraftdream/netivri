//go:build integration

package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

const brokerTestTimeout = 3 * time.Second

func brokerTCPAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func brokerUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func brokerListenRetry(t *testing.T, b *socketBroker, preferred string) (*socketLease, *socketStage, string) {
	t.Helper()
	addr := preferred
	for attempt := 0; attempt < 3; attempt++ {
		if addr == "" {
			addr = brokerTCPAddress(t)
		}
		stage := b.stage()
		listener, err := stage.Listen(context.Background(), "tcp", addr)
		if err == nil {
			return listener.(*socketLease), stage, addr
		}
		stage.rollback()
		if !isAddrInUse(err) {
			t.Fatalf("broker Listen %s: %v", addr, err)
		}
		addr = ""
	}
	t.Fatal("broker Listen: address remained in use after 3 attempts")
	return nil, nil, ""
}

func brokerAccept(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		ch <- result{c, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("Accept: %v", got.err)
		}
		if got.conn != nil {
			if err := got.conn.SetDeadline(time.Now().Add(brokerTestTimeout)); err != nil {
				_ = got.conn.Close()
				t.Fatalf("set accepted connection deadline: %v", err)
			}
			t.Cleanup(func() { _ = got.conn.Close() })
		}
		return got.conn
	case <-time.After(brokerTestTimeout):
		t.Fatal("timed out waiting for Accept")
		return nil
	}
}

func brokerDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, brokerTestTimeout)
	if err != nil {
		t.Fatalf("DialTimeout: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(brokerTestTimeout)); err != nil {
		_ = c.Close()
		t.Fatalf("set client connection deadline: %v", err)
	}
	return c
}

func TestSocketBrokerTCPStageReplacementAndRollback(t *testing.T) {
	b := newSocketBroker()
	t.Cleanup(b.close)
	old, oldStage, addr := brokerListenRetry(t, b, "")
	t.Cleanup(func() { _ = old.Close() })
	if err := oldStage.commit(); err != nil {
		t.Fatal(err)
	}

	newLease, staged, addr := brokerListenRetry(t, b, addr)
	if newLease.endpoint != old.endpoint {
		t.Fatal("staged replacement did not reuse the physical listener")
	}

	client := brokerDial(t, addr)
	server := brokerAccept(t, old)
	_ = client.Close()
	_ = server.Close()
	staged.rollback()

	client = brokerDial(t, addr)
	server = brokerAccept(t, old)
	_ = client.Close()
	_ = server.Close()

	replacement, replacementStage, replacementAddr := brokerListenRetry(t, b, addr)
	if replacementAddr != addr {
		t.Fatalf("replacement address = %s, want %s", replacementAddr, addr)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if replacement.endpoint != old.endpoint {
		t.Fatal("replacement did not reuse the physical listener")
	}
	if err := replacementStage.commit(); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	client = brokerDial(t, addr)
	server = brokerAccept(t, replacement)
	_ = client.Close()
	_ = server.Close()
	_ = replacement.Close()

	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("physical TCP listener remained bound after final lease close: %v", err)
	}
	_ = probe.Close()
}

func TestSocketBrokerListenPacketUnsupportedWithoutBinding(t *testing.T) {
	b := newSocketBroker()
	defer b.close()
	for attempt := 0; attempt < 3; attempt++ {
		addr := brokerUDPAddress(t)
		_, err := b.stage().ListenPacket(context.Background(), "udp", addr)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
			t.Fatalf("ListenPacket error = %v, want clear unsupported error", err)
		}
		probe, err := net.ListenPacket("udp", addr)
		if err != nil {
			if isAddrInUse(err) {
				continue
			}
			t.Fatalf("UDP bind %s after unsupported ListenPacket: %v", addr, err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("could not acquire an unused UDP address after 3 attempts")
}

func TestSocketBrokerRollbackClosesLeaseAndUnblocksAccept(t *testing.T) {
	b := newSocketBroker()
	defer b.close()
	stage := b.stage()
	listener, err := stage.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	accepted := make(chan error, 1)
	go func() { _, err := listener.Accept(); accepted <- err }()
	stage.rollback()
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(brokerTestTimeout):
		t.Fatal("Accept remained blocked after rollback")
	}
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("TCP listener remained bound after rollback: %v", err)
	}
	_ = probe.Close()
}

func TestSocketBrokerCloseUnblocksAcceptAndIsIdempotent(t *testing.T) {
	b := newSocketBroker()
	listener, err := b.stage().Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() { _, err := listener.Accept(); accepted <- err }()
	b.close()
	b.close()
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(brokerTestTimeout):
		t.Fatal("Accept remained blocked after broker close")
	}
}
