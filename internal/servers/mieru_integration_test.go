//go:build integration

package servers_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

const mieruTimeout = 8 * time.Second

func TestMieruTCPRoutesRotationFiltersAndClose(t *testing.T) {
	protocol, ok := servers.Lookup("mieru")
	if !ok {
		t.Fatal("mieru protocol is not registered")
	}
	aliceRecorder, aliceAddress := mieruStartRecorder(t)
	bobRecorder, bobAddress := mieruStartRecorder(t)
	echoAddress := mieruStartEcho(t)
	echoHost, echoPortText, err := net.SplitHostPort(echoAddress)
	if err != nil {
		t.Fatal("invalid echo endpoint")
	}
	echoPort, err := strconv.Atoi(echoPortText)
	if err != nil {
		t.Fatal("invalid echo port")
	}
	newPassword := func() string {
		secret, err := protocol.NewSecret("alice", nil)
		if err != nil {
			t.Fatal("secret generation failed")
		}
		return secret["password"]
	}
	alicePassword, bobPassword := newPassword(), newPassword()
	port := 0
	makeSpec := func(alice string) servers.Spec {
		return servers.Spec{
			Name: "entry", Protocol: "mieru", Bind: "127.0.0.1", Port: port,
			Accounts: []string{"alice", "bob"},
			Params:   map[string]string{"transport": "TCP", "multiplexing": "MULTIPLEXING_OFF"},
			Users: servers.UserSecrets{
				"alice": {"password": alice},
				"bob":   {"password": bobPassword},
			},
		}
	}
	home := testutil.TempHome(t)
	options := func(spec servers.Spec, global, aliceAllowed []netip.Prefix) core.Options {
		return core.Options{
			HomeDir: home, Servers: []servers.Spec{spec},
			RouteNodes: map[string][]map[string]any{
				"alice": {mieruNode(aliceAddress)},
				"bob":   {mieruNode(bobAddress)},
			},
			AccountRoute:          map[string]string{"alice": "alice", "bob": "bob"},
			AllowedClients:        global,
			AccountAllowedClients: map[string][]netip.Prefix{"alice": aliceAllowed},
		}
	}
	var inst *core.Instance
	port = servertest.StartOnFreePort(t, func(candidate int) error {
		port = candidate
		var startErr error
		inst, startErr = core.Start(context.Background(), options(makeSpec(alicePassword), nil, nil))
		var coreErr *cli.Error
		if errors.As(startErr, &coreErr) && coreErr.Code == "PORT_IN_USE" {
			return syscall.EADDRINUSE
		}
		return startErr
	})
	current := makeSpec(alicePassword)
	closed := false
	defer func() {
		if !closed {
			defer inst.Close()
			mieruWaitConnections(t, inst)
		}
	}()
	connect := func(user, password string) error {
		ctx, cancel := context.WithTimeout(context.Background(), mieruTimeout)
		defer cancel()
		client, err := outbound.NewMieru(outbound.MieruOption{
			Name: "mieru-test", Server: "127.0.0.1", Port: port, Transport: "TCP",
			UserName: user, Password: password, Multiplexing: "MULTIPLEXING_OFF",
		})
		if err != nil {
			return err
		}
		defer client.Close()
		conn, err := client.DialContext(ctx, &C.Metadata{
			NetWork: C.TCP, DstIP: netip.MustParseAddr(echoHost), DstPort: uint16(echoPort),
		})
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(mieruTimeout)); err != nil {
			return err
		}
		payload := []byte("mieru-route-check")
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			return err
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	checkRoutes := func(wantAlice, wantBob int) {
		t.Helper()
		mieruWaitConnections(t, inst)
		aliceTargets := aliceRecorder.snapshot()
		bobTargets := bobRecorder.snapshot()
		if len(aliceTargets) != wantAlice || len(bobTargets) != wantBob {
			t.Fatalf("route counts = %d/%d, want %d/%d", len(aliceTargets), len(bobTargets), wantAlice, wantBob)
		}
		wantTarget := net.JoinHostPort(echoHost, echoPortText)
		for _, target := range append(aliceTargets, bobTargets...) {
			if target != wantTarget {
				t.Fatalf("recorder observed target %q, want %q", target, wantTarget)
			}
		}
	}
	if err := connect("alice", alicePassword); err != nil {
		t.Fatalf("Alice connection failed: %v", err)
	}
	checkRoutes(1, 0)
	if err := connect("bob", bobPassword); err != nil {
		t.Fatalf("Bob connection failed: %v", err)
	}
	checkRoutes(1, 1)
	if err := connect("alice", newPassword()); err == nil {
		t.Fatal("wrong password accepted")
	}
	checkRoutes(1, 1)

	rotatedPassword := newPassword()
	rotatedSpec := current.Clone()
	rotatedSpec.Users["alice"]["password"] = rotatedPassword
	mieruWaitConnections(t, inst)
	if err := inst.Reconfigure(options(rotatedSpec, nil, nil)); err != nil {
		t.Fatal("rotation reconfigure failed")
	}
	current = rotatedSpec
	if err := connect("alice", alicePassword); err == nil {
		t.Fatal("old password accepted after rotation")
	}
	checkRoutes(1, 1)
	if err := connect("alice", rotatedPassword); err != nil {
		t.Fatalf("rotated password failed: %v", err)
	}
	if err := connect("bob", bobPassword); err != nil {
		t.Fatalf("Bob changed during rotation: %v", err)
	}
	checkRoutes(2, 2)

	globalDenied := options(current, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	mieruWaitConnections(t, inst)
	if err := inst.Reconfigure(globalDenied); err != nil {
		t.Fatal("global filter reconfigure failed")
	}
	baseline := mieruDrops(inst)
	if err := connect("alice", rotatedPassword); err == nil {
		t.Fatal("global filter allowed loopback client")
	}
	checkRoutes(2, 2)
	if mieruDrops(inst) <= baseline {
		t.Fatal("global filter denial was not counted")
	}

	accountDenied := options(current, nil, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	mieruWaitConnections(t, inst)
	if err := inst.Reconfigure(accountDenied); err != nil {
		t.Fatal("account filter reconfigure failed")
	}
	baseline = mieruDrops(inst)
	if err := connect("alice", rotatedPassword); err == nil {
		t.Fatal("Alice account filter allowed loopback client")
	}
	checkRoutes(2, 2)
	if mieruDrops(inst) <= baseline {
		t.Fatal("account filter denial was not counted")
	}
	if err := connect("bob", bobPassword); err != nil {
		t.Fatalf("Alice filter denied Bob: %v", err)
	}
	checkRoutes(2, 3)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not reserve conflict port")
	}
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	defer occupied.Close()
	conflictingSpec := current.Clone()
	conflictingSpec.Port = occupiedPort
	mieruWaitConnections(t, inst)
	if err := inst.Reconfigure(options(conflictingSpec, nil, nil)); err == nil {
		t.Fatal("occupied replacement port unexpectedly succeeded")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal("could not release conflict port")
	}
	if err := connect("bob", bobPassword); err != nil {
		t.Fatalf("original listener did not survive failed stage: %v", err)
	}
	checkRoutes(2, 4)

	mieruWaitConnections(t, inst)
	if err := inst.Close(); err != nil {
		t.Fatal("core close failed")
	}
	closed = true
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("listener port remained bound after Close")
	}
	_ = listener.Close()
}

