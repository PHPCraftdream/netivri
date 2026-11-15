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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
)

const multiportTrialTimeout = 12 * time.Second

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
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(multiportTrialTimeout))
				if c.HandshakeContext(context.Background()) == nil {
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
		conns.Range(func(k, _ any) bool {
			c, ok := k.(*tls.Conn)
			if !ok {
				t.Errorf("tracked connection is %T, want *tls.Conn", k)
				return true
			}
			_ = c.Close()
			return true
		})
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
	defer func() { _ = conn.Close() }()
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
	raw, err := (&net.Dialer{Timeout: multiportTrialTimeout}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
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
	conn, err := (&net.Dialer{Timeout: multiportTrialTimeout}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
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

// trialFixture is the state shared by the phases of
// TestMultiportTrialSharedSS2022VMessTLSAndRollback.
type trialFixture struct {
	backendIP   netip.Addr
	backendPort uint16
	camouflage  string
	port        int
	home        string
	ssParams    map[string]string
	vmParams    map[string]string
	trParams    map[string]string
	rlParams    map[string]string
	ssA         map[string]string
	ssB         map[string]string
	vm          map[string]string
	ts          map[string]string
	rs          map[string]string
	tm          map[string]string
	rm          map[string]string
	ssAddr      string
	vmAddr      string
	trAddr      string
	rlAddr      string
	ssRec       *chainRecorder
	vmRec       *chainRecorder
	trRec       *chainRecorder
	rlRec       *chainRecorder
	inst        *Instance
	sid         string
	ss1         *outbound.ShadowSocks
	ss2         *outbound.ShadowSocks
	vmc         C.ProxyAdapter
	trc         C.ProxyAdapter
	rlc         C.ProxyAdapter
}

// newTrialFixture builds the echo backend, the camouflage, the chain recorders
// and the secrets of the trial protocols.
func newTrialFixture(t *testing.T) *trialFixture {
	t.Helper()
	f := &trialFixture{}
	f.backendIP, f.backendPort = multiportEchoServer(t, multiportTrialTimeout)
	f.camouflage = multiportTrialCamouflage(t)
	ssP, _ := servers.Lookup("shadowsocks")
	vmP, _ := servers.Lookup("vmess")
	trP, _ := servers.Lookup("trojan")
	vlP, _ := servers.Lookup("vless-reality")
	f.ssRec, f.ssAddr = startChainRecorder(t)
	f.vmRec, f.vmAddr = startChainRecorder(t)
	f.trRec, f.trAddr = startChainRecorder(t)
	f.rlRec, f.rlAddr = startChainRecorder(t)
	f.ssParams = map[string]string{"cipher": "2022-blake3-aes-128-gcm"}
	f.vmParams = map[string]string{"cipher": "auto"}
	f.trParams = map[string]string{"sni": "trial-trojan.multiport.test"}
	f.rlParams = map[string]string{"dest": f.camouflage, "sni": "trial-reality.multiport.test", "flow": "xtls-rprx-vision", "fingerprint": "chrome"}
	f.ssA, _ = ssP.NewSecret("alice", f.ssParams)
	f.ssB, _ = ssP.NewSecret("bob", f.ssParams)
	f.vm, _ = vmP.NewSecret("carol", f.vmParams)
	var e error
	f.tm, e = trP.NewMaterial(f.trParams)
	if e != nil {
		t.Fatal(e)
	}
	f.ts, _ = trP.NewSecret("dave", f.trParams)
	f.rm, e = vlP.NewMaterial(f.rlParams)
	if e != nil {
		t.Fatal(e)
	}
	f.rs, _ = vlP.NewSecret("erin", f.rlParams)
	f.port = servertest.FreePort(t)
	f.home = t.TempDir()
	return f
}

// specs lists the trial servers. Bob's member is served only when bob is set,
// and with Alice's secret when bad is set.
func (f *trialFixture) specs(bob, bad bool) []servers.Spec {
	port := f.port
	s := []servers.Spec{{Name: "trial-ss-alice", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: f.ssParams, Users: servers.UserSecrets{"alice": servers.SecretMap(f.ssA)}}}
	if bob {
		u := f.ssB
		if bad {
			u = f.ssA
		}
		s = append(s, servers.Spec{Name: "trial-ss-bob", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port, Accounts: []string{"bob"}, Params: f.ssParams, Users: servers.UserSecrets{"bob": servers.SecretMap(u)}})
	}
	return append(s, servers.Spec{Name: "trial-vmess", Protocol: "vmess", Bind: "127.0.0.1", Port: port, Accounts: []string{"carol"}, Params: f.vmParams, Users: servers.UserSecrets{"carol": servers.SecretMap(f.vm)}}, servers.Spec{Name: "trial-trojan", Protocol: "trojan", Bind: "127.0.0.1", Port: port, Accounts: []string{"dave"}, Params: f.trParams, Material: servers.SecretMap(f.tm), Users: servers.UserSecrets{"dave": servers.SecretMap(f.ts)}}, servers.Spec{Name: "trial-reality", Protocol: "vless-reality", Bind: "127.0.0.1", Port: port, Accounts: []string{"erin"}, Params: f.rlParams, Material: servers.SecretMap(f.rm), Users: servers.UserSecrets{"erin": servers.SecretMap(f.rs)}})
}

// options is the instance options of the trial routes over the server list s.
func (f *trialFixture) options(s []servers.Spec) Options {
	return Options{HomeDir: f.home, Authenticator: auth.NewAuthenticator(nil), RouteNodes: map[string][]map[string]any{"alice": {nodeMap(f.ssAddr)}, "bob": {nodeMap(f.ssAddr)}, "carol": {nodeMap(f.vmAddr)}, "dave": {nodeMap(f.trAddr)}, "erin": {nodeMap(f.rlAddr)}}, AccountRoute: map[string]string{"alice": "alice", "bob": "bob", "carol": "carol", "dave": "dave", "erin": "erin"}, Servers: s}
}

// start opens the instance with Bob and trusts the Trojan material; it keeps
// the short id of the Reality key.
func (f *trialFixture) start(ctx context.Context, t *testing.T) {
	t.Helper()
	inst, e := Start(ctx, f.options(f.specs(true, false)))
	if e != nil {
		t.Fatal(e)
	}
	f.inst = inst
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if e := inst.Close(); e != nil {
			t.Errorf("close: %v", e)
		}
	})
	servertest.TrustForTest(t, f.tm)
	f.sid = strings.Split(f.rm["short_ids"], ",")[0]
	if _, e = hex.DecodeString(f.sid); e != nil {
		t.Fatal(e)
	}
}

// dialClients makes one client per trial route and closes them at cleanup.
func (f *trialFixture) dialClients(t *testing.T) {
	t.Helper()
	var e error
	f.ss1, e = outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "trial-ss-a", Server: "127.0.0.1", Port: f.port, Password: f.ssA["password"], Cipher: f.ssParams["cipher"]})
	if e != nil {
		t.Fatal(e)
	}
	f.ss2, e = outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "trial-ss-b", Server: "127.0.0.1", Port: f.port, Password: f.ssB["password"], Cipher: f.ssParams["cipher"]})
	if e != nil {
		t.Fatal(e)
	}
	vmc, e := outbound.NewVmess(outbound.VmessOption{Name: "trial-vm", Server: "127.0.0.1", Port: f.port, UUID: f.vm["uuid"], AlterID: 0, Cipher: "auto", Network: "tcp"})
	if e != nil {
		t.Fatal(e)
	}
	trc, e := outbound.NewTrojan(outbound.TrojanOption{Name: "trial-tr", Server: "127.0.0.1", Port: f.port, Password: f.ts["password"], SNI: f.trParams["sni"], Fingerprint: f.tm["fingerprint"]})
	if e != nil {
		t.Fatal(e)
	}
	rlc, e := outbound.NewVless(outbound.VlessOption{Name: "trial-rl", Server: "127.0.0.1", Port: f.port, UUID: f.rs["uuid"], Flow: "xtls-rprx-vision", TLS: true, Network: "tcp", Encryption: "none", ClientFingerprint: "chrome", ServerName: f.rlParams["sni"], RealityOpts: outbound.RealityOptions{PublicKey: f.rm["public_key"], ShortID: f.sid}})
	if e != nil {
		t.Fatal(e)
	}
	f.vmc, f.trc, f.rlc = vmc, trc, rlc
	for _, c := range []C.ProxyAdapter{f.ss1, f.ss2, f.vmc, f.trc, f.rlc} {
		t.Cleanup(func() { _ = c.Close() })
	}
}

