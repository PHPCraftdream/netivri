//go:build integration

package servers_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

type vmessRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func (r *vmessRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}
func startVMessRecorder(t *testing.T) (*vmessRecorder, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listen failed")
	}
	r := &vmessRecorder{}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.workers.Add(1)
			go func() {
				defer r.workers.Done()
				serveVMessRecorder(r, c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		r.workers.Wait()
	})
	return r, ln.Addr().String()
}
func readVMessN(c net.Conn, n int) ([]byte, bool) {
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err == nil
}
func serveVMessRecorder(r *vmessRecorder, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	h, ok := readVMessN(c, 2)
	if !ok || h[0] != 5 {
		return
	}
	if _, ok = readVMessN(c, int(h[1])); !ok {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req, ok := readVMessN(c, 4)
	if !ok || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b, ok := readVMessN(c, 4)
		if !ok {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, ok := readVMessN(c, 1)
		if !ok {
			return
		}
		b, ok := readVMessN(c, int(n[0]))
		if !ok {
			return
		}
		host = string(b)
	case 4:
		b, ok := readVMessN(c, 16)
		if !ok {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p, ok := readVMessN(c, 2)
	if !ok {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	r.mu.Lock()
	r.targets = append(r.targets, dest)
	r.mu.Unlock()
	up, err := net.DialTimeout("tcp", dest, 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	copyDone := make(chan struct{}, 2)
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(up, c)
		_ = up.Close()
		copyDone <- struct{}{}
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(c, up)
		_ = c.Close()
		copyDone <- struct{}{}
	}()
	select {
	case <-copyDone:
	case <-time.After(5 * time.Second):
	}
	_ = c.Close()
	_ = up.Close()
	copies.Wait()
}
func vmessNode(addr string) map[string]any {
	return map[string]any{
		"name":   "recorder",
		"type":   "socks5",
		"server": "127.0.0.1",
		"port":   vmessPort(addr),
		"udp":    false,
	}
}
func vmessPort(addr string) int {
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return port
}
func vmessTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return vmessPort(l.Addr().String())
}
func waitVMessConnections(t *testing.T, inst *core.Instance) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		active := 0
		for _, info := range inst.Servers() {
			active += info.Connections
		}
		if active == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("VMess connections did not drain: %d", active)
		}
	}
}
func TestVMessServerMultiuserRouteRotateAndFilters(t *testing.T) {
	protocol, ok := servers.Lookup("vmess")
	if !ok {
		t.Fatal("vmess protocol is not registered")
	}
	aliceNode, aliceAddress := startVMessRecorder(t)
	bobNode, bobAddress := startVMessRecorder(t)
	echo := startVMessEcho(t)
	host, backendText, err := net.SplitHostPort(echo)
	if err != nil {
		t.Fatal("echo endpoint invalid")
	}
	backendPort, err := strconv.Atoi(backendText)
	if err != nil {
		t.Fatal("echo port invalid")
	}
	wantBackend := net.JoinHostPort(host, backendText)
	newUUID := func() string {
		values, err := protocol.NewSecret("alice", nil)
		if err != nil {
			t.Fatal("secret generation failed")
		}
		return values["uuid"]
	}
	port := vmessTestPort(t)
	aliceID, bobID := newUUID(), newUUID()
	makeSpec := func(idA, idB string) servers.Spec {
		return servers.Spec{
			Name:     "entry",
			Protocol: "vmess",
			Bind:     "127.0.0.1",
			Port:     port,
			Accounts: []string{"alice", "bob"},
			Params:   map[string]string{},
			Users: servers.UserSecrets{
				"alice": {"uuid": idA},
				"bob":   {"uuid": idB},
			},
		}
	}
	home := testutil.TempHome(t)
	options := func(spec servers.Spec, global, account []netip.Prefix) core.Options {
		return core.Options{
			HomeDir: home,
			RouteNodes: map[string][]map[string]any{
				"alice": {vmessNode(aliceAddress)},
				"bob":   {vmessNode(bobAddress)},
			},
			AccountRoute:          map[string]string{"alice": "alice", "bob": "bob"},
			Servers:               []servers.Spec{spec},
			AllowedClients:        global,
			AccountAllowedClients: map[string][]netip.Prefix{"alice": account},
		}
	}
	current := makeSpec(aliceID, bobID)
	var inst *core.Instance
	for attempt := 0; attempt < 3; attempt++ {
		inst, err = core.Start(context.Background(), options(current, nil, nil))
		if err == nil {
			break
		}
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" {
			t.Fatalf("core start failed: %v", err)
		}
		port = vmessTestPort(t)
		current.Port = port
	}
	if err != nil {
		t.Fatalf("core start retries failed: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = inst.Close()
		}
	})
	connect := func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		clientOptions := outbound.VmessOption{
			Name:    "client",
			Server:  "127.0.0.1",
			Port:    port,
			UUID:    id,
			AlterID: 0,
			Cipher:  "auto",
			Network: "tcp",
		}
		client, err := outbound.NewVmess(clientOptions)
		if err != nil {
			return err
		}
		defer client.Close()
		clientConn, err := client.DialContext(ctx, &C.Metadata{
			NetWork: C.TCP,
			DstIP:   netip.MustParseAddr(host),
			DstPort: uint16(backendPort),
		})
		if err != nil {
			return err
		}
		defer clientConn.Close()
		if err = clientConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		payload := []byte("vmess-check")
		if _, err = clientConn.Write(payload); err != nil {
			return err
		}
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(clientConn, got); err != nil {
			return err
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	checkRoute := func(wantAlice, wantBob int) {
		t.Helper()
		waitVMessConnections(t, inst)
		aliceTargets := aliceNode.snapshot()
		bobTargets := bobNode.snapshot()
		if len(aliceTargets) != wantAlice || len(bobTargets) != wantBob {
			t.Fatalf("route counts = %d/%d, want %d/%d", len(aliceTargets), len(bobTargets), wantAlice, wantBob)
		}
		for _, target := range append(aliceTargets, bobTargets...) {
			if target != wantBackend {
				t.Fatal("recorder observed an unexpected destination")
			}
		}
	}
	if err = connect(aliceID); err != nil {
		t.Fatalf("alice connection failed: %v", err)
	}
	checkRoute(1, 0)
	if err = connect(bobID); err != nil {
		t.Fatalf("bob connection failed: %v", err)
	}
	checkRoute(1, 1)
	if err = connect(newUUID()); err == nil {
		t.Fatal("wrong UUID accepted")
	}
	waitVMessConnections(t, inst)
	checkRoute(1, 1)
	rotatedID := newUUID()
	rotated := current.Clone()
	rotated.Users["alice"]["uuid"] = rotatedID
	waitVMessConnections(t, inst)
	if err = inst.Reconfigure(options(rotated, nil, nil)); err != nil {
		t.Fatal("rotation failed")
	}
	current = rotated
	if err = connect(aliceID); err == nil {
		t.Fatal("old Alice UUID remained accepted")
	}
	checkRoute(1, 1)
	if err = connect(rotatedID); err != nil {
		t.Fatalf("rotated Alice UUID failed: %v", err)
	}
	checkRoute(2, 1)
	if err = connect(bobID); err != nil {
		t.Fatalf("Bob UUID changed during rotation: %v", err)
	}
	checkRoute(2, 2)
	globalDenied := options(current, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	waitVMessConnections(t, inst)
	if err = inst.Reconfigure(globalDenied); err != nil {
		t.Fatal("global filter reconfigure failed")
	}
	globalDropBaseline := droppedVMess(inst)
	if err = connect(rotatedID); err == nil {
		t.Fatal("global filter allowed loopback")
	}
	waitVMessConnections(t, inst)
	checkRoute(2, 2)
	globalDrops := droppedVMess(inst)
	if globalDrops <= globalDropBaseline {
		t.Fatal("global filter denial was not counted")
	}
	accountDenied := options(current, nil, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	waitVMessConnections(t, inst)
	if err = inst.Reconfigure(accountDenied); err != nil {
		t.Fatal("account filter reconfigure failed")
	}
	accountDropBaseline := droppedVMess(inst)
	if err = connect(rotatedID); err == nil {
		t.Fatal("Alice account filter allowed loopback")
	}
	waitVMessConnections(t, inst)
	checkRoute(2, 2)
	accountDrops := droppedVMess(inst)
	if accountDrops <= accountDropBaseline {
		t.Fatal("account filter denial was not counted")
	}
	if err = connect(bobID); err != nil {
		t.Fatalf("Alice filter denied Bob: %v", err)
	}
	checkRoute(2, 3)
	waitVMessConnections(t, inst)
	if err = inst.Close(); err != nil {
		t.Fatal("core close failed")
	}
	closed = true
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("listener port remained bound after Close")
	}
	_ = listener.Close()
}
func droppedVMess(inst *core.Instance) uint64 {
	for _, info := range inst.Servers() {
		if info.Name == "entry" {
			return info.DroppedClients
		}
	}
	return 0
}
func startVMessEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	t.Cleanup(func() { _ = l.Close(); <-acceptDone; workers.Wait() })
	return l.Addr().String()
}
