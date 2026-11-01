//go:build integration

package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

const multiportProtocolTimeout = 10 * time.Second

func multiportRoundTrip(t *testing.T, client C.ProxyAdapter, backend netip.Addr, port uint16) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), multiportProtocolTimeout)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: backend, DstPort: port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(multiportProtocolTimeout)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("multiport-protocol-echo")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

func multiportEchoTarget(t *testing.T) (netip.Addr, uint16) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var workers sync.WaitGroup
	var conns sync.Map
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns.Store(conn, struct{}{})
			workers.Add(1)
			go func(conn net.Conn) {
				defer workers.Done()
				defer conns.Delete(conn)
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(multiportProtocolTimeout))
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(multiportProtocolTimeout):
			t.Errorf("echo accept loop did not stop")
		}
		conns.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		wait := make(chan struct{})
		go func() { workers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(multiportProtocolTimeout):
			t.Errorf("echo handlers did not stop")
		}
	})
	addr := listener.Addr().(*net.TCPAddr)
	return netip.MustParseAddr("127.0.0.1"), uint16(addr.Port)
}

func multiportRealityCamouflage(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dnsNames := []string{"reality.multiport.test"}
	for i := 1; i < 33; i++ {
		dnsNames = append(dnsNames, fmt.Sprintf("reality-camouflage-%02d-%s.test", i, strings.Repeat("x", 40)))
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: dnsNames[0]}, DNSNames: dnsNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2"}}
	done := make(chan struct{})
	var workers sync.WaitGroup
	var conns sync.Map
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			tlsConn := tls.Server(conn, config)
			conns.Store(tlsConn, struct{}{})
			workers.Add(1)
			go func(conn *tls.Conn) {
				defer workers.Done()
				defer conns.Delete(conn)
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(multiportProtocolTimeout))
				if conn.Handshake() == nil {
					_, _ = io.Copy(io.Discard, conn)
				}
			}(tlsConn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(multiportProtocolTimeout):
			t.Errorf("Reality camouflage accept loop did not stop")
		}
		conns.Range(func(key, _ any) bool { _ = key.(*tls.Conn).Close(); return true })
		wait := make(chan struct{})
		go func() { workers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(multiportProtocolTimeout):
			t.Errorf("Reality camouflage handlers did not stop")
		}
	})
	return listener.Addr().String()
}

func TestMultiportVlessRealityAndTrustTunnelClients(t *testing.T) {
	backendIP, backendPort := multiportEchoTarget(t)
	camouflage := multiportRealityCamouflage(t)
	realityProtocol, ok := servers.Lookup("vless-reality")
	if !ok {
		t.Fatal("VLESS Reality unavailable")
	}
	trustProtocol, ok := servers.Lookup("trusttunnel")
	if !ok {
		t.Fatal("TrustTunnel unavailable")
	}
	vlessParams := map[string]string{"dest": camouflage, "sni": "reality.multiport.test", "flow": "xtls-rprx-vision", "fingerprint": "chrome"}
	trustParams := map[string]string{"public_host": "trust.multiport.test", "sni": "trust.multiport.test", "alpn": "h2"}
	realityMaterial, err := realityProtocol.NewMaterial(vlessParams)
	if err != nil {
		t.Fatal(err)
	}
	trustMaterial, err := trustProtocol.NewMaterial(trustParams)
	if err != nil {
		t.Fatal(err)
	}
	realitySecret, err := realityProtocol.NewSecret("alice", vlessParams)
	if err != nil {
		t.Fatal(err)
	}
	trustSecret, err := trustProtocol.NewSecret("bob", trustParams)
	if err != nil {
		t.Fatal(err)
	}
	port := servertest.FreePort(t)
	specs := []servers.Spec{
		{Name: "multiport-reality", Protocol: "vless-reality", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: vlessParams, Material: servers.SecretMap(realityMaterial), Users: servers.UserSecrets{"alice": servers.SecretMap(realitySecret)}},
		{Name: "multiport-trust", Protocol: "trusttunnel", Bind: "127.0.0.1", Port: port, Accounts: []string{"bob"}, Params: trustParams, Material: servers.SecretMap(trustMaterial), Users: servers.UserSecrets{"bob": servers.SecretMap(trustSecret)}},
	}
	servertest.TrustForTest(t, trustMaterial)
	inst, err := Start(context.Background(), Options{HomeDir: t.TempDir(), Authenticator: auth.NewAuthenticator(nil), Servers: specs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if err := inst.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	shortID := strings.Split(realityMaterial["short_ids"], ",")[0]
	if _, err := hex.DecodeString(shortID); err != nil {
		t.Fatal(err)
	}
	realityClient, err := outbound.NewVless(outbound.VlessOption{Name: "multiport-reality-client", Server: "127.0.0.1", Port: port, UUID: realitySecret["uuid"], Flow: "xtls-rprx-vision", TLS: true, Network: "tcp", Encryption: "none", ClientFingerprint: "chrome", ServerName: vlessParams["sni"], RealityOpts: outbound.RealityOptions{PublicKey: realityMaterial["public_key"], ShortID: shortID}})
	if err != nil {
		t.Fatal(err)
	}
	defer realityClient.Close()
	t.Run("vless-reality", func(t *testing.T) { multiportRoundTrip(t, realityClient, backendIP, backendPort) })
	trustClient, err := outbound.NewTrustTunnel(outbound.TrustTunnelOption{Name: "multiport-trust-client", Server: "127.0.0.1", Port: port, UserName: "bob", Password: trustSecret["password"], SNI: trustParams["sni"], Fingerprint: trustMaterial["fingerprint"], ALPN: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer trustClient.Close()
	t.Run("trusttunnel", func(t *testing.T) { multiportRoundTrip(t, trustClient, backendIP, backendPort) })
}
