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
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

const trusttunnelTestTimeout = 10 * time.Second

func TestTrustTunnelRoutesAuthenticationPolicyRotationAndClose(t *testing.T) {
	aliceNode, aliceAddress := trusttunnelStartRecorder(t)
	bobNode, bobAddress := trusttunnelStartRecorder(t)
	backend := trusttunnelEchoServer(t)
	host, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	protocol, ok := servers.Lookup("trusttunnel")
	if !ok {
		t.Fatal("TrustTunnel protocol is not registered")
	}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal("TLS material generation failed")
	}
	aliceSecret := trusttunnelSecret(t, protocol)
	bobSecret := trusttunnelSecret(t, protocol)
	home := t.TempDir()
	var instance *core.Instance
	port := servertest.StartOnFreePort(t, func(candidate int) error {
		started, startErr := core.Start(context.Background(), trusttunnelOptions(candidate, home, aliceSecret, bobSecret, material, aliceAddress, bobAddress, nil, nil))
		instance = started
		return startErr
	})
	closed := false
	defer func() {
		if !closed {
			defer func() {
				if err := instance.Close(); err != nil {
					t.Error("core close failed")
				}
			}()
			trusttunnelWaitConnections(t, instance)
		}
	}()
	servertest.TrustForTest(t, material)
	for _, account := range []struct {
		name, password string
		node           *trusttunnelRecorder
	}{{"alice", aliceSecret, aliceNode}, {"bob", bobSecret, bobNode}} {
		if err := trusttunnelRoundTrip(t, port, account.name, account.password, material["fingerprint"], host, backendPort); err != nil {
			t.Fatalf("%s round trip failed", account.name)
		}
		trusttunnelWaitConnections(t, instance)
		if got := account.node.snapshot(); len(got) != 1 || got[0] != backend {
			t.Fatalf("%s route recorder mismatch: %v", account.name, got)
		}
	}
	if len(aliceNode.snapshot()) != 1 || len(bobNode.snapshot()) != 1 {
		t.Fatal("account traffic used the wrong route")
	}
	if err := trusttunnelRoundTrip(t, port, "alice", "wrong-password", material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("wrong password was accepted")
	}
	trusttunnelWaitConnections(t, instance)
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := instance.Reconfigure(trusttunnelOptions(port, home, aliceSecret, bobSecret, material, aliceAddress, bobAddress, blocked, nil)); err != nil {
		t.Fatal("global policy reload failed")
	}
	if err := trusttunnelRoundTrip(t, port, "alice", aliceSecret, material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("globally denied client succeeded")
	}
	if len(instance.Servers()) != 1 || instance.Servers()[0].DroppedClients == 0 {
		t.Fatal("global denial was not counted")
	}
	if err := instance.Reconfigure(trusttunnelOptions(port, home, aliceSecret, bobSecret, material, aliceAddress, bobAddress, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("account policy reload failed")
	}
	if err := trusttunnelRoundTrip(t, port, "alice", aliceSecret, material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("account-denied client succeeded")
	}
	if err := trusttunnelRoundTrip(t, port, "bob", bobSecret, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("Alice policy blocked Bob")
	}
	trusttunnelWaitConnections(t, instance)
	rotated := trusttunnelSecret(t, protocol)
	if err := instance.Reconfigure(trusttunnelOptions(port, home, rotated, bobSecret, material, aliceAddress, bobAddress, nil, nil)); err != nil {
		t.Fatal("secret rotation failed")
	}
	if err := trusttunnelRoundTrip(t, port, "alice", rotated, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("rotated password failed")
	}
	if err := trusttunnelRoundTrip(t, port, "alice", aliceSecret, material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("old password remains accepted")
	}
	trusttunnelWaitConnections(t, instance)
	if err := instance.Close(); err != nil {
		t.Fatal("core close failed")
	}
	closed = true
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("Close did not release the port")
	}
	_ = listener.Close()
}

func trusttunnelSecret(t *testing.T, protocol servers.Protocol) string {
	t.Helper()
	secret, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	return secret["password"]
}

