//go:build integration

package servers_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // G501: AnyTLS padding-md5 is a protocol-defined digest, not a security control
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/anytls/padding"
	"github.com/metacubex/mihomo/transport/vmess"
	M "github.com/metacubex/sing/common/metadata"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
)

const anyTLSTestTimeout = 5 * time.Second

type anyTLSRecorder struct {
	mu      sync.Mutex
	targets []string
	wg      sync.WaitGroup
}

func anyTLSStartRecorder(t *testing.T) (*anyTLSRecorder, string) {
	t.Helper()
	r := &anyTLSRecorder{}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.wg.Add(1)
			go func() { defer r.wg.Done(); r.serve(c) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(anyTLSTestTimeout):
			t.Errorf("recorder accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { r.wg.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(anyTLSTestTimeout):
			t.Errorf("recorder connections did not stop")
		}
	})
	return r, ln.Addr().String()
}
func (r *anyTLSRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}
func (r *anyTLSRecorder) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	if c.SetDeadline(time.Now().Add(anyTLSTestTimeout)) != nil {
		return
	}
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		b := make([]byte, 1)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		n := make([]byte, int(b[0]))
		if _, err := io.ReadFull(c, n); err != nil {
			return
		}
		host = string(n)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p := make([]byte, 2)
	if _, err := io.ReadFull(c, p); err != nil {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	r.mu.Lock()
	r.targets = append(r.targets, dest)
	r.mu.Unlock()
	up, err := (&net.Dialer{Timeout: anyTLSTestTimeout}).DialContext(context.Background(), "tcp", dest)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = up.Close() }()
	if up.SetDeadline(time.Now().Add(anyTLSTestTimeout)) != nil {
		return
	}
	if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	remaining := 2
	select {
	case <-done:
		remaining--
	case <-time.After(anyTLSTestTimeout):
	}
	_ = c.Close()
	_ = up.Close()
	timer := time.NewTimer(anyTLSTestTimeout)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case <-done:
			remaining--
		case <-timer.C:
			return
		}
	}
}
func anyTLSStartEcho(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() { _ = c.Close() }()
				if c.SetDeadline(time.Now().Add(anyTLSTestTimeout)) == nil {
					_, _ = io.Copy(c, c)
				}
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(anyTLSTestTimeout):
			t.Errorf("echo accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { wg.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(anyTLSTestTimeout):
			t.Errorf("echo connections did not stop")
		}
	})
	return ln.Addr().String()
}
func anyTLSWaitConnections(t *testing.T, i *core.Instance) {
	t.Helper()
	deadline := time.NewTimer(anyTLSTestTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		n := 0
		for _, v := range i.Servers() {
			n += v.Connections
		}
		if n == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("AnyTLS server connections remain: %d", n)
		}
	}
}
func anyTLSOptions(home string, specs []servers.Spec, aliceAddress, bobAddress string, global []netip.Prefix, account map[string][]netip.Prefix) core.Options {
	return core.Options{
		HomeDir: home,
		Servers: specs,
		RouteNodes: map[string][]map[string]any{
			"alice": {anyTLSNodeMap(aliceAddress)},
			"bob":   {anyTLSNodeMap(bobAddress)},
		},
		AccountRoute:          map[string]string{"alice": "alice", "bob": "bob"},
		AllowedClients:        global,
		AccountAllowedClients: account,
	}
}

func anyTLSNodeMap(address string) map[string]any {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		panic("invalid test recorder address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic("invalid test recorder port")
	}
	return map[string]any{"name": "anytls-node", "type": "socks5", "server": host, "port": port}
}

