//go:build integration

package servers_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
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

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	mihomolog "github.com/metacubex/mihomo/log"
)

const vlessTestTimeout = 10 * time.Second

type vlessNodeRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
	conns   sync.Map
}

func (r *vlessNodeRecorder) record(target string) {
	r.mu.Lock()
	r.targets = append(r.targets, target)
	r.mu.Unlock()
}

func (r *vlessNodeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}

func (r *vlessNodeRecorder) serveConn(c net.Conn) {
	defer r.workers.Done()
	defer r.conns.Delete(c)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(vlessTestTimeout))
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(c, b)
		return b, err
	}
	h, err := read(2)
	if err != nil || h[0] != 5 {
		return
	}
	if _, err = read(int(h[1])); err != nil {
		return
	}
	if _, err = c.Write([]byte{5, 0}); err != nil {
		return
	}
	req, err := read(4)
	if err != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b, err := read(4)
		if err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, err := read(1)
		if err != nil {
			return
		}
		b, err := read(int(n[0]))
		if err != nil {
			return
		}
		host = string(b)
	case 4:
		b, err := read(16)
		if err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p, err := read(2)
	if err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	r.record(target)
	ctx, cancel := context.WithTimeout(context.Background(), vlessTestTimeout)
	defer cancel()
	d := net.Dialer{}
	up, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(vlessTestTimeout))
	if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}

func startVlessNodeRecorder(t *testing.T) (*vlessNodeRecorder, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := new(vlessNodeRecorder)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.conns.Store(c, struct{}{})
			r.workers.Add(1)
			go r.serveConn(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-acceptDone:
		case <-time.After(vlessTestTimeout):
			t.Errorf("recorder accept loop did not stop")
		}
		r.conns.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		if err := waitVlessWorkers(&r.workers); err != nil {
			t.Errorf("recorder workers: %v", err)
		}
	})
	return r, ln.Addr().String()
}

func waitVlessWorkers(wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(vlessTestTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func startVlessEcho(t *testing.T) (netip.Addr, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var conns sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(c, struct{}{})
			workers.Add(1)
			go func(c net.Conn) {
				defer workers.Done()
				defer conns.Delete(c)
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(vlessTestTimeout))
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-acceptDone:
		case <-time.After(vlessTestTimeout):
			t.Errorf("echo accept loop did not stop")
		}
		conns.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		if err := waitVlessWorkers(&workers); err != nil {
			t.Errorf("echo workers: %v", err)
		}
	})
	a := ln.Addr().(*net.TCPAddr)
	return netip.MustParseAddr("127.0.0.1"), uint16(a.Port)
}

type realityTargetObservation struct {
	mu          sync.Mutex
	accepted    int
	handshakes  int
	serverNames []string
	alpn        [][]string
	versions    []uint16
	errors      []string
	records     []string
}

type tlsRecordObserverConn struct {
	net.Conn
	mu          sync.Mutex
	pending     []byte
	observation *realityTargetObservation
}

func (c *tlsRecordObserverConn) Write(p []byte) (int, error) {
	written, err := c.Conn.Write(p)
	c.mu.Lock()
	c.pending = append(c.pending, p[:written]...)
	for len(c.pending) >= 5 {
		recordLen := 5 + int(binary.BigEndian.Uint16(c.pending[3:5]))
		if len(c.pending) < recordLen {
			break
		}
		c.observation.mu.Lock()
		c.observation.records = append(c.observation.records, fmt.Sprintf("type=%d version=%04x length=%d", c.pending[0], binary.BigEndian.Uint16(c.pending[1:3]), recordLen-5))
		c.observation.mu.Unlock()
		c.pending = c.pending[recordLen:]
	}
	c.mu.Unlock()
	return written, err
}

func (o *realityTargetObservation) acceptedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.accepted
}

func waitRealityTargetAccepted(t *testing.T, o *realityTargetObservation) int {
	t.Helper()
	timer := time.NewTimer(vlessTestTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if accepted := o.acceptedCount(); accepted > 0 {
			return accepted
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return 0
		}
	}
}

