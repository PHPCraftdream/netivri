//go:build integration

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// recordedPhase is one observed transport phase.
type recordedPhase struct {
	phase outbound.Phase
	took  time.Duration
	err   error
}

// phaseRecorder records observed transport phases.
type phaseRecorder struct {
	mu     sync.Mutex
	phases []recordedPhase
}

// ObservePhase implements outbound.PhaseObserver.
func (r *phaseRecorder) ObservePhase(phase outbound.Phase, took time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = append(r.phases, recordedPhase{phase, took, err})
}

func (r *phaseRecorder) snapshot() []recordedPhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedPhase, len(r.phases))
	copy(out, r.phases)
	return out
}

// newSeamVless builds a plain tcp+tls VLESS adapter pointing at addr
// (host:port) and registers its close via t.Cleanup.
func newSeamVless(t testing.TB, addr string, flow string) *outbound.Vless {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatalf("parse server address %q: %v", addr, err)
	}
	v, err := outbound.NewVless(outbound.VlessOption{
		Name:           "t",
		Server:         ap.Addr().String(),
		Port:           int(ap.Port()),
		UUID:           testutil.VlessTestUUID,
		TLS:            true,
		ServerName:     "netivri.test",
		SkipCertVerify: true,
		Flow:           flow,
		UDP:            false,
	})
	if err != nil {
		t.Fatalf("new vless adapter: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

// seamContext returns a context bounded for the whole test.
func seamContext(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seamEchoRoundTrip writes msg with a deadline and expects it echoed back.
func seamEchoRoundTrip(t testing.TB, conn net.Conn, msg string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("echo mismatch: got %q want %q", got, msg)
	}
}

func seamMetadata(port int) *C.Metadata {
	return &C.Metadata{
		NetWork: C.TCP,
		Host:    "127.0.0.1",
		DstPort: uint16(port),
	}
}

func TestVlessSeamPrepareAndAttach(t *testing.T) {
	for _, flow := range []string{"", "xtls-rprx-vision"} {
		t.Run("flow-"+flow, func(t *testing.T) {
			ctx := seamContext(t)
			dest := testutil.StartEchoDest(t)
			srv := testutil.StartVlessServer(t, flow)
			v := newSeamVless(t, srv.Addr(), flow)

			destPort := int(netip.MustParseAddrPort(dest.Addr()).Port())
			md := seamMetadata(destPort)

			prepared, err := v.PrepareTransport(ctx)
			if err != nil {
				t.Fatalf("PrepareTransport: %v", err)
			}
			t.Cleanup(func() { _ = prepared.Close() })

			// Normal dial without a source: sends a VLESS request over a
			// freshly dialed transport.
			conn, err := v.DialContext(ctx, md)
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			seamEchoRoundTrip(t, conn, "ping-ping")
			_ = conn.Close()

			srv.Tunnel().WaitTCP(t, 1)
			dest.WaitAccepts(t, 1)
			if got := dest.Accepts(); got != 1 {
				t.Fatalf("echo dest accepts: got %d want 1", got)
			}
			if got := srv.Tunnel().TCPCount(); got != 1 {
				t.Fatalf("tunnel conns: got %d want 1", got)
			}

			// Attach the prepared transport: the dial must reuse it and the
			// echo dest must see exactly one more conn.
			v.SetTransportSource(&testutil.FixedTransportSource{Conn: prepared})
			t.Cleanup(func() { v.SetTransportSource(nil) })

			conn2, err := v.DialContext(ctx, md)
			if err != nil {
				t.Fatalf("DialContext with source: %v", err)
			}
			t.Cleanup(func() { _ = conn2.Close() })
			seamEchoRoundTrip(t, conn2, "attach-ok")
			_ = conn2.Close()

			srv.Tunnel().WaitTCP(t, 2)
			dest.WaitAccepts(t, 2)
			if got := dest.Accepts(); got != 2 {
				t.Fatalf("echo dest accepts after attach: got %d want 2", got)
			}
			// Both dials went through the tunnel: exactly two conns total, so
			// the prepared transport dial did not open an extra transport.
			if got := srv.Tunnel().TCPCount(); got != 2 {
				t.Fatalf("tunnel conns after attach: got %d want 2", got)
			}
		})
	}
}

func TestVlessSeamSourceMissFallsBack(t *testing.T) {
	ctx := seamContext(t)
	dest := testutil.StartEchoDest(t)
	srv := testutil.StartVlessServer(t, "")
	v := newSeamVless(t, srv.Addr(), "")
	v.SetTransportSource(&testutil.EmptyTransportSource{})

	md := seamMetadata(int(netip.MustParseAddrPort(dest.Addr()).Port()))
	conn, err := v.DialContext(ctx, md)
	if err != nil {
		t.Fatalf("DialContext after source miss: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	seamEchoRoundTrip(t, conn, "fallback")
	_ = conn.Close()

	srv.Tunnel().WaitTCP(t, 1)
	dest.WaitAccepts(t, 1)
}

func TestVlessSeamPhases(t *testing.T) {
	ctx := seamContext(t)
	dest := testutil.StartEchoDest(t)
	srv := testutil.StartVlessServer(t, "")
	v := newSeamVless(t, srv.Addr(), "")

	// Good path: PrepareTransport records tcp then tls, both successful.
	rec := &phaseRecorder{}
	v.SetPhaseObserver(rec)
	t.Cleanup(func() { v.SetPhaseObserver(nil) })

	prepared, err := v.PrepareTransport(ctx)
	if err != nil {
		t.Fatalf("PrepareTransport: %v", err)
	}
	_ = prepared.Close()
	phases := rec.snapshot()
	if len(phases) != 2 {
		t.Fatalf("observed %d phases, want 2: %+v", len(phases), phases)
	}
	if phases[0].phase != outbound.PhaseTCP || phases[1].phase != outbound.PhaseTLS {
		t.Fatalf("phase order: got %s then %s", phases[0].phase, phases[1].phase)
	}
	for i, p := range phases {
		if p.err != nil {
			t.Fatalf("phase %d error: %v", i, p.err)
		}
		if p.took < 0 {
			t.Fatalf("phase %d negative duration: %v", i, p.took)
		}
	}

	// Listener that accepts and immediately closes: tls phase must fail.
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

	failRec := &phaseRecorder{}
	vFail := newSeamVless(t, ln.Addr().String(), "")
	vFail.SetPhaseObserver(failRec)
	t.Cleanup(func() { vFail.SetPhaseObserver(nil) })

	if _, err := vFail.PrepareTransport(ctx); err == nil {
		t.Fatal("PrepareTransport against closing listener: want error, got nil")
	}
	failPhases := failRec.snapshot()
	if len(failPhases) != 2 {
		t.Fatalf("observed %d phases, want 2: %+v", len(failPhases), failPhases)
	}
	if failPhases[0].phase != outbound.PhaseTCP || failPhases[0].err != nil {
		t.Fatalf("tcp phase: got %+v, want phase tcp with nil err", failPhases[0])
	}
	if failPhases[1].phase != outbound.PhaseTLS || failPhases[1].err == nil {
		t.Fatalf("tls phase: got %+v, want phase tls with non-nil err", failPhases[1])
	}

	// No observer set: PrepareTransport and a dial must not panic.
	vPlain := newSeamVless(t, srv.Addr(), "")
	plainPrepared, err := vPlain.PrepareTransport(ctx)
	if err != nil {
		t.Fatalf("PrepareTransport without observer: %v", err)
	}
	_ = plainPrepared.Close()
	md := seamMetadata(int(netip.MustParseAddrPort(dest.Addr()).Port()))
	conn, err := vPlain.DialContext(ctx, md)
	if err != nil {
		t.Fatalf("DialContext without observer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	seamEchoRoundTrip(t, conn, "plain")
	_ = conn.Close()
}

func TestVlessSeamUnsupported(t *testing.T) {
	ctx := seamContext(t)

	v, err := outbound.NewVless(outbound.VlessOption{
		Name:     "g",
		Server:   "127.0.0.1",
		Port:     1,
		UUID:     testutil.VlessTestUUID,
		Network:  "grpc",
		GrpcOpts: outbound.GrpcOptions{GrpcServiceName: "GunService"},
		TLS:      true,
	})
	if err != nil {
		t.Fatalf("new grpc vless adapter: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.PrepareTransport(ctx); !errors.Is(err, outbound.ErrTransportUnsupported) {
		t.Fatalf("grpc PrepareTransport: got %v, want ErrTransportUnsupported", err)
	}

	vPlain, err := outbound.NewVless(outbound.VlessOption{
		Name:   "p",
		Server: "127.0.0.1",
		Port:   1,
		UUID:   testutil.VlessTestUUID,
		TLS:    false,
	})
	if err != nil {
		t.Fatalf("new plain vless adapter: %v", err)
	}
	t.Cleanup(func() { _ = vPlain.Close() })
	if _, err := vPlain.PrepareTransport(ctx); !errors.Is(err, outbound.ErrTransportUnsupported) {
		t.Fatalf("plain PrepareTransport: got %v, want ErrTransportUnsupported", err)
	}
}

func TestVlessAdapterUnwrap(t *testing.T) {
	v, err := outbound.NewVless(outbound.VlessOption{
		Name:   "t",
		Server: "127.0.0.1",
		Port:   1,
		UUID:   testutil.VlessTestUUID,
		TLS:    true,
	})
	if err != nil {
		t.Fatalf("new vless adapter: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	p := adapter.NewProxy(v)
	got, ok := vlessAdapter(p)
	if !ok || got != v {
		t.Fatalf("vlessAdapter positive: got (%v, %v), want (%v, true)", got, ok, v)
	}

	// adapter.ParseProxy wraps the adapter in an auto-close wrapper.
	parsed, err := adapter.ParseProxy(map[string]any{
		"type":             "vless",
		"name":             "parsed",
		"server":           "127.0.0.1",
		"port":             1,
		"uuid":             testutil.VlessTestUUID,
		"tls":              true,
		"skip-cert-verify": true,
	})
	if err != nil {
		t.Fatalf("parse vless proxy: %v", err)
	}
	t.Cleanup(func() { _ = parsed.Close() })
	if got, ok := vlessAdapter(parsed); !ok || got == nil {
		t.Fatalf("vlessAdapter through ParseProxy: got (%v, %v), want a vless adapter", got, ok)
	}

	proxy, err := adapter.ParseProxy(map[string]any{"type": "socks5", "name": "s", "server": "127.0.0.1", "port": 1})
	if err != nil {
		t.Fatalf("parse socks5 proxy: %v", err)
	}
	got, ok = vlessAdapter(proxy)
	if ok || got != nil {
		t.Fatalf("vlessAdapter negative: got (%v, %v), want (nil, false)", got, ok)
	}
}
