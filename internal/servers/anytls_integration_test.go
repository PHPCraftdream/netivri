//go:build integration

package servers_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
	defer c.Close()
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
	up, err := net.DialTimeout("tcp", dest, anyTLSTestTimeout)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
				defer c.Close()
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
	defer client.Close()
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		return err
	}
	defer conn.Close()
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
func anyTLSDropped(i *core.Instance, name string) uint64 {
	for _, v := range i.Servers() {
		if v.Name == name {
			return v.DroppedClients
		}
	}
	return 0
}
func TestAnyTLSServerRoutesAndReload(t *testing.T) {
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
	newPassword := func() string {
		v, e := protocol.NewSecret("alice", params)
		if e != nil {
			t.Fatal("could not generate test password")
		}
		return v["password"]
	}
	alicePassword, bobPassword := newPassword(), newPassword()
	makeSpec := func(port int, alice string) servers.Spec {
		return servers.Spec{
			Name:     "anytls-entry",
			Protocol: "anytls",
			Bind:     "127.0.0.1",
			Port:     port,
			Accounts: []string{"alice", "bob"},
			Params:   params,
			Material: servers.SecretMap(material),
			Users:    servers.UserSecrets{"alice": {"password": alice}, "bob": {"password": bobPassword}},
		}
	}
	home := t.TempDir()
	var inst *core.Instance
	port := servertest.StartOnFreePort(t, func(p int) error {
		var e error
		inst, e = core.Start(context.Background(), anyTLSOptions(home, []servers.Spec{makeSpec(p, alicePassword)}, aliceAddress, bobAddress, nil, nil))
		return e
	})
	closed := false
	t.Cleanup(func() {
		if !closed {
			defer func() {
				if err := inst.Close(); err != nil {
					t.Errorf("close instance: %v", err)
				}
			}()
			anyTLSWaitConnections(t, inst)
		}
	})
	dial := func(password, fingerprint string) error {
		defer anyTLSWaitConnections(t, inst)
		return anyTLSDial(port, password, fingerprint, backend)
	}
	if err := dial(alicePassword, material["fingerprint"]); err != nil {
		t.Fatalf("alice route failed: %v", err)
	}
	anyTLSCheckTargets(t, aliceNode, backend, 1)
	anyTLSCheckTargets(t, bobNode, backend, 0)
	if err := dial(bobPassword, material["fingerprint"]); err != nil {
		t.Fatalf("bob route failed: %v", err)
	}
	anyTLSCheckTargets(t, aliceNode, backend, 1)
	anyTLSCheckTargets(t, bobNode, backend, 1)
	if err := dial(newPassword(), material["fingerprint"]); err == nil {
		t.Fatal("wrong password was accepted")
	}
	rotated := newPassword()
	if err := inst.Reconfigure(anyTLSOptions(home, []servers.Spec{makeSpec(port, rotated)}, aliceAddress, bobAddress, nil, nil)); err != nil {
		t.Fatalf("rotate password: %v", err)
	}
	if err := dial(rotated, material["fingerprint"]); err != nil {
		t.Fatalf("rotated password failed: %v", err)
	}
	if err := dial(alicePassword, material["fingerprint"]); err == nil {
		t.Fatal("old password was accepted after rotation")
	}
	if err := dial(bobPassword, material["fingerprint"]); err != nil {
		t.Fatalf("bob password after rotation failed: %v", err)
	}
	beforeAlice, beforeBob := len(aliceNode.snapshot()), len(bobNode.snapshot())
	global := anyTLSOptions(home, []servers.Spec{makeSpec(port, rotated)}, aliceAddress, bobAddress, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	if err := inst.Reconfigure(global); err != nil {
		t.Fatalf("set global policy: %v", err)
	}
	if err := dial(rotated, material["fingerprint"]); err == nil {
		t.Fatal("global client policy accepted loopback")
	}
	if anyTLSDropped(inst, "anytls-entry") != 1 {
		t.Fatalf("global denial count=%d, want 1", anyTLSDropped(inst, "anytls-entry"))
	}
	if len(aliceNode.snapshot()) != beforeAlice || len(bobNode.snapshot()) != beforeBob {
		t.Fatal("global-denied client reached a node route")
	}
	account := anyTLSOptions(home, []servers.Spec{makeSpec(port, rotated)}, aliceAddress, bobAddress, nil, map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := inst.Reconfigure(account); err != nil {
		t.Fatalf("set account policy: %v", err)
	}
	if err := dial(rotated, material["fingerprint"]); err == nil {
		t.Fatal("account client policy accepted loopback")
	}
	if anyTLSDropped(inst, "anytls-entry") != 2 {
		t.Fatalf("account denial count=%d, want 2", anyTLSDropped(inst, "anytls-entry"))
	}
	if len(aliceNode.snapshot()) != beforeAlice || len(bobNode.snapshot()) != beforeBob {
		t.Fatal("account-denied client reached a node route")
	}
	if err := inst.Reconfigure(anyTLSOptions(home, []servers.Spec{makeSpec(port, rotated)}, aliceAddress, bobAddress, nil, nil)); err != nil {
		t.Fatalf("reset client policy: %v", err)
	}
	if err := dial(rotated, material["fingerprint"]); err != nil {
		t.Fatalf("allowed client after policy reset: %v", err)
	}
	anyTLSCheckTargets(t, aliceNode, backend, beforeAlice+1)
	anyTLSWaitConnections(t, inst)
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port not released after Close: %v", err)
	}
	_ = ln.Close()
	t.Run("CA trust without fingerprint", func(t *testing.T) {
		servertest.TrustForTest(t, material)
		var caInstance *core.Instance
		caPort := servertest.StartOnFreePort(t, func(p int) error {
			var startErr error
			caInstance, startErr = core.Start(context.Background(), anyTLSOptions(home, []servers.Spec{makeSpec(p, rotated)}, aliceAddress, bobAddress, nil, nil))
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
		if err := anyTLSDial(caPort, rotated, "", backend); err != nil {
			t.Fatalf("CA-trusted connection without pin: %v", err)
		}
		anyTLSWaitConnections(t, caInstance)
		if err := caInstance.Close(); err != nil {
			t.Fatal(err)
		}
		caClosed = true
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(caPort)))
		if err != nil {
			t.Fatalf("CA-trust port not released: %v", err)
		}
		_ = listener.Close()
	})
}
