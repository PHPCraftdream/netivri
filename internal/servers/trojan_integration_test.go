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

const trojanTestTimeout = 10 * time.Second

func TestTrojanRoutesAuthenticationPolicyRotationAndClose(t *testing.T) {
	aliceNode, aliceRoute := startTrojanRecorder(t)
	bobNode, bobRoute := startTrojanRecorder(t)
	backend, backendPort := startTrojanEcho(t)
	protocol, ok := servers.Lookup("trojan")
	if !ok {
		t.Fatal("Trojan protocol is not registered")
	}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal("TLS fixture generation failed")
	}
	aliceSecret := trojanSecret(t, protocol)
	bobSecret := trojanSecret(t, protocol)
	var port int
	var instance *core.Instance
	port = servertest.StartOnFreePort(t, func(candidatePort int) error {
		candidate, startErr := core.Start(context.Background(), trojanOptions(t, candidatePort, aliceSecret, bobSecret, material, aliceRoute, bobRoute, nil, nil))
		if startErr != nil {
			return startErr
		}
		instance = candidate
		return nil
	})
	closed := false
	defer func() {
		if !closed {
			defer instance.Close()
			waitTrojanConnections(t, instance)
		}
	}()

	if err := trojanRoundTrip(t, port, aliceSecret, material["fingerprint"], backend, backendPort); err != nil {
		t.Fatal("pinned Alice client failed")
	}
	waitTrojanConnections(t, instance)
	if err := trojanRoundTrip(t, port, aliceSecret, "0000000000000000000000000000000000000000000000000000000000000000", backend, backendPort); err == nil {
		t.Fatal("client with incorrect fingerprint was accepted")
	}
	waitTrojanConnections(t, instance)
	if got := aliceNode.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(backend, strconv.Itoa(backendPort)) {
		t.Fatal("Alice traffic was not routed to Alice's recorder")
	}
	if len(bobNode.snapshot()) != 0 {
		t.Fatal("Alice traffic reached Bob's route")
	}
	if err := trojanRoundTrip(t, port, bobSecret, material["fingerprint"], backend, backendPort); err != nil {
		t.Fatal("Bob client failed")
	}
	waitTrojanConnections(t, instance)
	if got := bobNode.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(backend, strconv.Itoa(backendPort)) {
		t.Fatal("Bob traffic was not routed to Bob's recorder")
	}
	if err := trojanRoundTrip(t, port, "wrong-password", material["fingerprint"], backend, backendPort); err == nil {
		t.Fatal("wrong password was accepted")
	}
	waitTrojanConnections(t, instance)

	servertest.TrustForTest(t, material)
	if err := trojanRoundTrip(t, port, aliceSecret, "", backend, backendPort); err != nil {
		t.Fatal("trusted-CA client failed")
	}
	waitTrojanConnections(t, instance)

	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := instance.Reconfigure(trojanOptions(t, port, aliceSecret, bobSecret, material, aliceRoute, bobRoute, blocked, nil)); err != nil {
		t.Fatal("client policy update failed")
	}
	if err := trojanRoundTrip(t, port, aliceSecret, material["fingerprint"], backend, backendPort); err == nil {
		t.Fatal("disallowed client was accepted")
	}
	waitTrojanConnections(t, instance)
	if len(instance.Servers()) == 0 || instance.Servers()[0].DroppedClients == 0 {
		t.Fatal("disallowed client was not counted")
	}

	if err := instance.Reconfigure(trojanOptions(t, port, aliceSecret, bobSecret, material, aliceRoute, bobRoute, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("account client policy update failed")
	}
	if err := trojanRoundTrip(t, port, aliceSecret, material["fingerprint"], backend, backendPort); err == nil {
		t.Fatal("Alice-specific client restriction was not enforced")
	}
	waitTrojanConnections(t, instance)
	if err := trojanRoundTrip(t, port, bobSecret, material["fingerprint"], backend, backendPort); err != nil {
		t.Fatal("Alice restriction blocked Bob")
	}
	waitTrojanConnections(t, instance)

	rotatedSecret := trojanSecret(t, protocol)
	rotatedMaterial, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal("TLS rotation fixture generation failed")
	}
	if err := instance.Reconfigure(trojanOptions(t, port, rotatedSecret, bobSecret, rotatedMaterial, aliceRoute, bobRoute, nil, nil)); err != nil {
		t.Fatal("credential and material rotation failed")
	}
	if err := trojanRoundTrip(t, port, rotatedSecret, rotatedMaterial["fingerprint"], backend, backendPort); err != nil {
		t.Fatal("rotated Alice credential failed")
	}
	waitTrojanConnections(t, instance)
	if err := trojanRoundTrip(t, port, aliceSecret, rotatedMaterial["fingerprint"], backend, backendPort); err == nil {
		t.Fatal("old Alice credential remained valid")
	}
	waitTrojanConnections(t, instance)
	if err := trojanRoundTrip(t, port, bobSecret, rotatedMaterial["fingerprint"], backend, backendPort); err != nil {
		t.Fatal("Bob credential failed after Alice rotation")
	}
	waitTrojanConnections(t, instance)
	if err := instance.Close(); err != nil {
		t.Fatal("instance close failed")
	}
	closed = true
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("Close did not free listener port")
	}
	_ = listener.Close()
}