// roundTrips sends one payload over every trial route and checks that the
// route's recorder saw it.
func (f *trialFixture) roundTrips(t *testing.T) {
	t.Helper()
	for _, x := range []struct {
		name string
		c    C.ProxyAdapter
		r    *chainRecorder
	}{{"ssa", f.ss1, f.ssRec}, {"ssb", f.ss2, f.ssRec}, {"vm", f.vmc, f.vmRec}, {"tr", f.trc, f.trRec}, {"rl", f.rlc, f.rlRec}} {
		before := len(x.r.snapshot())
		multiportTrialRoundTrip(t, x.c, f.backendIP, f.backendPort, "trial-"+x.name)
		waitServerConnections(t, f.inst)
		if len(x.r.snapshot()) <= before {
			t.Fatalf("route recorder not used: %s", x.name)
		}
	}
}

// replayRefused captures Alice's first flight and replays it raw: the replay
// counter must move, and the route must not see the replay. done is the
// cancellation of the test context.
func (f *trialFixture) replayRefused(t *testing.T, done <-chan struct{}) {
	t.Helper()
	meta := &C.Metadata{NetWork: C.TCP, DstIP: f.backendIP, DstPort: f.backendPort}
	beforeCapture := len(f.ssRec.snapshot())
	flight := multiportTrialCaptureFirstFlight(t, f.ss1, f.port, meta)
	waitServerConnections(t, f.inst)
	if len(f.ssRec.snapshot()) <= beforeCapture {
		t.Fatal("captured first flight did not route")
	}
	before := len(f.ssRec.snapshot())
	replay := uint64(0)
	for _, i := range f.inst.Servers() {
		if i.Name == "trial-ss-alice" {
			replay = i.Replayed
		}
	}
	multiportTrialRawReplay(t, f.port, flight)
	f.waitReplayCounted(t, done, replay)
	if len(f.ssRec.snapshot()) != before {
		t.Fatal("refused replay reached route")
	}
}

