//go:build integration

package main

import (
	"net"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// TestDoctorListenRealSocket covers the port probe with a real bind: the
// port of a live listener is taken, a released one is free.
func TestDoctorListenRealSocket(t *testing.T) {
	doctorFixTime(t)
	doctorDaemonDown(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) // a second close after the test's own is harmless
	port := ln.Addr().(*net.TCPAddr).Port

	// The listener is still open: the configured port is taken.
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) { cfg.Listen.Port = port })
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c := doctorFind(t, doc, "listen")
	if c.Status != doctorWarn || !strings.HasSuffix(c.Detail, "is taken") {
		t.Errorf("listen = %+v", c)
	}

	// The same port, released: it is free now.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	freeHome := t.TempDir()
	doctorConfig(t, freeHome, func(cfg *config.Config) { cfg.Listen.Port = port })
	doc, err = doctorRunDoc(t, freeHome)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c = doctorFind(t, doc, "listen")
	if c.Status != doctorOK || !strings.HasSuffix(c.Detail, "is free") {
		t.Errorf("listen = %+v", c)
	}
}
