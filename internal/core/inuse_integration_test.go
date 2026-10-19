//go:build integration

package core

import (
	"errors"
	"net"
	"testing"
)

func TestIsAddrInUse(t *testing.T) {
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer l1.Close()

	l2, err := net.Listen("tcp", l1.Addr().String())
	if err == nil {
		_ = l2.Close()
		t.Fatal("second listen on the same address unexpectedly succeeded")
	}

	if !isAddrInUse(err) {
		t.Fatalf("isAddrInUse(%v) = false, want true", err)
	}
	if isAddrInUse(errors.New("unrelated failure")) {
		t.Fatal("isAddrInUse(unrelated error) = true, want false")
	}
	if isAddrInUse(nil) {
		t.Fatal("isAddrInUse(nil) = true, want false")
	}
}