// Synchronous wire probes avoid the vendored Stream.Write/closeLocally race on rejected sessions, including the adapter's initial destination write.
func anyTLSWireProbe(port int, password, fingerprint, backend string, policy bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), anyTLSTestTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	if err = raw.SetDeadline(time.Now().Add(anyTLSTestTimeout)); err != nil {
		return err
	}
	tlsConn, err := vmess.StreamTLSConn(ctx, raw, &vmess.TLSConfig{Host: "localhost", FingerPrint: fingerprint, ClientFingerprint: "chrome"})
	if err != nil {
		return err
	}
	defer func() { _ = tlsConn.Close() }()
	digest := sha256.Sum256([]byte(password))
	prelude := append(append([]byte(nil), digest[:]...), 0, 0)
	if _, err = tlsConn.Write(prelude); err != nil {
		return err
	}
	if !policy {
		var b [1]byte
		_, err = tlsConn.Read(b[:])
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return err
		}
		return nil
	}
	settings := []byte(fmt.Sprintf("v=2\nclient=netivri-test\npadding-md5=%x", md5.Sum(padding.DefaultPaddingScheme))) //nolint:gosec // G401: md5 is the AnyTLS padding-md5 protocol digest
	if err = anyTLSWriteFrame(tlsConn, 4, 0, settings); err != nil {
		return err
	}
	if err = anyTLSWriteFrame(tlsConn, 1, 1, nil); err != nil {
		return err
	}
	var destination bytes.Buffer
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	backendPort16, ok := servertest.Uint16(backendPort)
	if !ok {
		return errors.New("backend port out of range")
	}
	if err = M.SocksaddrSerializer.WriteAddrPort(&destination, M.ParseSocksaddrHostPort(host, backendPort16)); err != nil {
		return err
	}
	if err = anyTLSWriteFrame(tlsConn, 2, 1, destination.Bytes()); err != nil {
		return err
	}
	seenSynAck, seenFin := false, false
	for !seenFin {
		var h [7]byte
		if _, err = io.ReadFull(tlsConn, h[:]); err != nil {
			return err
		}
		cmd, sid, n := h[0], binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint16(h[5:])
		if n > 4096 {
			return fmt.Errorf("AnyTLS frame too large: %d", n)
		}
		payload := make([]byte, int(n))
		if _, err = io.ReadFull(tlsConn, payload); err != nil {
			return err
		}
		if sid != 1 {
			continue
		}
		switch cmd {
		case 7:
			seenSynAck = true
		case 2:
			if len(payload) != 0 {
				return errors.New("unexpected echoed data")
			}
		case 3:
			seenFin = true
		default:
		}
	}
	if !seenSynAck {
		return errors.New("missing stream SYNACK")
	}
	return nil
}
func anyTLSWriteFrame(w io.Writer, cmd byte, sid uint32, payload []byte) error {
	length, ok := servertest.Uint16(len(payload))
	if !ok {
		return errors.New("AnyTLS payload too large")
	}
	h := make([]byte, 7)
	h[0] = cmd
	binary.BigEndian.PutUint32(h[1:5], sid)
	binary.BigEndian.PutUint16(h[5:7], length)
	if _, err := w.Write(h); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func anyTLSDial(port int, password, fingerprint string, backend string) error {
	client, err := outbound.NewAnyTLS(outbound.AnyTLSOption{
		Name:                     "anytls-test",
		Server:                   "127.0.0.1",
		Port:                     port,
		Password:                 password,
		SNI:                      "localhost",
		Fingerprint:              fingerprint,
		ClientFingerprint:        "chrome",
		UDP:                      false,
		IdleSessionCheckInterval: 30,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), anyTLSTestTimeout)
	defer cancel()
	defer func() { _ = client.Close() }()
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	dstPort, ok := servertest.Uint16(backendPort)
	if !ok {
		return errors.New("backend port out of range")
	}
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: dstPort}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(anyTLSTestTimeout)); err != nil {
		return err
	}
	payload := []byte("anytls-route-echo")
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
func anyTLSCheckTargets(t *testing.T, recorder *anyTLSRecorder, backend string, want int) {
	t.Helper()
	got := recorder.snapshot()
	if len(got) != want {
		t.Fatalf("recorder target count=%d, want %d", len(got), want)
	}
	for _, target := range got {
		if target != backend {
			t.Fatalf("recorder target=%q, want backend", target)
		}
	}
}
func anyTLSDropped(i *core.Instance) uint64 {
	for _, v := range i.Servers() {
		if v.Name == "anytls-entry" {
			return v.DroppedClients
		}
	}
	return 0
}

// anyTLSReloadRun holds the state shared by the phases of TestAnyTLSServerRoutesAndReload.
type anyTLSReloadRun struct {
	t             *testing.T
	home          string
	aliceNode     *anyTLSRecorder
	bobNode       *anyTLSRecorder
	aliceAddress  string
	bobAddress    string
	backend       string
	params        map[string]string
	material      map[string]string
	protocol      servers.Protocol
	alicePassword string
	bobPassword   string
	port          int
	inst          *core.Instance
	closed        bool
}