func mieruNode(address string) map[string]any {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		panic("invalid recorder address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic("invalid recorder port")
	}
	return map[string]any{"name": "mieru-recorder", "type": "socks5", "server": host, "port": port, "udp": false}
}

type mieruRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func (r *mieruRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}

func mieruStartRecorder(t *testing.T) (*mieruRecorder, string) {
	t.Helper()
	recorder := &mieruRecorder{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listen failed")
	}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			recorder.workers.Add(1)
			go func() {
				defer recorder.workers.Done()
				mieruServeRecorderConn(recorder, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		recorder.workers.Wait()
	})
	return recorder, listener.Addr().String()
}

func mieruServeRecorderConn(recorder *mieruRecorder, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(mieruTimeout))
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 5 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil || request[1] != 1 {
		return
	}
	var targetHost string
	switch request[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		targetHost = net.IP(address).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		address := make([]byte, int(size[0]))
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		targetHost = string(address)
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		targetHost = net.IP(address).String()
	default:
		return
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return
	}
	target := net.JoinHostPort(targetHost, strconv.Itoa(int(portBytes[0])<<8|int(portBytes[1])))
	recorder.mu.Lock()
	recorder.targets = append(recorder.targets, target)
	recorder.mu.Unlock()
	upstream, err := net.DialTimeout("tcp", target, mieruTimeout)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	_ = upstream.SetDeadline(time.Now().Add(mieruTimeout))
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		_ = upstream.Close()
		return
	}
	finished := make(chan struct{}, 2)
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(upstream, conn)
		finished <- struct{}{}
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(conn, upstream)
		finished <- struct{}{}
	}()
	select {
	case <-finished:
	case <-time.After(mieruTimeout):
	}
	_ = conn.Close()
	_ = upstream.Close()
	copies.Wait()
}

func mieruStartEcho(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("echo listen failed")
	}
	acceptDone := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(mieruTimeout))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		workers.Wait()
	})
	return listener.Addr().String()
}

func mieruWaitConnections(t *testing.T, inst *core.Instance) {
	t.Helper()
	deadline := time.NewTimer(mieruTimeout)
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
			t.Fatalf("Mieru connections did not drain: %d", active)
		}
	}
}

func mieruDrops(inst *core.Instance) uint64 {
	for _, info := range inst.Servers() {
		if info.Name == "entry" {
			return info.DroppedClients
		}
	}
	return 0
}
