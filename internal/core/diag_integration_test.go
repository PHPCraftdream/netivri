//go:build integration

package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// diagTestContext returns a context bounded for the whole test.
func diagTestContext(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// diagStartProbeServer starts an httptest server answering 204 on
// 127.0.0.1:0, closed via t.Cleanup.
func diagStartProbeServer(t testing.TB) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// diagOverrideMapping shallow-copies a mapping and overrides entries.
func diagOverrideMapping(base map[string]any, overrides map[string]any) map[string]any {
	m := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range overrides {
		m[k] = v
	}
	return m
}

// TestDiagDialVlessPhases measures a local VLESS node for both flows: the
// transport seam must be used and every phase must succeed on every sample.
func TestDiagDialVlessPhases(t *testing.T) {
	for _, flow := range []string{"", "xtls-rprx-vision"} {
		t.Run("flow-"+flow, func(t *testing.T) {
			ctx := diagTestContext(t)
			ts := diagStartProbeServer(t)
			srv := testutil.StartVlessServer(t, flow)

			res, err := DiagDial(ctx, DiagOptions{
				Nodes:         []DiagNode{{Label: "test", Mapping: srv.ClientProxy()}},
				URL:           ts.URL,
				Samples:       2,
				Idle:          []time.Duration{},
				SampleTimeout: 10 * time.Second,
			})
			if err != nil {
				t.Fatalf("DiagDial: %v", err)
			}
			if len(res) != 1 {
				t.Fatalf("results: got %d, want 1", len(res))
			}
			nd := res[0]
			if !nd.TransportPhases {
				t.Fatal("TransportPhases: got false, want true")
			}
			if nd.Samples != 2 {
				t.Fatalf("Samples: got %d, want 2", nd.Samples)
			}
			// 127.0.0.1 is an IP literal: the dns phase is skipped.
			if nd.DNS.N != 0 {
				t.Fatalf("DNS samples: got %d, want 0", nd.DNS.N)
			}
			if nd.TCP.N != 2 || nd.TLS.N != 2 || nd.FirstByte.N != 2 {
				t.Fatalf("phase sample counts: tcp %d tls %d first_byte %d, want 2 each",
					nd.TCP.N, nd.TLS.N, nd.FirstByte.N)
			}
			for _, k := range []string{diagFailDNS, diagFailTCP, diagFailTLS, diagFailFirstByte} {
				if nd.Failures[k] != 0 {
					t.Fatalf("Failures[%q] = %d, want 0", k, nd.Failures[k])
				}
			}
			if len(nd.Idle) != 0 {
				t.Fatalf("Idle entries: got %d, want 0 (idle skipped)", len(nd.Idle))
			}
		})
	}
}

// TestDiagDialUnreachableNode measures a node whose port is closed: every
// sample must fail exactly at the tcp phase, and no tls or first_byte
// samples may be recorded.
func TestDiagDialUnreachableNode(t *testing.T) {
	ctx := diagTestContext(t)
	ts := diagStartProbeServer(t)
	srv := testutil.StartVlessServer(t, "")

	// Grab a port and close it again: nothing listens there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	mapping := diagOverrideMapping(srv.ClientProxy(), map[string]any{
		"server": "127.0.0.1",
		"port":   closedPort,
	})
	res, err := DiagDial(ctx, DiagOptions{
		Nodes:         []DiagNode{{Label: "dead", Mapping: mapping}},
		URL:           ts.URL,
		Samples:       2,
		Idle:          []time.Duration{},
		SampleTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("DiagDial: %v", err)
	}
	nd := res[0]
	if !nd.TransportPhases {
		t.Fatal("TransportPhases: got false, want true (tcp+tls vless node)")
	}
	if got := nd.Failures[diagFailTCP]; got != 2 {
		t.Fatalf("tcp failures: got %d, want 2", got)
	}
	// A tcp phase failure ends the sample: no tls or first_byte samples.
	if got := nd.Failures[diagFailTLS]; got != 0 {
		t.Fatalf("tls failures: got %d, want 0", got)
	}
	if nd.TLS.N != 0 {
		t.Fatalf("tls samples: got %d, want 0", nd.TLS.N)
	}
	if got := nd.Failures[diagFailFirstByte]; got != 0 {
		t.Fatalf("first_byte failures: got %d, want 0", got)
	}
	if nd.FirstByte.N != 0 {
		t.Fatalf("first_byte samples: got %d, want 0", nd.FirstByte.N)
	}
	if nd.SuggestedPoolTTL != 0 {
		t.Fatalf("SuggestedPoolTTL: got %v, want 0", nd.SuggestedPoolTTL)
	}
}