func TestAnyTLSServerRoutesAndReload(t *testing.T) {
	r := newAnyTLSReloadRun(t)
	r.checkRoutes()
	rotated := r.rotatePassword()
	beforeAlice := r.checkPolicies(rotated)
	r.checkResetAndClose(rotated, beforeAlice)
	t.Run("CA trust without fingerprint", func(t *testing.T) {
		r.checkCATrust(t, rotated)
	})
}

func newAnyTLSReloadRun(t *testing.T) *anyTLSReloadRun {
	t.Helper()
	aliceNode, aliceAddress := anyTLSStartRecorder(t)
	bobNode, bobAddress := anyTLSStartRecorder(t)
	backend := anyTLSStartEcho(t)
	protocol, ok := servers.Lookup("anytls")
	if !ok {
		t.Fatal("AnyTLS protocol is not registered")
	}
	params := map[string]string{"public_host": "localhost", "sni": "localhost"}
	material, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal(err)
	}
	r := &anyTLSReloadRun{
		t: t, aliceNode: aliceNode, bobNode: bobNode,
		aliceAddress: aliceAddress, bobAddress: bobAddress, backend: backend,
		params: params, material: material, protocol: protocol,
	}
	r.alicePassword, r.bobPassword = r.newPassword(), r.newPassword()
	r.home = t.TempDir()
	r.port = servertest.StartOnFreePort(t, func(p int) error {
		var e error
		r.inst, e = core.Start(context.Background(), anyTLSOptions(r.home, []servers.Spec{r.makeSpec(p, r.alicePassword)}, r.aliceAddress, r.bobAddress, nil, nil))
		return e
	})
	t.Cleanup(func() {
		if !r.closed {
			defer func() {
				if err := r.inst.Close(); err != nil {
					t.Errorf("close instance: %v", err)
				}
			}()
			anyTLSWaitConnections(t, r.inst)
		}
	})
	return r
}

func (r *anyTLSReloadRun) newPassword() string {
	v, e := r.protocol.NewSecret("alice", r.params)
	if e != nil {
		r.t.Fatal("could not generate test password")
	}
	return v["password"]
}

func (r *anyTLSReloadRun) makeSpec(port int, alice string) servers.Spec {
	return servers.Spec{
		Name:     "anytls-entry",
		Protocol: "anytls",
		Bind:     "127.0.0.1",
		Port:     port,
		Accounts: []string{"alice", "bob"},
		Params:   r.params,
		Material: servers.SecretMap(r.material),
		Users:    servers.UserSecrets{"alice": {"password": alice}, "bob": {"password": r.bobPassword}},
	}
}

func (r *anyTLSReloadRun) dial(password, fingerprint string) error {
	defer anyTLSWaitConnections(r.t, r.inst)
	return anyTLSDial(r.port, password, fingerprint, r.backend)
}

func (r *anyTLSReloadRun) probe(password, fingerprint string, policy bool) error {
	defer anyTLSWaitConnections(r.t, r.inst)
	return anyTLSWireProbe(r.port, password, fingerprint, r.backend, policy)
}

func (r *anyTLSReloadRun) checkRoutes() {
	t := r.t
	if err := r.dial(r.alicePassword, r.material["fingerprint"]); err != nil {
		t.Fatalf("alice route failed: %v", err)
	}
	if err := r.dial(r.alicePassword, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "certificate fingerprints do not match") {
		t.Fatalf("wrong fingerprint error=%v, want certificate fingerprint mismatch", err)
	}
	anyTLSCheckTargets(t, r.aliceNode, r.backend, 1)
	anyTLSCheckTargets(t, r.bobNode, r.backend, 0)
	if err := r.dial(r.bobPassword, r.material["fingerprint"]); err != nil {
		t.Fatalf("bob route failed: %v", err)
	}
	anyTLSCheckTargets(t, r.aliceNode, r.backend, 1)
	anyTLSCheckTargets(t, r.bobNode, r.backend, 1)
	if err := r.probe(r.newPassword(), r.material["fingerprint"], false); err != nil {
		t.Fatalf("wrong-password refusal probe: %v", err)
	}
}

func (r *anyTLSReloadRun) rotatePassword() string {
	t := r.t
	rotated := r.newPassword()
	if err := r.inst.Reconfigure(anyTLSOptions(r.home, []servers.Spec{r.makeSpec(r.port, rotated)}, r.aliceAddress, r.bobAddress, nil, nil)); err != nil {
		t.Fatalf("rotate password: %v", err)
	}
	if err := r.dial(rotated, r.material["fingerprint"]); err != nil {
		t.Fatalf("rotated password failed: %v", err)
	}
	if err := r.probe(r.alicePassword, r.material["fingerprint"], false); err != nil {
		t.Fatalf("old-password refusal probe: %v", err)
	}
	if err := r.dial(r.bobPassword, r.material["fingerprint"]); err != nil {
		t.Fatalf("bob password after rotation failed: %v", err)
	}
	return rotated
}

