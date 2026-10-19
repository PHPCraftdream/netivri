//go:build integration

// Package testutil integration-only helpers. This file is part of the
// same testutil package but is only compiled when the integration build
// tag is set, so helpers touching the network stay out of unit builds.
package testutil

import (
	"net"
	"testing"
)

// FreeLoopbackAddr returns a free TCP address on 127.0.0.1 by asking the
// kernel for an ephemeral port. The address may be reused by other
// processes afterwards; bind immediately after calling.
func FreeLoopbackAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback address: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}
