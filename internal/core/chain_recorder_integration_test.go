//go:build integration

package core

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// chainRecorder is a no-auth SOCKS5 server recording requested destinations.
// It relays the stream to permit chaining recorders as independent hops.
type chainRecorder struct {
	mu      sync.Mutex
	targets []string
}

func startChainRecorder(t *testing.T) (*chainRecorder, string) {
	t.Helper()
	r := &chainRecorder{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("chain recorder listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.serve(c)
		}
	}()
	return r, ln.Addr().String()
}
func (r *chainRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}
func (r *chainRecorder) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(testTimeout))
	h, ok := readN(c, 2)
	if !ok || h[0] != 5 {
		return
	}
	if _, ok = readN(c, int(h[1])); !ok {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req, ok := readN(c, 4)
	if !ok || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b, ok := readN(c, 4)
		if !ok {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, ok := readN(c, 1)
		if !ok {
			return
		}
		b, ok := readN(c, int(n[0]))
		if !ok {
			return
		}
		host = string(b)
	case 4:
		b, ok := readN(c, 16)
		if !ok {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p, ok := readN(c, 2)
	if !ok {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	r.mu.Lock()
	r.targets = append(r.targets, dest)
	r.mu.Unlock()
	up, err := net.DialTimeout("tcp", dest, testTimeout)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}
