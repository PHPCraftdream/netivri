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
	"errors"
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

const multiportTrialTimeout = 12 * time.Second

func multiportTrialEcho(t *testing.T) (netip.Addr, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	var conns sync.Map
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			conns.Store(c, struct{}{})
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer conns.Delete(c)
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(multiportTrialTimeout))
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(multiportTrialTimeout):
			t.Error("echo accept loop did not stop")
		}
		conns.Range(func(k, _ any) bool { _ = k.(net.Conn).Close(); return true })
		fin := make(chan struct{})
		go func() { wg.Wait(); close(fin) }()
		select {
		case <-fin:
		case <-time.After(multiportTrialTimeout):
			t.Error("echo handlers did not stop")
		}
	})
	return netip.MustParseAddr("127.0.0.1"), uint16(ln.Addr().(*net.TCPAddr).Port)
}
func multiportTrialCamouflage(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"trial-reality.multiport.test"}
	for i := 1; i < 33; i++ {
		names = append(names, fmt.Sprintf("trial-camouflage-%02d-%s.test", i, strings.Repeat("x", 40)))
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2"}}
	done := make(chan struct{})
	var wg sync.WaitGroup
	var conns sync.Map
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			tc := tls.Server(c, cfg)
			conns.Store(tc, struct{}{})
			wg.Add(1)
			go func(c *tls.Conn) {
				defer wg.Done()
				defer conns.Delete(c)
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(multiportTrialTimeout))
				if c.Handshake() == nil {
					_, _ = io.Copy(io.Discard, c)
				}
			}(tc)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(multiportTrialTimeout):
			t.Error("camouflage accept loop did not stop")
		}
		conns.Range(func(k, _ any) bool { _ = k.(*tls.Conn).Close(); return true })
		fin := make(chan struct{})
		go func() { wg.Wait(); close(fin) }()
		select {
		case <-fin:
		case <-time.After(multiportTrialTimeout):
			t.Error("camouflage handlers did not stop")
		}
	})
	return ln.Addr().String()
}
func multiportTrialRoundTrip(t *testing.T, c C.ProxyAdapter, ip netip.Addr, p uint16, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), multiportTrialTimeout)
	defer cancel()
	conn, err := c.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: ip, DstPort: p})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(multiportTrialTimeout))
	if _, err = io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil || string(got) != payload {
		t.Fatalf("echo=%q err=%v", got, err)
	}
}

type multiportTrialCaptureConn struct {
	net.Conn
	buffer []byte
}

func (c *multiportTrialCaptureConn) Write(p []byte) (int, error) {
	n, e := c.Conn.Write(p)
	c.buffer = append(c.buffer, p[:n]...)
	return n, e
}
func multiportTrialCaptureFirstFlight(t *testing.T, c *outbound.ShadowSocks, port int, m *C.Metadata) []byte {
	t.Helper()
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), multiportTrialTimeout)
	if err != nil {
		t.Fatal(err)
	}
	capture := &multiportTrialCaptureConn{Conn: raw}
	_ = capture.SetDeadline(time.Now().Add(multiportTrialTimeout))
	ctx, cancel := context.WithTimeout(context.Background(), multiportTrialTimeout)
	defer cancel()
	encoded, err := c.StreamConnContext(ctx, capture, m)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	payload := []byte("trial-replay-echo")
	if _, err = encoded.Write(payload); err != nil {
		_ = encoded.Close()
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(encoded, got); err != nil || string(got) != string(payload) {
		_ = encoded.Close()
		t.Fatalf("first flight round trip failed: %v", err)
	}
	if len(capture.buffer) == 0 {
		_ = encoded.Close()
		t.Fatal("first flight empty")
	}
	_ = encoded.Close()
	return append([]byte(nil), capture.buffer...)
}
func multiportTrialRawReplay(t *testing.T, port int, flight []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), multiportTrialTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(multiportTrialTimeout))
	if _, err = conn.Write(flight); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if n > 0 {
		t.Fatal("refused replay produced proxy data")
	}
	if err == nil {
		t.Fatal("refused replay produced neither error nor timeout")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return
	}
}

