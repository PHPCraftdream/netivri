//go:build integration

package servers_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	mihomoAuth "github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

func TestSudokuCoreRoutePolicyRotationAndClose(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			c, e := backend.Accept()
			if e != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	backendHost, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	backendPort, _ := strconv.Atoi(backendPortText)
	recorder, recorderAddress := sudokuStartRecorder(t)
	_, nodePortText, _ := net.SplitHostPort(recorderAddress)
	nodePort, _ := strconv.Atoi(nodePortText)
	protocol, ok := servers.Lookup("sudoku")
	if !ok {
		t.Fatal("Sudoku protocol missing")
	}
	newKey := func() string {
		s, e := protocol.NewSecret("alice", nil)
		if e != nil {
			t.Fatal(e)
		}
		return s["key"]
	}
	key, port := newKey(), 0
	home := testutil.TempHome(t)
	options := func(policy []netip.Prefix) core.Options {
		return core.Options{
			HomeDir:       home,
			Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{{User: "alice", Pass: "integration-password"}}),
			RouteNodes: map[string][]map[string]any{
				"alice": {map[string]any{"type": "socks5", "server": "127.0.0.1", "port": nodePort}},
			},
			AccountRoute: map[string]string{"alice": "alice"},
			Servers: []servers.Spec{{
				Name: "sudoku-entry", Protocol: "sudoku", Bind: "127.0.0.1", Port: port,
				Accounts: []string{"alice"}, Params: map[string]string{},
				Users: servers.UserSecrets{"alice": map[string]string{"key": key}},
			}},
			AccountAllowedClients: map[string][]netip.Prefix{"alice": policy},
		}
	}
	var inst *core.Instance
	port = servertest.StartOnFreePort(t, func(p int) error {
		port = p
		var e error
		inst, e = core.Start(context.Background(), options(nil))
		return e
	})
	closed := false
	defer func() {
		if !closed && inst != nil {
			defer inst.Close()
			sudokuWaitConnections(t, inst)
		}
	}()
	dial := func(k string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client, e := outbound.NewSudoku(outbound.SudokuOption{
			Name:         "sudoku-test",
			Server:       "127.0.0.1",
			Port:         port,
			Key:          k,
			HTTPMask:     sudokuBoolPointer(true),
			HTTPMaskMode: "legacy",
		})
		if e != nil {
			return e
		}
		defer client.Close()
		conn, e := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(backendHost), DstPort: uint16(backendPort)})
		if e != nil {
			return e
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		payload := []byte("sudoku-routed-echo")
		if _, e = conn.Write(payload); e != nil {
			return e
		}
		got := make([]byte, len(payload))
		if _, e = io.ReadFull(conn, got); e != nil {
			return e
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if err := dial(key); err != nil {
		t.Fatalf("route echo: %v", err)
	}
	if got := recorder.snapshot(); len(got) != 1 || got[0] != backend.Addr().String() {
		t.Fatalf("route recorder: %v", got)
	}
	if err := dial(newKey()); err == nil {
		t.Fatal("wrong key succeeded")
	}
	sudokuWaitConnections(t, inst)
	if err := inst.Reconfigure(options([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})); err != nil {
		t.Fatal(err)
	}
	if err := dial(key); err == nil {
		t.Fatal("disallowed client succeeded")
	}
	if inst.Servers()[0].DroppedClients == 0 {
		t.Fatal("account denial was not counted")
	}
	if err := inst.Reconfigure(options(nil)); err != nil {
		t.Fatal(err)
	}
	old := key
	key = newKey()
	if err := inst.Reconfigure(options(nil)); err != nil {
		t.Fatal(err)
	}
	if err := dial(key); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if err := dial(old); err == nil {
		t.Fatal("old key succeeded after rotation")
	}
	sudokuWaitConnections(t, inst)
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port not released: %v", err)
	}
	_ = ln.Close()
}

type sudokuRecorder struct{ targets chan string }

func sudokuBoolPointer(value bool) *bool { return &value }

func sudokuStartRecorder(t *testing.T) (*sudokuRecorder, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &sudokuRecorder{targets: make(chan string, 10)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go r.serve(c)
		}
	}()
	return r, ln.Addr().String()
}
func (r *sudokuRecorder) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	h := make([]byte, 2)
	if _, e := io.ReadFull(c, h); e != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(c, methods); e != nil {
		return
	}
	if _, e := c.Write([]byte{5, 0}); e != nil {
		return
	}
	req := make([]byte, 4)
	if _, e := io.ReadFull(c, req); e != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, e := io.ReadFull(c, b); e != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n := make([]byte, 1)
		if _, e := io.ReadFull(c, n); e != nil {
			return
		}
		b := make([]byte, int(n[0]))
		if _, e := io.ReadFull(c, b); e != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, e := io.ReadFull(c, b); e != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	p := make([]byte, 2)
	if _, e := io.ReadFull(c, p); e != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p))))
	select {
	case r.targets <- target:
	default:
	}
	up, e := net.DialTimeout("tcp", target, 5*time.Second)
	if e != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, c)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, up)
		done <- struct{}{}
	}()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}
func (r *sudokuRecorder) snapshot() []string {
	var out []string
	for {
		select {
		case target := <-r.targets:
			out = append(out, target)
		default:
			return out
		}
	}
}

func sudokuWaitConnections(t *testing.T, inst *core.Instance) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		count := 0
		for _, server := range inst.Servers() {
			count += server.Connections
		}
		if count == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Error("Sudoku server connections did not drain")
			return
		}
	}
}
