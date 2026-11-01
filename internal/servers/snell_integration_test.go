//go:build integration

package servers_test

import (
	"bufio"
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
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

const snellIntegrationTimeout = 5 * time.Second

type snellSOCKSRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func (r *snellSOCKSRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}
func snellRecorderStart(t *testing.T) (*snellSOCKSRecorder, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &snellSOCKSRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			r.workers.Add(1)
			go func() { defer r.workers.Done(); snellRecorderServe(r, c) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(snellIntegrationTimeout):
			t.Error("recorder accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { r.workers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(snellIntegrationTimeout):
			t.Error("recorder handlers did not stop")
		}
	})
	return r, ln.Addr().String()
}
func snellRecorderServe(r *snellSOCKSRecorder, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(snellIntegrationTimeout))
	br := bufio.NewReader(c)
	h := make([]byte, 2)
	if _, e := io.ReadFull(br, h); e != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(br, methods); e != nil {
		return
	}
	if _, e := c.Write([]byte{5, 0}); e != nil {
		return
	}
	req := make([]byte, 4)
	if _, e := io.ReadFull(br, req); e != nil || req[1] != 1 {
		return
	}
	host := ""
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, e := io.ReadFull(br, b); e != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		b := make([]byte, 1)
		if _, e := io.ReadFull(br, b); e != nil {
			return
		}
		n := make([]byte, int(b[0]))
		if _, e := io.ReadFull(br, n); e != nil {
			return
		}
		host = string(n)
	case 4:
		b := make([]byte, 16)
		if _, e := io.ReadFull(br, b); e != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p := make([]byte, 2)
	if _, e := io.ReadFull(br, p); e != nil {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	r.mu.Lock()
	r.targets = append(r.targets, dest)
	r.mu.Unlock()
	up, e := (&net.Dialer{Timeout: snellIntegrationTimeout}).Dial("tcp", dest)
	if e != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(snellIntegrationTimeout))
	if _, e = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	select {
	case <-done:
	case <-time.After(snellIntegrationTimeout):
	}
	_ = c.Close()
	_ = up.Close()
	select {
	case <-done:
	case <-time.After(snellIntegrationTimeout):
	}
}
func snellIntegrationOptions(home string, s servers.Spec, recorder string, g, a []netip.Prefix) core.Options {
	host, p, _ := net.SplitHostPort(recorder)
	n, _ := strconv.Atoi(p)
	return core.Options{HomeDir: home, RouteNodes: map[string][]map[string]any{"alice": {{"name": "snell-node", "type": "socks5", "server": host, "port": n, "udp": false}}}, AccountRoute: map[string]string{"alice": "alice"}, Servers: []servers.Spec{s}, AllowedClients: g, AccountAllowedClients: map[string][]netip.Prefix{"alice": a}}
}
func snellIntegrationWait(t *testing.T, i *core.Instance) {
	t.Helper()
	timer := time.NewTimer(snellIntegrationTimeout)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		n := 0
		for _, v := range i.Servers() {
			n += v.Connections
		}
		if n == 0 {
			return
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatalf("server connections did not drain: %d", n)
		}
	}
}
func TestSnellIntegrationRoutePoliciesRotation(t *testing.T) {
	p, ok := servers.Lookup("snell")
	if !ok {
		t.Fatal("Snell not registered")
	}
	rec, recAddr := snellRecorderStart(t)
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	echoDone := make(chan struct{})
	var ew sync.WaitGroup
	go func() {
		defer close(echoDone)
		for {
			c, x := echo.Accept()
			if x != nil {
				return
			}
			ew.Add(1)
			go func() {
				defer ew.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(snellIntegrationTimeout))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = echo.Close()
		select {
		case <-echoDone:
		case <-time.After(snellIntegrationTimeout):
			t.Error("echo accept loop did not stop")
		}
		ch := make(chan struct{})
		go func() { ew.Wait(); close(ch) }()
		select {
		case <-ch:
		case <-time.After(snellIntegrationTimeout):
			t.Error("echo handlers did not stop")
		}
	})
	hostText, portText, _ := net.SplitHostPort(echo.Addr().String())
	dst := net.JoinHostPort(hostText, portText)
	ip := netip.MustParseAddr(hostText)
	dstPort, _ := strconv.Atoi(portText)
	key := func() string {
		v, x := p.NewSecret("alice", nil)
		if x != nil {
			t.Fatal("PSK generation failed")
		}
		return v["psk"]
	}
	home := testutil.TempHome(t)
	port := 0
	makeSpec := func(k, v, o, h string) servers.Spec {
		params := map[string]string{}
		if v != "" {
			params["version"] = v
		}
		if o != "" {
			params["obfs"] = o
		}
		if h != "" {
			params["obfs_host"] = h
		}
		return servers.Spec{Name: "snell-entry", Protocol: "snell", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: params, Users: servers.UserSecrets{"alice": {"psk": k}}}
	}
	spec := makeSpec(key(), "2", "", "")
	options := func(s servers.Spec, g, a []netip.Prefix) core.Options {
		return snellIntegrationOptions(home, s, recAddr, g, a)
	}
	var inst *core.Instance
	port = servertest.StartOnFreePort(t, func(candidate int) error {
		port = candidate
		spec.Port = candidate
		var x error
		inst, x = core.Start(context.Background(), options(spec, nil, nil))
		return x
	})
	closed := false
	drain := func() { snellIntegrationWait(t, inst) }
	t.Cleanup(func() {
		if !closed {
			defer func() { _ = inst.Close() }()
			drain()
		}
	})
	dial := func(k, v, o, h string) error {
		ctx, cancel := context.WithTimeout(context.Background(), snellIntegrationTimeout)
		defer cancel()
		ver := 2
		if v == "3" {
			ver = 3
		}
		client, x := outbound.NewSnell(outbound.SnellOption{Name: "snell-client", Server: "127.0.0.1", Port: port, Psk: k, Version: ver, ObfsOpts: map[string]any{"mode": o, "host": h}})
		if x != nil {
			return x
		}
		defer client.Close()
		raw, x := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if x != nil {
			return x
		}
		defer raw.Close()
		if x = raw.SetDeadline(time.Now().Add(snellIntegrationTimeout)); x != nil {
			return x
		}
		conn, x := client.StreamConnContext(ctx, raw, &C.Metadata{NetWork: C.TCP, DstIP: ip, DstPort: uint16(dstPort)})
		if x != nil {
			return x
		}
		payload := []byte("snell-echo")
		if _, x = conn.Write(payload); x != nil {
			return x
		}
		got := make([]byte, len(payload))
		if _, x = io.ReadFull(conn, got); x != nil {
			return x
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if e = dial(spec.Users["alice"]["psk"], "2", "", ""); e != nil {
		t.Fatalf("echo failed: %v", e)
	}
	drain()
	targets := rec.snapshot()
	if len(targets) != 1 || targets[0] != dst {
		t.Fatal("route recorder destination mismatch")
	}
	for _, tc := range []struct{ v, o, h string }{{"2", "", ""}, {"2", "http", "proxy.test"}, {"2", "tls", "proxy.test"}, {"3", "", ""}, {"3", "http", "proxy.test"}, {"3", "tls", "proxy.test"}} {
		next := makeSpec(key(), tc.v, tc.o, tc.h)
		if e = inst.Reconfigure(options(next, nil, nil)); e != nil {
			t.Fatal("reconfigure failed")
		}
		if e = dial(next.Users["alice"]["psk"], tc.v, tc.o, tc.h); e != nil {
			t.Fatalf("version/obfs case failed: %v", e)
		}
		drain()
	}
	current := makeSpec(spec.Users["alice"]["psk"], "2", "", "")
	if e = inst.Reconfigure(options(current, nil, nil)); e != nil {
		t.Fatal(e)
	}
	if e = dial(key(), "2", "", ""); e == nil {
		t.Fatal("wrong PSK accepted")
	}
	drain()
	for _, pol := range []struct{ g, a []netip.Prefix }{{[]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil}, {nil, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}} {
		if e = inst.Reconfigure(options(current, pol.g, pol.a)); e != nil {
			t.Fatal(e)
		}
		if e = dial(current.Users["alice"]["psk"], "2", "", ""); e == nil {
			t.Fatal("disallowed client accepted")
		}
		drain()
		if inst.Servers()[0].DroppedClients == 0 {
			t.Fatal("drop not counted")
		}
	}
	rotated := makeSpec(key(), "2", "", "")
	if e = inst.Reconfigure(options(rotated, nil, nil)); e != nil {
		t.Fatal(e)
	}
	if e = dial(rotated.Users["alice"]["psk"], "2", "", ""); e != nil {
		t.Fatalf("rotated PSK rejected: %v", e)
	}
	drain()
	if e = dial(current.Users["alice"]["psk"], "2", "", ""); e == nil {
		t.Fatal("old PSK accepted")
	}
	drain()
	if e = inst.Close(); e != nil {
		t.Fatal(e)
	}
	closed = true
	ln, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		t.Fatalf("Close did not free port: %v", e)
	}
	_ = ln.Close()
}