// TestDiagDialTLSFailureKeepsTCP measures a node whose server accepts the
// connection and closes it at once: the tcp phase succeeded and must still be
// recorded, the failure lands in tls, and nothing reaches first_byte.
func TestDiagDialTLSFailureKeepsTCP(t *testing.T) {
	ctx := diagTestContext(t)
	ts := diagStartProbeServer(t)
	srv := testutil.StartVlessServer(t, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	mapping := diagOverrideMapping(srv.ClientProxy(), map[string]any{
		"server": "127.0.0.1",
		"port":   diagPort(t, ln.Addr().String()),
	})
	res, err := DiagDial(ctx, DiagOptions{
		Nodes:         []DiagNode{{Label: "closer", Mapping: mapping}},
		URL:           ts.URL,
		Samples:       2,
		Idle:          []time.Duration{},
		SampleTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("DiagDial: %v", err)
	}
	nd := res[0]
	if !nd.TransportPhases {
		t.Fatal("TransportPhases: got false, want true")
	}
	if nd.TCP.N != 2 {
		t.Fatalf("tcp samples: got %d, want 2 (a successful tcp phase counts)", nd.TCP.N)
	}
	if got := nd.Failures[diagFailTLS]; got != 2 {
		t.Fatalf("tls failures: got %d, want 2", got)
	}
	if got := nd.Failures[diagFailTCP]; got != 0 {
		t.Fatalf("tcp failures: got %d, want 0", got)
	}
	if nd.TLS.N != 0 || nd.FirstByte.N != 0 {
		t.Fatalf("tls samples %d, first_byte samples %d, want 0", nd.TLS.N, nd.FirstByte.N)
	}
}

// TestDiagDialCancel stops a run whose idle wait is long: the context
// cancellation ends it promptly with its error.
func TestDiagDialCancel(t *testing.T) {
	ts := diagStartProbeServer(t)
	srv := testutil.StartVlessServer(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := DiagDial(ctx, DiagOptions{
		Nodes:         []DiagNode{{Label: "slow", Mapping: srv.ClientProxy()}},
		URL:           ts.URL,
		Samples:       1,
		Idle:          []time.Duration{30 * time.Second},
		SampleTimeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("DiagDial: want the context error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DiagDial error = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("DiagDial took %v after the cancel, want a prompt return", took)
	}
}

// TestDiagDialIdleSurvival prepares transports through a forwarder that
// closes connections one second after accept: a wait shorter than that must
// survive, a longer one must fail, and the suggested TTL is half of the
// longest fully surviving wait.
func TestDiagDialIdleSurvival(t *testing.T) {
	ctx := diagTestContext(t)
	ts := diagStartProbeServer(t)
	srv := testutil.StartVlessServer(t, "")
	fwd := testutil.StartCloseAfterProxy(t, srv.Addr(), 1*time.Second)

	mapping := diagOverrideMapping(srv.ClientProxy(), map[string]any{
		"server": "127.0.0.1",
		"port":   diagPort(t, fwd.Addr()),
	})
	res, err := DiagDial(ctx, DiagOptions{
		Nodes: []DiagNode{{Label: "idle", Mapping: mapping}},
		URL:   ts.URL,
		// The first sample warms the mode (fresh transport succeeds);
		// the idle attempts then run with fresh transports.
		Samples:       1,
		Idle:          []time.Duration{100 * time.Millisecond, 1500 * time.Millisecond},
		SampleTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("DiagDial: %v", err)
	}
	nd := res[0]
	if !nd.TransportPhases {
		t.Fatal("TransportPhases: got false, want true")
	}
	if len(nd.Idle) != 2 {
		t.Fatalf("Idle entries: got %d, want 2 (%+v)", len(nd.Idle), nd.Idle)
	}
	if nd.Idle[0].Idle != 100*time.Millisecond || nd.Idle[0].OK != 1 || nd.Idle[0].Fail != 0 {
		t.Fatalf("idle[0]: got %+v, want 100ms OK=1 Fail=0", nd.Idle[0])
	}
	if nd.Idle[1].Idle != 1500*time.Millisecond || nd.Idle[1].OK != 0 || nd.Idle[1].Fail != 1 {
		t.Fatalf("idle[1]: got %+v, want 1500ms OK=0 Fail=1", nd.Idle[1])
	}
	if nd.SuggestedPoolTTL != 50*time.Millisecond {
		t.Fatalf("SuggestedPoolTTL: got %v, want %v", nd.SuggestedPoolTTL, 50*time.Millisecond)
	}
}

// diagPort parses host:port into a port number.
func diagPort(t testing.TB, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port %q: %v", port, err)
	}
	return p
}