func trojanSecret(t *testing.T, protocol servers.Protocol) string {
	t.Helper()
	secret, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	return secret["password"]
}

func trojanOptions(t *testing.T, port int, aliceSecret, bobSecret string, material map[string]string, aliceRoute, bobRoute string, allowed []netip.Prefix, allowedAccounts map[string][]netip.Prefix) core.Options {
	t.Helper()
	return core.Options{
		HomeDir:       testutil.TempHome(t),
		Authenticator: auth.NewAuthenticator(nil),
		RouteNodes: map[string][]map[string]any{
			"alice": {trojanNodeMap(aliceRoute)},
			"bob":   {trojanNodeMap(bobRoute)},
		},
		AccountRoute: map[string]string{"alice": "alice", "bob": "bob"},
		Servers: []servers.Spec{{
			Name: "entry", Protocol: "trojan", Bind: "127.0.0.1", Port: port,
			Accounts: []string{"alice", "bob"}, Params: map[string]string{},
			Material: servers.SecretMap(material),
			Users: servers.UserSecrets{
				"alice": {"password": aliceSecret},
				"bob":   {"password": bobSecret},
			},
		}},
		AccountAllowedClients: allowedAccounts,
		AllowedClients:        allowed,
	}
}

func trojanNodeMap(address string) map[string]any {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		panic("invalid recorder address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic("invalid recorder port")
	}
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": port}
}

func trojanRoundTrip(t *testing.T, port int, password, fingerprint, host string, backendPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), trojanTestTimeout)
	defer cancel()
	client, err := outbound.NewTrojan(outbound.TrojanOption{
		Name: "trojan-test", Server: "127.0.0.1", Port: port,
		Password: password, SNI: "localhost", Fingerprint: fingerprint,
	})
	if err != nil {
		return err
	}
	defer client.Close()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(trojanTestTimeout)); err != nil {
		return err
	}
	payload := []byte("trojan-echo")
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

func waitTrojanConnections(t *testing.T, instance *core.Instance) {
	t.Helper()
	timer := time.NewTimer(trojanTestTimeout)
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
			t.Fatal("Trojan server connections did not drain")
		}
	}
}

type trojanRecorder struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func startTrojanRecorder(t *testing.T) (*trojanRecorder, string) {
	t.Helper()
	recorder := &trojanRecorder{}
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
				recorder.serve(conn)
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

func (r *trojanRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets...)
}

func (r *trojanRecorder) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(trojanTestTimeout))
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
		address := make([]byte, 4)
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
		address := make([]byte, 16)
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
	destination := net.JoinHostPort(host, strconv.Itoa(int(portBytes[0])<<8|int(portBytes[1])))
	r.mu.Lock()
	r.targets = append(r.targets, destination)
	r.mu.Unlock()
	upstream, err := net.DialTimeout("tcp", destination, trojanTestTimeout)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(trojanTestTimeout))
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
	_ = conn.Close()
	_ = upstream.Close()
	<-done
}

func startTrojanEcho(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("echo listen failed")
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
				_ = conn.SetDeadline(time.Now().Add(trojanTestTimeout))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-acceptDone; workers.Wait() })
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal("echo address invalid")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("echo port invalid")
	}
	return host, port
}
