//go:build integration

package servers_test

import (
	"context"
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
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

const shadowquicTestTimeout = 10 * time.Second

func TestShadowquicCoreRoutesPolicyRotationAndClose(t *testing.T) {
	protocol, ok := servers.Lookup("shadowquic")
	if !ok {
		t.Fatal("ShadowQUIC protocol is not registered")
	}
	params := map[string]string{"sni": "localhost"}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	secret := func() string {
		value, err := protocol.NewSecret("account", nil)
		if err != nil {
			t.Fatal(err)
		}
		return value["password"]
	}
	alicePassword, bobPassword := secret(), secret()
	aliceAddress, aliceRecorder := shadowquicStartRecorder(t)
	bobAddress, bobRecorder := shadowquicStartRecorder(t)
	backend := shadowquicStartEcho(t)
	backendHost, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	home := testutil.TempHome(t)
	options := func(port int, alice, bob string, allowed []netip.Prefix) core.Options {
		return core.Options{
			HomeDir: home, Authenticator: auth.NewAuthenticator(nil),
			RouteNodes:   map[string][]map[string]any{"alice": {shadowquicNodeMap(aliceAddress)}, "bob": {shadowquicNodeMap(bobAddress)}},
			AccountRoute: map[string]string{"alice": "alice", "bob": "bob"}, AllowedClients: allowed,
			Servers: []servers.Spec{{Name: "shadowquic-entry", Protocol: "shadowquic", Bind: "127.0.0.1", Port: port,
				Accounts: []string{"alice", "bob"}, Params: params, Material: servers.SecretMap(material),
				Users: servers.UserSecrets{"alice": {"password": alice}, "bob": {"password": bob}}}},
		}
	}
	var instance *core.Instance
	port := servertest.StartOnFreePort(t, func(candidate int) error {
		started, startErr := core.Start(context.Background(), options(candidate, alicePassword, bobPassword, nil))
		if startErr == nil {
			instance = started
		}
		return startErr
	})
	closed := false
	t.Cleanup(func() {
		if instance != nil && !closed {
			defer func() {
				if err := instance.Close(); err != nil {
					t.Errorf("close core: %v", err)
				}
			}()
			shadowquicDrain(t, instance)
		}
	})
	servertest.TrustForTest(t, material)
	wantTarget := net.JoinHostPort(backendHost, backendPortText)

	if err := shadowquicRoundtrip(t, port, "alice", alicePassword, backendHost, backendPort); err != nil {
		t.Fatal("Alice route:", err)
	}
	shadowquicDrain(t, instance)
	shadowquicAssertTargets(t, aliceRecorder, wantTarget, 1)
	shadowquicAssertTargets(t, bobRecorder, wantTarget, 0)
	if err := shadowquicRoundtrip(t, port, "bob", bobPassword, backendHost, backendPort); err != nil {
		t.Fatal("Bob route:", err)
	}
	shadowquicDrain(t, instance)
	shadowquicAssertTargets(t, aliceRecorder, wantTarget, 1)
	shadowquicAssertTargets(t, bobRecorder, wantTarget, 1)

	wrongPassword := secret()
	if err := shadowquicRoundtrip(t, port, "alice", wrongPassword, backendHost, backendPort); err == nil {
		t.Fatal("wrong password was accepted")
	}
	shadowquicDrain(t, instance)
	shadowquicAssertTargets(t, aliceRecorder, wantTarget, 1)
	shadowquicAssertTargets(t, bobRecorder, wantTarget, 1)

	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := instance.Reconfigure(options(port, alicePassword, bobPassword, blocked)); err != nil {
		t.Fatal("allowed-clients reconfigure:", err)
	}
	beforeDrops := shadowquicDropped(instance)
	if err := shadowquicRoundtrip(t, port, "alice", alicePassword, backendHost, backendPort); err == nil {
		t.Fatal("disallowed loopback client was accepted")
	}
	shadowquicDrain(t, instance)
	if shadowquicDropped(instance) <= beforeDrops {
		t.Fatal("disallowed client drop was not counted")
	}
	shadowquicAssertTargets(t, aliceRecorder, wantTarget, 1)
	shadowquicAssertTargets(t, bobRecorder, wantTarget, 1)

	rotated := secret()
	if err := instance.Reconfigure(options(port, rotated, bobPassword, nil)); err != nil {
		t.Fatal("rotate secret:", err)
	}
	if err := shadowquicRoundtrip(t, port, "alice", rotated, backendHost, backendPort); err != nil {
		t.Fatal("fresh rotated client:", err)
	}
	shadowquicDrain(t, instance)
	if err := shadowquicRoundtrip(t, port, "alice", alicePassword, backendHost, backendPort); err == nil {
		t.Fatal("old password was accepted after rotation")
	}
	shadowquicDrain(t, instance)
	shadowquicAssertTargets(t, aliceRecorder, wantTarget, 2)
	shadowquicAssertTargets(t, bobRecorder, wantTarget, 1)

	if err := instance.Close(); err != nil {
		t.Fatal("close core:", err)
	}
	closed = true
	probe, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("Close did not free UDP port:", err)
	}
	_ = probe.Close()
}

