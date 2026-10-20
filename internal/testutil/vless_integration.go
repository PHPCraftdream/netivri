//go:build integration

// VLESS test server and helpers for the transport seam integration tests.
// This file is part of the same testutil package but is only compiled when
// the integration build tag is set, so helpers touching the network stay out
// of unit builds.
package testutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/inbound"
)

// VlessTestUUID is the fixed user UUID accepted by the test VLESS server.
const VlessTestUUID = "0d3a5c47-95f0-4b8b-9f5e-2a45b7e1c3d8"

// vlessTestServerName is the TLS server name of the test certificate.
const vlessTestServerName = "netivri.test"

// countWaiter counts events and lets callers block until a target count is
// reached, without sleeps.
type countWaiter struct {
	mu      sync.Mutex
	count   int
	waiters map[int][]chan struct{}
}

func (w *countWaiter) add() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	for n, chs := range w.waiters {
		if w.count >= n {
			for _, ch := range chs {
				close(ch)
			}
			delete(w.waiters, n)
		}
	}
	return w.count
}

func (w *countWaiter) value() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

func (w *countWaiter) wait(t testing.TB, n int, timeout time.Duration) {
	t.Helper()
	w.mu.Lock()
	if w.count >= n {
		w.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	if w.waiters == nil {
		w.waiters = make(map[int][]chan struct{})
	}
	w.waiters[n] = append(w.waiters[n], ch)
	w.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("timed out waiting for count %d (have %d)", n, w.value())
	}
}

// VlessTunnel is a minimal C.Tunnel that relays TCP conns to their requested
// destination and counts handled TCP conns.
type VlessTunnel struct {
	cw countWaiter
}

// HandleTCPConn implements C.Tunnel.
func (tn *VlessTunnel) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	tn.cw.add()
	dest := metadata.RemoteAddress()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	dst, err := dialer.Dial("tcp", dest)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() {
		_ = dst.Close()
		_ = conn.Close()
	}()
	go func() {
		_, _ = io.Copy(conn, dst)
		_ = conn.Close()
	}()
	_, _ = io.Copy(dst, conn)
}

// HandleUDPPacket implements C.Tunnel. UDP is not used by the tests.
func (tn *VlessTunnel) HandleUDPPacket(packet C.UDPPacket, _ *C.Metadata) {
	packet.Drop()
}

// NatTable implements C.Tunnel. UDP is disabled in the client config, so it
// is never called.
func (tn *VlessTunnel) NatTable() C.NatTable {
	return nil
}

// WaitTCP blocks until n TCP conns have been handled.
func (tn *VlessTunnel) WaitTCP(t testing.TB, n int) {
	tn.cw.wait(t, n, 5*time.Second)
}

// TCPCount returns the number of handled TCP conns.
func (tn *VlessTunnel) TCPCount() int {
	return tn.cw.value()
}

// VlessServer is a local VLESS server (TLS, self-signed) relaying through a
// VlessTunnel.
type VlessServer struct {
	in     *inbound.Vless
	tunnel *VlessTunnel
	host   string
	port   int
	flow   string
}

// Addr returns the host:port the inbound listener is bound to.
func (s *VlessServer) Addr() string {
	ap, err := netip.ParseAddrPort(s.in.Address())
	if err != nil {
		return s.in.Address()
	}
	return ap.String()
}

// Tunnel returns the server tunnel for waits and counters.
func (s *VlessServer) Tunnel() *VlessTunnel {
	return s.tunnel
}

// ClientProxy returns a proxy mapping accepted by adapter.ParseProxy.
func (s *VlessServer) ClientProxy() map[string]any {
	mapping := map[string]any{
		"type":             "vless",
		"name":             "netivri-vless-test",
		"server":           s.host,
		"port":             s.port,
		"uuid":             VlessTestUUID,
		"tls":              true,
		"servername":       vlessTestServerName,
		"skip-cert-verify": true,
		"udp":              false,
	}
	if s.flow != "" {
		mapping["flow"] = s.flow
	}
	return mapping
}

// Close stops the inbound listener.
func (s *VlessServer) Close() error {
	return s.in.Close()
}

// StartVlessServer starts a local VLESS server on 127.0.0.1:0 with a single
// user (UUID VlessTestUUID) and the given flow, using an in-memory
// self-signed certificate. The server is closed via t.Cleanup.
func StartVlessServer(t testing.TB, flow string) *VlessServer {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: vlessTestServerName},
		DNSNames:              []string{vlessTestServerName},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tunnel := &VlessTunnel{}
	in, err := inbound.NewVless(&inbound.VlessOption{
		BaseOption:  inbound.BaseOption{NameStr: "netivri-vless-in", Listen: "127.0.0.1", Port: "0"},
		Users:       []inbound.VlessUser{{Username: "netivri", UUID: VlessTestUUID, Flow: flow}},
		Certificate: string(certPEM),
		PrivateKey:  string(keyPEM),
	})
	if err != nil {
		t.Fatalf("new vless inbound: %v", err)
	}
	if err := in.Listen(tunnel); err != nil {
		t.Fatalf("listen vless inbound: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })

	srv := &VlessServer{in: in, tunnel: tunnel}
	ap, err := netip.ParseAddrPort(in.Address())
	if err != nil {
		t.Fatalf("parse inbound address %q: %v", in.Address(), err)
	}
	srv.host = ap.Addr().String()
	srv.port = int(ap.Port())
	srv.flow = flow
	return srv
}

// EchoDest is a tiny TCP echo destination used as the tunnel target.
type EchoDest struct {
	ln net.Listener
	cw countWaiter
}

// StartEchoDest starts a TCP echo server on 127.0.0.1:0. The listener is
// closed via t.Cleanup.
func StartEchoDest(t testing.TB) *EchoDest {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo dest: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &EchoDest{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.cw.add()
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return d
}

// Addr returns the host:port the echo destination listens on.
func (d *EchoDest) Addr() string {
	return d.ln.Addr().String()
}

// WaitAccepts blocks until n conns have been accepted.
func (d *EchoDest) WaitAccepts(t testing.TB, n int) {
	d.cw.wait(t, n, 5*time.Second)
}

// Accepts returns the number of accepted conns so far.
func (d *EchoDest) Accepts() int {
	return d.cw.value()
}

// FixedTransportSource always hands out the single stored conn.
type FixedTransportSource struct {
	Conn net.Conn
}

var _ outbound.TransportSource = (*FixedTransportSource)(nil)

// Take implements outbound.TransportSource.
func (s *FixedTransportSource) Take(context.Context) (net.Conn, bool) {
	return s.Conn, true
}

// EmptyTransportSource never provides a conn, forcing the fallback dial path.
type EmptyTransportSource struct{}

var _ outbound.TransportSource = (*EmptyTransportSource)(nil)

// Take implements outbound.TransportSource.
func (*EmptyTransportSource) Take(context.Context) (net.Conn, bool) {
	return nil, false
}