func trusttunnelOptions(port int, home string, alicePassword, bobPassword string, material map[string]string, aliceAddress, bobAddress string, global []netip.Prefix, perAccount map[string][]netip.Prefix) core.Options {
	return core.Options{
		HomeDir: home, Authenticator: auth.NewAuthenticator(nil),
		RouteNodes:     map[string][]map[string]any{"alice": {trusttunnelNodeMap(aliceAddress)}, "bob": {trusttunnelNodeMap(bobAddress)}},
		AccountRoute:   map[string]string{"alice": "alice", "bob": "bob"},
		AllowedClients: global, AccountAllowedClients: perAccount,
		Servers: []servers.Spec{{Name: "entry", Protocol: "trusttunnel", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice", "bob"}, Params: map[string]string{}, Material: servers.SecretMap(material), Users: servers.UserSecrets{"alice": {"password": alicePassword}, "bob": {"password": bobPassword}}}},
	}
}

func trusttunnelNodeMap(address string) map[string]any {
	_, text, err := net.SplitHostPort(address)
	if err != nil {
		panic("invalid recorder address")
	}
	port, err := strconv.Atoi(text)
	if err != nil {
		panic("invalid recorder port")
	}
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": port}
}

func trusttunnelRoundTrip(t *testing.T, port int, username, password, fingerprint, host string, backendPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), trusttunnelTestTimeout)
	defer cancel()
	client, err := outbound.NewTrustTunnel(outbound.TrustTunnelOption{Name: "trusttunnel-test", Server: "127.0.0.1", Port: port, UserName: username, Password: password, SNI: "localhost", Fingerprint: fingerprint, ALPN: []string{"h2"}})
	if err != nil {
		return err
	}
	defer client.Close()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(trusttunnelTestTimeout)); err != nil {
		return err
	}
	payload := []byte("trusttunnel-echo")
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

func trusttunnelWaitConnections(t *testing.T, instance *core.Instance) {
	t.Helper()
	timer := time.NewTimer(trusttunnelTestTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		connections := 0
		for _, info := range instance.Servers() {
			connections += info.Connections
		}
		if connections == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("TrustTunnel connections did not drain")
		}
	}
}

func trusttunnelEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("echo listener failed")
	}
	done := make(chan struct{})
	var handlers sync.WaitGroup
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func(conn net.Conn) {
				defer handlers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(trusttunnelTestTimeout))
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		wait := make(chan struct{})
		go func() { handlers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(trusttunnelTestTimeout):
			t.Error("echo handlers did not stop")
		}
	})
	return listener.Addr().String()
}

type trusttunnelRecorder struct {
	mu       sync.Mutex
	targets  []string
	handlers sync.WaitGroup
}

func trusttunnelStartRecorder(t *testing.T) (*trusttunnelRecorder, string) {
	t.Helper()
	recorder := &trusttunnelRecorder{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listener failed")
	}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			recorder.handlers.Add(1)
			go func(conn net.Conn) { defer recorder.handlers.Done(); recorder.serve(conn) }(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-acceptDone:
		case <-time.After(trusttunnelTestTimeout):
			t.Error("recorder accept loop did not stop")
		}
		wait := make(chan struct{})
		go func() { recorder.handlers.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(trusttunnelTestTimeout):
			t.Error("recorder handlers did not stop")
		}
	})
	return recorder, listener.Addr().String()
}

func (r *trusttunnelRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}

func (r *trusttunnelRecorder) serve(conn net.Conn) {
	defer conn.Close()
	if conn.SetDeadline(time.Now().Add(trusttunnelTestTimeout)) != nil {
		return
	}
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(conn, length); err != nil {
			return
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		host = string(address)
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		host = net.IP(address).String()
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return
	}
	destination := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes))))
	r.mu.Lock()
	r.targets = append(r.targets, destination)
	r.mu.Unlock()
	upstream, err := net.DialTimeout("tcp", destination, trusttunnelTestTimeout)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if upstream.SetDeadline(time.Now().Add(trusttunnelTestTimeout)) != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	select {
	case <-done:
	case <-time.After(trusttunnelTestTimeout):
	}
	_ = conn.Close()
	_ = upstream.Close()
	select {
	case <-done:
	case <-time.After(trusttunnelTestTimeout):
	}
}