// checkPolicies applies global and account denials and returns the alice route count before them.
func (r *anyTLSReloadRun) checkPolicies(rotated string) int {
	t := r.t
	beforeAlice, beforeBob := len(r.aliceNode.snapshot()), len(r.bobNode.snapshot())
	global := anyTLSOptions(r.home, []servers.Spec{r.makeSpec(r.port, rotated)}, r.aliceAddress, r.bobAddress, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	if err := r.inst.Reconfigure(global); err != nil {
		t.Fatalf("set global policy: %v", err)
	}
	// Synchronous raw probes avoid the vendored stream close/write race, including CreateProxy's initial destination write.
	if err := r.probe(rotated, r.material["fingerprint"], true); err != nil {
		t.Fatalf("global policy refusal probe: %v", err)
	}
	if anyTLSDropped(r.inst) != 1 {
		t.Fatalf("global denial count=%d, want 1", anyTLSDropped(r.inst))
	}
	if len(r.aliceNode.snapshot()) != beforeAlice || len(r.bobNode.snapshot()) != beforeBob {
		t.Fatal("global-denied client reached a node route")
	}
	account := anyTLSOptions(r.home, []servers.Spec{r.makeSpec(r.port, rotated)}, r.aliceAddress, r.bobAddress, nil, map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := r.inst.Reconfigure(account); err != nil {
		t.Fatalf("set account policy: %v", err)
	}
	if err := r.probe(rotated, r.material["fingerprint"], true); err != nil {
		t.Fatalf("account policy refusal probe: %v", err)
	}
	if anyTLSDropped(r.inst) != 2 {
		t.Fatalf("account denial count=%d, want 2", anyTLSDropped(r.inst))
	}
	if len(r.aliceNode.snapshot()) != beforeAlice || len(r.bobNode.snapshot()) != beforeBob {
		t.Fatal("account-denied client reached a node route")
	}
	return beforeAlice
}

func (r *anyTLSReloadRun) checkResetAndClose(rotated string, beforeAlice int) {
	t := r.t
	if err := r.inst.Reconfigure(anyTLSOptions(r.home, []servers.Spec{r.makeSpec(r.port, rotated)}, r.aliceAddress, r.bobAddress, nil, nil)); err != nil {
		t.Fatalf("reset client policy: %v", err)
	}
	if err := r.dial(rotated, r.material["fingerprint"]); err != nil {
		t.Fatalf("allowed client after policy reset: %v", err)
	}
	anyTLSCheckTargets(t, r.aliceNode, r.backend, beforeAlice+1)
	anyTLSWaitConnections(t, r.inst)
	if err := r.inst.Close(); err != nil {
		t.Fatal(err)
	}
	r.closed = true
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.port)))
	if err != nil {
		t.Fatalf("port not released after Close: %v", err)
	}
	_ = ln.Close()
}

func (r *anyTLSReloadRun) checkCATrust(t *testing.T, rotated string) {
	var caInstance *core.Instance
	caPort := servertest.StartOnFreePort(t, func(p int) error {
		var startErr error
		caInstance, startErr = core.Start(context.Background(), anyTLSOptions(r.home, []servers.Spec{r.makeSpec(p, rotated)}, r.aliceAddress, r.bobAddress, nil, nil))
		return startErr
	})
	caClosed := false
	t.Cleanup(func() {
		if !caClosed {
			defer func() {
				if err := caInstance.Close(); err != nil {
					t.Errorf("close CA-trust instance: %v", err)
				}
			}()
			anyTLSWaitConnections(t, caInstance)
		}
	})
	servertest.TrustForTest(t, r.material)
	if err := anyTLSDial(caPort, rotated, "", r.backend); err != nil {
		t.Fatalf("CA-trusted connection without pin: %v", err)
	}
	anyTLSWaitConnections(t, caInstance)
	if err := caInstance.Close(); err != nil {
		t.Fatal(err)
	}
	caClosed = true
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(caPort)))
	if err != nil {
		t.Fatalf("CA-trust port not released: %v", err)
	}
	_ = listener.Close()
}