func shadowquicRoundtrip(t *testing.T, port int, username, password, host string, destinationPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), shadowquicTestTimeout)
	defer cancel()
	client, err := outbound.NewShadowQuic(outbound.ShadowQuicOption{Name: "shadowquic-test", Server: "127.0.0.1", Port: port, Username: username, Password: password, SNI: "localhost"})
	if err != nil {
		return err
	}
	defer client.Close()
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(destinationPort)}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(shadowquicTestTimeout)); err != nil {
		return err
	}
	payload := []byte("shadowquic-route-echo")
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

func shadowquicNodeMap(address string) map[string]any {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		panic("invalid ShadowQUIC recorder address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic("invalid ShadowQUIC recorder port")
	}
	return map[string]any{"name": "shadowquic-node", "type": "socks5", "server": host, "port": port}
}

func shadowquicDropped(instance *core.Instance) uint64 {
	var count uint64
	for _, status := range instance.Servers() {
		count += status.DroppedClients
	}
	return count
}

func shadowquicDrain(t *testing.T, instance *core.Instance) {
	t.Helper()
	timer := time.NewTimer(shadowquicTestTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		connections := 0
		for _, status := range instance.Servers() {
			connections += status.Connections
		}
		if connections == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("ShadowQUIC connections did not drain: %d", connections)
		}
	}
}

func shadowquicAssertTargets(t *testing.T, recorder *shadowquicRecorder, target string, count int) {
	t.Helper()
	got := recorder.snapshot()
	if len(got) != count {
		t.Fatalf("recorded targets = %v, want %d copies of %q", got, count, target)
	}
	for _, actual := range got {
		if actual != target {
			t.Fatalf("recorded target %q, want %q", actual, target)
		}
	}
}

type shadowquicRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func (r *shadowquicRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}

func shadowquicStartRecorder(t *testing.T) (string, *shadowquicRecorder) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("SOCKS recorder listen:", err)
	}
	r := &shadowquicRecorder{}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			r.workers.Add(1)
			go func() { defer r.workers.Done(); r.serve(conn) }()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-acceptDone:
		case <-time.After(shadowquicTestTimeout):
			t.Errorf("SOCKS recorder accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { r.workers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(shadowquicTestTimeout):
			t.Errorf("SOCKS recorder workers did not stop")
		}
	})
	return listener.Addr().String(), r
}

func (r *shadowquicRecorder) serve(conn net.Conn) {
	defer conn.Close()
	if conn.SetDeadline(time.Now().Add(shadowquicTestTimeout)) != nil {
		return
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 5 || greeting[1] == 0 {
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
	if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	host, err := shadowquicReadSOCKSHost(conn, request[3])
	if err != nil {
		return
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(portBytes[0])<<8|int(portBytes[1])))
	r.mu.Lock()
	r.targets = append(r.targets, target)
	r.mu.Unlock()
	upstream, err := (&net.Dialer{Timeout: shadowquicTestTimeout}).Dial("tcp", target)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if upstream.SetDeadline(time.Now().Add(shadowquicTestTimeout)) != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, conn); finished <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); finished <- struct{}{} }()
	select {
	case <-finished:
	case <-time.After(shadowquicTestTimeout):
	}
	_ = conn.Close()
	_ = upstream.Close()
	select {
	case <-finished:
	case <-time.After(shadowquicTestTimeout):
	}
}

func shadowquicReadSOCKSHost(conn net.Conn, addressType byte) (string, error) {
	switch addressType {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", err
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}
		return string(address), nil
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	default:
		return "", io.ErrUnexpectedEOF
	}
}

func shadowquicStartEcho(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("echo listen:", err)
	}
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
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
				_ = conn.SetDeadline(time.Now().Add(shadowquicTestTimeout))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-acceptDone:
		case <-time.After(shadowquicTestTimeout):
			t.Errorf("echo accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { workers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(shadowquicTestTimeout):
			t.Errorf("echo workers did not stop")
		}
	})
	return listener.Addr().String()
}
