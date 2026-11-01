//go:build integration

package servers_test

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

func startRealityCamouflageTarget(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.test"}, DNSNames: []string{"reality.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}})
	var workers sync.WaitGroup
	var conns sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := tlsLn.Accept()
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
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = tlsLn.Close()
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
	return ln.Addr().String()
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
	dest := startRealityCamouflageTarget(t)
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
		return core.Options{HomeDir: home, RouteNodes: map[string][]map[string]any{
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
	dial := func(uuid, sid, pub string) error {
		ctx, cancel := context.WithTimeout(context.Background(), vlessTestTimeout)
		defer cancel()
		client, err := outbound.NewVless(outbound.VlessOption{Name: "loopback-vless", Server: "127.0.0.1", Port: port, UUID: uuid, Flow: "xtls-rprx-vision", TLS: true, Network: "tcp", Encryption: "none", ClientFingerprint: "chrome", ServerName: "reality.test", RealityOpts: outbound.RealityOptions{PublicKey: pub, ShortID: sid}})
		if err != nil {
			return err
		}
		defer client.Close()
		conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: targetIP, DstPort: targetPort})
		if err != nil {
			return err
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(vlessTestTimeout)); err != nil {
			return err
		}
		payload := []byte("vless-reality-route-echo")
		if _, err = conn.Write(payload); err != nil {
			return err
		}
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(conn, got); err != nil {
			return err
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if err := dial(users["alice"]["uuid"], shortID, publicKey); err != nil {
		t.Fatalf("Alice echo: %v", err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 0, backend)
	if err := dial(users["bob"]["uuid"], shortID, publicKey); err != nil {
		t.Fatalf("Bob echo: %v", err)
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
		if err := dial(args.args[0], args.args[1], args.args[2]); err == nil {
			t.Errorf("wrong %s completed echo", args.name)
		}
		waitVlessConnections(t, inst)
	}
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 1, backend)

	denied := options(makeSpec(users), map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := inst.Reconfigure(denied); err != nil {
		t.Fatalf("deny Alice client: %v", err)
	}
	if err := dial(users["alice"]["uuid"], shortID, publicKey); err == nil {
		t.Fatal("Alice account allowlist accepted loopback")
	}
	waitVlessConnections(t, inst)
	if dropped := inst.Servers()[0].DroppedClients; dropped == 0 {
		t.Fatal("Alice denial did not increment dropped-client counter")
	}
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 1, backend)
	if err := dial(users["bob"]["uuid"], shortID, publicKey); err != nil {
		t.Fatalf("Bob while Alice policy denies: %v", err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 1, 2, backend)
	allowed := options(makeSpec(users), nil)
	if err := inst.Reconfigure(allowed); err != nil {
		t.Fatalf("restore Alice client policy: %v", err)
	}
	if err := dial(users["alice"]["uuid"], shortID, publicKey); err != nil {
		t.Fatalf("Alice after policy restore: %v", err)
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
	if err := dial(oldAliceUUID, shortID, publicKey); err == nil {
		t.Fatal("old Alice UUID still works after rotation")
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 2, 2, backend)
	if err := dial(rotatedUsers["alice"]["uuid"], shortID, publicKey); err != nil {
		t.Fatalf("rotated Alice UUID: %v", err)
	}
	waitVlessConnections(t, inst)
	assertVlessRouteCounts(t, aliceNode, bobNode, 3, 2, backend)
	users = rotatedUsers

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