func TestMultiportTrialSharedSS2022VMessTLSAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	backendIP, backendPort := multiportTrialEcho(t)
	camouflage := multiportTrialCamouflage(t)
	ssP, _ := servers.Lookup("shadowsocks")
	vmP, _ := servers.Lookup("vmess")
	trP, _ := servers.Lookup("trojan")
	vlP, _ := servers.Lookup("vless-reality")
	ssRec, ssAddr := startChainRecorder(t)
	vmRec, vmAddr := startChainRecorder(t)
	trRec, trAddr := startChainRecorder(t)
	rlRec, rlAddr := startChainRecorder(t)
	ssParams := map[string]string{"cipher": "2022-blake3-aes-128-gcm"}
	vmParams := map[string]string{"cipher": "auto"}
	trParams := map[string]string{"sni": "trial-trojan.multiport.test"}
	rlParams := map[string]string{"dest": camouflage, "sni": "trial-reality.multiport.test", "flow": "xtls-rprx-vision", "fingerprint": "chrome"}
	ssA, _ := ssP.NewSecret("alice", ssParams)
	ssB, _ := ssP.NewSecret("bob", ssParams)
	vm, _ := vmP.NewSecret("carol", vmParams)
	tm, e := trP.NewMaterial(trParams)
	if e != nil {
		t.Fatal(e)
	}
	ts, _ := trP.NewSecret("dave", trParams)
	rm, e := vlP.NewMaterial(rlParams)
	if e != nil {
		t.Fatal(e)
	}
	rs, _ := vlP.NewSecret("erin", rlParams)
	port := servertest.FreePort(t)
	home := t.TempDir()
	specs := func(bob, bad bool) []servers.Spec {
		s := []servers.Spec{{Name: "trial-ss-alice", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: ssParams, Users: servers.UserSecrets{"alice": servers.SecretMap(ssA)}}}
		if bob {
			u := ssB
			if bad {
				u = ssA
			}
			s = append(s, servers.Spec{Name: "trial-ss-bob", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port, Accounts: []string{"bob"}, Params: ssParams, Users: servers.UserSecrets{"bob": servers.SecretMap(u)}})
		}
		return append(s, servers.Spec{Name: "trial-vmess", Protocol: "vmess", Bind: "127.0.0.1", Port: port, Accounts: []string{"carol"}, Params: vmParams, Users: servers.UserSecrets{"carol": servers.SecretMap(vm)}}, servers.Spec{Name: "trial-trojan", Protocol: "trojan", Bind: "127.0.0.1", Port: port, Accounts: []string{"dave"}, Params: trParams, Material: servers.SecretMap(tm), Users: servers.UserSecrets{"dave": servers.SecretMap(ts)}}, servers.Spec{Name: "trial-reality", Protocol: "vless-reality", Bind: "127.0.0.1", Port: port, Accounts: []string{"erin"}, Params: rlParams, Material: servers.SecretMap(rm), Users: servers.UserSecrets{"erin": servers.SecretMap(rs)}})
	}
	options := func(s []servers.Spec) Options {
		return Options{HomeDir: home, Authenticator: auth.NewAuthenticator(nil), RouteNodes: map[string][]map[string]any{"alice": {nodeMap(ssAddr)}, "bob": {nodeMap(ssAddr)}, "carol": {nodeMap(vmAddr)}, "dave": {nodeMap(trAddr)}, "erin": {nodeMap(rlAddr)}}, AccountRoute: map[string]string{"alice": "alice", "bob": "bob", "carol": "carol", "dave": "dave", "erin": "erin"}, Servers: s}
	}
	inst, e := Start(ctx, options(specs(true, false)))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if e := inst.Close(); e != nil {
			t.Errorf("close: %v", e)
		}
	})
	servertest.TrustForTest(t, tm)
	sid := strings.Split(rm["short_ids"], ",")[0]
	if _, e = hex.DecodeString(sid); e != nil {
		t.Fatal(e)
	}
	ss1, e := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "trial-ss-a", Server: "127.0.0.1", Port: port, Password: ssA["password"], Cipher: ssParams["cipher"]})
	if e != nil {
		t.Fatal(e)
	}
	ss2, e := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "trial-ss-b", Server: "127.0.0.1", Port: port, Password: ssB["password"], Cipher: ssParams["cipher"]})
	if e != nil {
		t.Fatal(e)
	}
	vmc, e := outbound.NewVmess(outbound.VmessOption{Name: "trial-vm", Server: "127.0.0.1", Port: port, UUID: vm["uuid"], AlterID: 0, Cipher: "auto", Network: "tcp"})
	if e != nil {
		t.Fatal(e)
	}
	trc, e := outbound.NewTrojan(outbound.TrojanOption{Name: "trial-tr", Server: "127.0.0.1", Port: port, Password: ts["password"], SNI: trParams["sni"], Fingerprint: tm["fingerprint"]})
	if e != nil {
		t.Fatal(e)
	}
	rlc, e := outbound.NewVless(outbound.VlessOption{Name: "trial-rl", Server: "127.0.0.1", Port: port, UUID: rs["uuid"], Flow: "xtls-rprx-vision", TLS: true, Network: "tcp", Encryption: "none", ClientFingerprint: "chrome", ServerName: rlParams["sni"], RealityOpts: outbound.RealityOptions{PublicKey: rm["public_key"], ShortID: sid}})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []C.ProxyAdapter{ss1, ss2, vmc, trc, rlc} {
		c := c
		t.Cleanup(func() { _ = c.Close() })
	}
	for _, x := range []struct {
		name string
		c    C.ProxyAdapter
		r    *chainRecorder
	}{{"ssa", ss1, ssRec}, {"ssb", ss2, ssRec}, {"vm", vmc, vmRec}, {"tr", trc, trRec}, {"rl", rlc, rlRec}} {
		before := len(x.r.snapshot())
		multiportTrialRoundTrip(t, x.c, backendIP, backendPort, "trial-"+x.name)
		waitServerConnections(t, inst)
		if len(x.r.snapshot()) <= before {
			t.Fatalf("route recorder not used: %s", x.name)
		}
	}
	meta := &C.Metadata{NetWork: C.TCP, DstIP: backendIP, DstPort: backendPort}
	beforeCapture := len(ssRec.snapshot())
	flight := multiportTrialCaptureFirstFlight(t, ss1, port, meta)
	waitServerConnections(t, inst)
	if len(ssRec.snapshot()) <= beforeCapture {
		t.Fatal("captured first flight did not route")
	}
	before := len(ssRec.snapshot())
	replay := uint64(0)
	for _, i := range inst.Servers() {
		if i.Name == "trial-ss-alice" {
			replay = i.Replayed
		}
	}
	multiportTrialRawReplay(t, port, flight)
	deadline := time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, i := range inst.Servers() {
			if i.Name == "trial-ss-alice" && i.Replayed > replay {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replay counter unchanged")
		}
		select {
		case <-ctx.Done():
			t.Fatal("waiting replay")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(ssRec.snapshot()) != before {
		t.Fatal("refused replay reached route")
	}
	if e := inst.Reconfigure(options(specs(false, false))); e != nil {
		t.Fatal(e)
	}
	beforeRemoved := len(ssRec.snapshot())
	removedCtx, removedCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	removedConn, removedErr := ss2.DialContext(removedCtx, &C.Metadata{NetWork: C.TCP, DstIP: backendIP, DstPort: backendPort})
	if removedErr == nil {
		_ = removedConn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		payload := []byte("removed-bob-must-not-route")
		if _, writeErr := removedConn.Write(payload); writeErr == nil {
			response := make([]byte, len(payload))
			if _, readErr := io.ReadFull(removedConn, response); readErr == nil && string(response) == string(payload) {
				_ = removedConn.Close()
				removedCancel()
				t.Fatal("removed Bob member routed a successful round trip")
			}
		}
		_ = removedConn.Close()
	}
	removedCancel()
	waitServerConnections(t, inst)
	if len(ssRec.snapshot()) != beforeRemoved {
		t.Fatal("removed Bob member reached the route recorder")
	}
	if e := inst.Reconfigure(options(specs(true, false))); e != nil {
		t.Fatalf("re-add Bob member: %v", e)
	}
	beforeReadded := len(ssRec.snapshot())
	multiportTrialRoundTrip(t, ss2, backendIP, backendPort, "re-added-member")
	waitServerConnections(t, inst)
	if len(ssRec.snapshot()) <= beforeReadded {
		t.Fatal("re-added Bob member did not route")
	}
	multiportTrialRoundTrip(t, ss1, backendIP, backendPort, "still-serving")
	if e := inst.Reconfigure(options(specs(true, true))); e == nil {
		t.Fatal("duplicate key accepted")
	}
	for _, name := range []string{"trial-ss-alice", "trial-vmess", "trial-trojan", "trial-reality"} {
		ok := false
		for _, i := range inst.Servers() {
			if i.Name == name {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("rollback lost %s", name)
		}
	}
	for _, c := range []C.ProxyAdapter{vmc, trc, rlc} {
		multiportTrialRoundTrip(t, c, backendIP, backendPort, "after-rollback")
	}
}