func (o *realityTargetObservation) snapshot() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return fmt.Sprintf("accepted=%d handshakes=%d sni=%v alpn=%v tls_versions=%v handshake_errors=%v target_records=%v", o.accepted, o.handshakes, o.serverNames, o.alpn, o.versions, o.errors, o.records)
}

func startRealityCamouflageTarget(t *testing.T) (string, *realityTargetObservation) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dnsNames := make([]string, 33)
	dnsNames[0] = "reality.test"
	for i := 1; i < len(dnsNames); i++ {
		dnsNames[i] = fmt.Sprintf("reality-camouflage-%02d-%s.test", i, strings.Repeat("x", 40))
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.test"}, DNSNames: dnsNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	if len(der) < 2048 || len(der)+13+22 <= 512 || len(der)+13+22 >= 16384 {
		t.Fatalf("camouflage certificate DER size %d outside intended record margin", len(der))
	}
	t.Logf("camouflage certificate DER=%d bytes; TLS 1.3 Certificate handshake message=%d bytes; minimum encrypted record size=%d bytes (actual records are observed separately)", len(der), len(der)+13, len(der)+13+22)
	// Reality pads to target lengths: the certificate and RSA signature add room;
	// 4096 ticket bytes cover its synthetic ticket, while Finished uses the same hash.
	ticketPadding := make([]byte, 4096)
	if _, err := rand.Read(ticketPadding); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	obs := new(realityTargetObservation)
	tlsConfig := &tls.Config{
		Certificates:           []tls.Certificate{cert},
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		CurvePreferences:       []tls.CurveID{tls.X25519},
		NextProtos:             []string{"h2"},
		SessionTicketsDisabled: false,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			obs.mu.Lock()
			obs.serverNames = append(obs.serverNames, hello.ServerName)
			obs.alpn = append(obs.alpn, append([]string(nil), hello.SupportedProtos...))
			obs.mu.Unlock()
			return nil, nil
		},
	}
	tlsConfig.WrapSession = func(cs tls.ConnectionState, state *tls.SessionState) ([]byte, error) {
		state.Extra = append(state.Extra, ticketPadding)
		return tlsConfig.EncryptTicket(cs, state)
	}
	var workers sync.WaitGroup
	var conns sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			rawConn := &tlsRecordObserverConn{Conn: c, observation: obs}
			tc := tls.Server(rawConn, tlsConfig)
			obs.mu.Lock()
			obs.accepted++
			obs.mu.Unlock()
			conns.Store(tc, struct{}{})
			workers.Add(1)
			go func(conn net.Conn) {
				defer workers.Done()
				defer conns.Delete(conn)
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(vlessTestTimeout))
				if tlsConn, ok := conn.(*tls.Conn); ok {
					err := tlsConn.Handshake()
					state := tlsConn.ConnectionState()
					obs.mu.Lock()
					if err == nil {
						obs.handshakes++
						obs.versions = append(obs.versions, state.Version)
					} else {
						obs.errors = append(obs.errors, err.Error())
					}
					obs.mu.Unlock()
					if err != nil {
						return
					}
				}
				_, _ = io.Copy(io.Discard, conn)
			}(tc)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-acceptDone:
		case <-time.After(vlessTestTimeout):
			t.Errorf("Reality target accept loop did not stop")
		}
		conns.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		if err := waitVlessWorkers(&workers); err != nil {
			t.Errorf("Reality target workers: %v", err)
		}
	})
	return ln.Addr().String(), obs
}

func serverPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func assertVlessRouteCounts(t *testing.T, alice, bob *vlessNodeRecorder, aliceWant, bobWant int, backend string) {
	t.Helper()
	aliceTargets, bobTargets := alice.snapshot(), bob.snapshot()
	if len(aliceTargets) != aliceWant {
		t.Fatalf("alice recorder got %d entries, want %d", len(aliceTargets), aliceWant)
	}
	if len(bobTargets) != bobWant {
		t.Fatalf("bob recorder got %d entries, want %d", len(bobTargets), bobWant)
	}
	for name, targets := range map[string][]string{"alice": aliceTargets, "bob": bobTargets} {
		for _, target := range targets {
			if target != backend {
				t.Fatalf("%s recorder destination = %q; want only %q", name, target, backend)
			}
		}
	}
}

