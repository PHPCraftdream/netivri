//go:build integration

package testutil

import (
	"net"
	"testing"
)

func TestFreeLoopbackAddr(t *testing.T) {
	addr := FreeLoopbackAddr(t)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", host)
	}
	if port == "" || port == "0" {
		t.Fatalf("port = %q, want an ephemeral port", port)
	}

	// The address must be bindable again: proves the helper closed its
	// listener before returning.
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebind %q: %v", addr, err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close rebound listener: %v", err)
	}
}