// waitReplayCounted waits until Alice's replay counter passes replay.
func (f *trialFixture) waitReplayCounted(t *testing.T, done <-chan struct{}, replay uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, i := range f.inst.Servers() {
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
		case <-done:
			t.Fatal("waiting replay")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// removeBob takes Bob's member out: his secret must not route any more.
func (f *trialFixture) removeBob(t *testing.T) {
	t.Helper()
	if e := f.inst.Reconfigure(f.options(f.specs(false, false))); e != nil {
		t.Fatal(e)
	}
	beforeRemoved := len(f.ssRec.snapshot())
	removedCtx, removedCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	removedConn, removedErr := f.ss2.DialContext(removedCtx, &C.Metadata{NetWork: C.TCP, DstIP: f.backendIP, DstPort: f.backendPort})
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
	waitServerConnections(t, f.inst)
	if len(f.ssRec.snapshot()) != beforeRemoved {
		t.Fatal("removed Bob member reached the route recorder")
	}
}

// readdBob puts Bob back: his route must serve again while Alice keeps serving.
func (f *trialFixture) readdBob(t *testing.T) {
	t.Helper()
	if e := f.inst.Reconfigure(f.options(f.specs(true, false))); e != nil {
		t.Fatalf("re-add Bob member: %v", e)
	}
	beforeReadded := len(f.ssRec.snapshot())
	multiportTrialRoundTrip(t, f.ss2, f.backendIP, f.backendPort, "re-added-member")
	waitServerConnections(t, f.inst)
	if len(f.ssRec.snapshot()) <= beforeReadded {
		t.Fatal("re-added Bob member did not route")
	}
	multiportTrialRoundTrip(t, f.ss1, f.backendIP, f.backendPort, "still-serving")
}

// rollbackRefused checks that a duplicate key is refused, that the refused
// change leaves every server in place, and that every other route still works.
func (f *trialFixture) rollbackRefused(t *testing.T) {
	t.Helper()
	if e := f.inst.Reconfigure(f.options(f.specs(true, true))); e == nil {
		t.Fatal("duplicate key accepted")
	}
	for _, name := range []string{"trial-ss-alice", "trial-vmess", "trial-trojan", "trial-reality"} {
		ok := false
		for _, i := range f.inst.Servers() {
			if i.Name == name {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("rollback lost %s", name)
		}
	}
	for _, c := range []C.ProxyAdapter{f.vmc, f.trc, f.rlc} {
		multiportTrialRoundTrip(t, c, f.backendIP, f.backendPort, "after-rollback")
	}
}

func TestMultiportTrialSharedSS2022VMessTLSAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := newTrialFixture(t)
	f.start(ctx, t)
	f.dialClients(t)
	f.roundTrips(t)
	f.replayRefused(t, ctx.Done())
	f.removeBob(t)
	f.readdBob(t)
	f.rollbackRefused(t)
}