func assertVlessNoCamouflageRoute(t *testing.T, alice, bob *vlessNodeRecorder, dest string) {
	t.Helper()
	for name, targets := range map[string][]string{"alice": alice.snapshot(), "bob": bob.snapshot()} {
		for _, target := range targets {
			if target == dest {
				t.Errorf("Reality camouflage destination %q was routed through %s node recorder", dest, name)
			}
		}
	}
}

func waitVlessConnections(t *testing.T, inst *core.Instance) {
	t.Helper()
	timer := time.NewTimer(vlessTestTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		active := 0
		for _, server := range inst.Servers() {
			active += server.Connections
		}
		if active == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("VLESS connections did not drain; still have %d", active)
		}
	}
}

func TestVlessRealityServerRoutesAndRejectsInvalidCredentials(t *testing.T) {
	aliceNode, aliceAddr := startVlessNodeRecorder(t)
	bobNode, bobAddr := startVlessNodeRecorder(t)
	targetIP, targetPort := startVlessEcho(t)
	backend := net.JoinHostPort(targetIP.String(), strconv.Itoa(int(targetPort)))
	dest, targetObservation := startRealityCamouflageTarget(t)
	protocol, ok := servers.Lookup("vless-reality")
	if !ok {
		t.Fatal("VLESS Reality protocol is not registered")
	}
	params := map[string]string{"dest": dest, "sni": "reality.test", "flow": "xtls-rprx-vision", "fingerprint": "chrome"}
	material, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal(err)
	}
	newSecret := func(login string) servers.SecretMap {
		values, err := protocol.NewSecret(login, params)
		if err != nil {
			t.Fatal(err)
		}
		return servers.SecretMap(values)
	}
	users := servers.UserSecrets{"alice": newSecret("alice"), "bob": newSecret("bob")}
	port := serverPort(t)
	makeSpec := func(users servers.UserSecrets) servers.Spec {
		return servers.Spec{Name: "vless-test", Protocol: "vless-reality", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice", "bob"}, Params: params, Material: servers.SecretMap(material), Users: users}
	}
	home := testutil.TempHome(t)
	options := func(spec servers.Spec, accountPolicy map[string][]netip.Prefix) core.Options {
		return core.Options{HomeDir: home, Node: map[string]any{"type": "socks5", "server": "127.0.0.1", "port": mustVlessPort(aliceAddr)}, AllowedClients: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, RouteNodes: map[string][]map[string]any{
			"alice": {{"type": "socks5", "server": "127.0.0.1", "port": mustVlessPort(aliceAddr)}},
			"bob":   {{"type": "socks5", "server": "127.0.0.1", "port": mustVlessPort(bobAddr)}},
		}, AccountRoute: map[string]string{"alice": "alice", "bob": "bob"}, Servers: []servers.Spec{spec}, AccountAllowedClients: accountPolicy}
	}
	var inst *core.Instance
	start := func() error {
		var startErr error
		inst, startErr = core.Start(context.Background(), options(makeSpec(users), nil))
		return startErr
	}
	var startErr error
	for attempts := 0; attempts < 3; attempts++ {
		startErr = start()
		if startErr == nil {
			break
		}
		var cliErr *cli.Error
		if !errors.As(startErr, &cliErr) || cliErr.Code != "PORT_IN_USE" {
			t.Fatalf("core.Start: %v", startErr)
		}
		port = serverPort(t)
	}
	if startErr != nil {
		t.Fatalf("core.Start after port retries: %v", startErr)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		defer func() {
			if err := inst.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
		waitVlessConnections(t, inst)
	})
	if mihomolog.Level() != mihomolog.SILENT {
		t.Fatalf("Mihomo log level = %v, want SILENT", mihomolog.Level())
	}
	publicKey := material["public_key"]
	shortIDs := strings.Split(material["short_ids"], ",")
	shortID := shortIDs[0]
	successfulDiagnosticCalls := 0
	type diagnosticResult struct {
		stage string
		err   error
	}
	dial := func(uuid, sid, pub, flow, sni string) diagnosticResult {
		ctx, cancel := context.WithTimeout(context.Background(), vlessTestTimeout)
		defer cancel()
		client, err := outbound.NewVless(outbound.VlessOption{Name: "loopback-vless", Server: "127.0.0.1", Port: port, UUID: uuid, Flow: flow, TLS: true, Network: "tcp", Encryption: "none", ClientFingerprint: "chrome", ServerName: sni, RealityOpts: outbound.RealityOptions{PublicKey: pub, ShortID: sid}})
		if err != nil {
			return diagnosticResult{"new outbound", err}
		}
		defer client.Close()
		conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: targetIP, DstPort: targetPort})
		if err != nil {
			return diagnosticResult{"outbound connect", err}
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(vlessTestTimeout)); err != nil {
			return diagnosticResult{"set deadline", err}
		}
		payload := []byte("vless-reality-route-echo")
		if _, err = conn.Write(payload); err != nil {
			return diagnosticResult{"echo write", err}
		}
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(conn, got); err != nil {
			return diagnosticResult{"echo read", err}
		}
		if string(got) != string(payload) {
			return diagnosticResult{"echo contents", io.ErrUnexpectedEOF}
		}
		return diagnosticResult{"echo complete", nil}
	}
	var runDiagnosticMatrix func()
	runDiagnosticMatrix = func() {
		paddedShortID := shortID + "00000000"
		cases := []struct {
			name string
			flow string
			sid  string
			sni  string
			want bool
		}{
			{"vision baseline", "xtls-rprx-vision", shortID, "reality.test", true},
			{"flow off", "", shortID, "reality.test", true},
			{"zero padded short ID", "xtls-rprx-vision", paddedShortID, "reality.test", true},
			{"wrong SNI", "xtls-rprx-vision", shortID, "wrong.test", false},
		}
		for _, tc := range cases {
			t.Run("diagnostic/"+tc.name, func(t *testing.T) {
				result := dial(users["alice"]["uuid"], tc.sid, publicKey, tc.flow, tc.sni)
				if tc.want && result.err == nil {
					successfulDiagnosticCalls++
					waitVlessConnections(t, inst)
				}
				if tc.want && result.err != nil {
					t.Errorf("expected success; failed at %s: %v", result.stage, result.err)
				}
				if !tc.want && (result.stage != "outbound connect" || result.err == nil) {
					t.Errorf("wrong SNI should fail outbound connect, got stage=%s err=%v", result.stage, result.err)
				}
				t.Logf("target stages: %s", targetObservation.snapshot())
			})
		}
	}
	if firstResult := dial(users["alice"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); firstResult.err != nil {
		if accepted := waitRealityTargetAccepted(t, targetObservation); accepted == 0 {
			t.Errorf("camouflage target accepted no connection after first VLESS attempt; stages: %s", targetObservation.snapshot())
		}
		assertVlessNoCamouflageRoute(t, aliceNode, bobNode, dest)
		assertVlessRouteCounts(t, aliceNode, bobNode, 0, 0, backend)
		t.Errorf("Alice echo stage %s: %v; camouflage target stages: %s", firstResult.stage, firstResult.err, targetObservation.snapshot())
		runDiagnosticMatrix()
		return
	} else {
		if accepted := waitRealityTargetAccepted(t, targetObservation); accepted == 0 {
			t.Errorf("camouflage target accepted no connection after first VLESS attempt; stages: %s", targetObservation.snapshot())
		}
		assertVlessNoCamouflageRoute(t, aliceNode, bobNode, dest)
		waitVlessConnections(t, inst)
		assertVlessRouteCounts(t, aliceNode, bobNode, 1, 0, backend)
	}
	if result := dial(users["bob"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err != nil {
		t.Fatalf("Bob echo at %s: %v", result.stage, result.err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 1, backend)

	badUUID := newSecret("invalid")["uuid"]
	wrongPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongPublic := base64.RawURLEncoding.EncodeToString(wrongPrivate.PublicKey().Bytes())
	used := make(map[string]bool)
	for _, id := range shortIDs {
		used[strings.ToLower(id)] = true
	}
	wrongIDBytes := make([]byte, 4)
	var wrongShortID string
	for {
		if _, err := rand.Read(wrongIDBytes); err != nil {
			t.Fatal(err)
		}
		wrongShortID = hex.EncodeToString(wrongIDBytes)
		if !used[wrongShortID] {
			break
		}
	}
	for _, args := range []struct {
		name string
		args [3]string
	}{
		{"UUID", [3]string{badUUID, shortID, publicKey}},
		{"short ID", [3]string{users["alice"]["uuid"], wrongShortID, publicKey}},
		{"public key", [3]string{users["alice"]["uuid"], shortID, wrongPublic}},
	} {
		result := dial(args.args[0], args.args[1], args.args[2], "xtls-rprx-vision", "reality.test")
		if result.err == nil {
			t.Errorf("wrong %s completed echo at %s", args.name, result.stage)
		} else if args.name == "UUID" && result.stage != "echo read" && result.stage != "echo write" {
			t.Errorf("wrong UUID was rejected before VLESS auth response stage: %s: %v", result.stage, result.err)
		} else if (args.name == "short ID" || args.name == "public key") && result.stage != "outbound connect" {
			t.Errorf("wrong %s should fail Reality transport at outbound connect, got %s: %v", args.name, result.stage, result.err)
		} else {
			t.Logf("wrong %s rejected at %s: %v; target stages: %s", args.name, result.stage, result.err, targetObservation.snapshot())
		}
		waitVlessConnections(t, inst)
	}
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 1, backend)

	denied := options(makeSpec(users), map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := inst.Reconfigure(denied); err != nil {
		t.Fatalf("deny Alice client: %v", err)
	}
	droppedBefore := inst.Servers()[0].DroppedClients
	if result := dial(users["alice"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err == nil {
		t.Fatal("Alice account allowlist accepted loopback")
	} else if result.stage != "echo write" && result.stage != "echo read" {
		t.Errorf("Alice account allowlist rejection stage = %s; want VLESS request stage", result.stage)
	}
	waitVlessConnections(t, inst)
	if dropped := inst.Servers()[0].DroppedClients; dropped <= droppedBefore {
		t.Fatal("Alice denial did not increment dropped-client counter")
	}
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 1, backend)
	if result := dial(users["bob"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err != nil {
		t.Fatalf("Bob while Alice policy denies at %s: %v", result.stage, result.err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 2, backend)
	allowed := options(makeSpec(users), nil)
	if err := inst.Reconfigure(allowed); err != nil {
		t.Fatalf("restore Alice client policy: %v", err)
	}
	if result := dial(users["alice"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err != nil {
		t.Fatalf("Alice after policy restore at %s: %v", result.stage, result.err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 2, 2, backend)

	oldAliceUUID := users["alice"]["uuid"]
	rotatedUsers := servers.UserSecrets{"alice": newSecret("alice"), "bob": users["bob"]}
	if rotatedUsers["alice"]["uuid"] == oldAliceUUID {
		t.Fatal("generated rotation repeated Alice UUID")
	}
	if err := inst.Reconfigure(options(makeSpec(rotatedUsers), nil)); err != nil {
		t.Fatalf("rotate Alice UUID: %v", err)
	}
	if result := dial(oldAliceUUID, shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err == nil {
		t.Fatal("old Alice UUID still works after rotation")
	} else if result.stage != "echo read" && result.stage != "echo write" {
		t.Errorf("old Alice UUID rejected before VLESS authentication stage: %s: %v", result.stage, result.err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 2, 2, backend)
	if result := dial(rotatedUsers["alice"]["uuid"], shortID, publicKey, "xtls-rprx-vision", "reality.test"); result.err != nil {
		t.Fatalf("rotated Alice UUID at %s: %v", result.stage, result.err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 3, 2, backend)
	users = rotatedUsers
	runDiagnosticMatrix()
	assertVlessRouteCounts(t, aliceNode, bobNode, 3+successfulDiagnosticCalls, 2, backend)

	waitVlessConnections(t, inst)
	if err := inst.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("Close did not free port: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustVlessPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		panic(err)
	}
	return n
}
