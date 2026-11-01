//go:build integration

package servers_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

const h2Timeout = 10 * time.Second

func TestHysteria2CoreRoutesAuthPolicyRotationAndClose(t *testing.T) {
	protocol, ok := servers.Lookup("hysteria2")
	if !ok {
		t.Fatal("Hysteria2 protocol is not registered")
	}
	alice, bob := h2Secret(t, protocol), h2Secret(t, protocol)
	material, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	aliceAddress, aliceRecorder := h2Recorder(t)
	bobAddress, bobRecorder := h2Recorder(t)
	backendHost, backendPort := h2Echo(t)
	home := testutil.TempHome(t)
	options := func(port int, alicePassword, bobPassword string, currentMaterial map[string]string, aliceNode, bobNode string, allowed []netip.Prefix, accounts map[string][]netip.Prefix) core.Options {
		return core.Options{
			HomeDir:       home,
			Authenticator: auth.NewAuthenticator(nil),
			RouteNodes: map[string][]map[string]any{
				"alice": {h2NodeMap(aliceNode)},
				"bob":   {h2NodeMap(bobNode)},
			},
			AccountRoute:          map[string]string{"alice": "alice", "bob": "bob"},
			AllowedClients:        allowed,
			AccountAllowedClients: accounts,
			Servers: []servers.Spec{{
				Name: "entry", Protocol: "hysteria2", Bind: "127.0.0.1", Port: port,
				Accounts: []string{"alice", "bob"}, Params: map[string]string{},
				Material: servers.SecretMap(currentMaterial),
				Users: servers.UserSecrets{
					"alice": {"password": alicePassword},
					"bob":   {"password": bobPassword},
				},
			}},
		}
	}
	var instance *core.Instance
	port := servertest.StartOnFreePort(t, func(candidate int) error {
		started, startErr := core.Start(context.Background(), options(candidate, alice, bob, material, aliceAddress, bobAddress, nil, nil))
		if startErr != nil {
			return startErr
		}
		instance = started
		return nil
	})
	closed := false
	defer func() {
		if instance != nil && !closed {
			defer instance.Close()
			h2Drain(t, instance)
		}
	}()
	servertest.TrustForTest(t, material)
	wantTarget := net.JoinHostPort(backendHost, strconv.Itoa(backendPort))

	if err := h2Roundtrip(t, port, alice, material["fingerprint"], backendPort); err != nil {
		t.Fatal("pinned Alice client failed:", err)
	}
	h2Drain(t, instance)
	if got := aliceRecorder.snapshot(); len(got) != 1 || got[0] != wantTarget {
		t.Fatalf("Alice target = %v, want exactly [%s]", got, wantTarget)
	}
	if got := bobRecorder.snapshot(); len(got) != 0 {
		t.Fatalf("Alice traffic reached Bob route: %v", got)
	}
	aliceBefore, bobBefore := aliceRecorder.snapshot(), bobRecorder.snapshot()

	if err := h2Roundtrip(t, port, bob, material["fingerprint"], backendPort); err != nil {
		t.Fatal("pinned Bob client failed:", err)
	}
	h2Drain(t, instance)
	if got := bobRecorder.snapshot(); len(got) != 1 || got[0] != wantTarget {
		t.Fatalf("Bob target = %v, want exactly [%s]", got, wantTarget)
	}
	if got := aliceRecorder.snapshot(); !h2SameTargets(aliceBefore, got) {
		t.Fatalf("Bob traffic changed Alice route: %v", got)
	}
	aliceBefore, bobBefore = aliceRecorder.snapshot(), bobRecorder.snapshot()

	wrongAuth := h2Roundtrip(t, port, "incorrect-password", material["fingerprint"], backendPort)
	if wrongAuth == nil || !strings.Contains(wrongAuth.Error(), "authentication failed, status code: 404") || strings.Contains(strings.ToLower(wrongAuth.Error()), "timeout") {
		t.Fatalf("wrong credential error = %v, want Hysteria2 authentication status 404", wrongAuth)
	}
	h2Drain(t, instance)
	if !h2SameTargets(aliceBefore, aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, bobRecorder.snapshot()) {
		t.Fatal("wrong credentials reached an account recorder")
	}

	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := instance.Reconfigure(options(port, alice, bob, material, aliceAddress, bobAddress, blocked, nil)); err != nil {
		t.Fatal("global policy update failed:", err)
	}
	beforeDrops := h2Dropped(instance)
	aliceBefore, bobBefore = aliceRecorder.snapshot(), bobRecorder.snapshot()
	if err := h2Roundtrip(t, port, alice, material["fingerprint"], backendPort); err == nil {
		t.Fatal("global deny policy allowed Alice")
	}
	h2Drain(t, instance)
	if h2Dropped(instance) <= beforeDrops {
		t.Fatal("global client drop was not counted")
	}
	if !h2SameTargets(aliceBefore, aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, bobRecorder.snapshot()) {
		t.Fatal("globally denied traffic reached a recorder")
	}
	if err := instance.Reconfigure(options(port, alice, bob, material, aliceAddress, bobAddress, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("account policy update failed:", err)
	}
	beforeDrops = h2Dropped(instance)
	aliceBefore, bobBefore = aliceRecorder.snapshot(), bobRecorder.snapshot()
	if err := h2Roundtrip(t, port, alice, material["fingerprint"], backendPort); err == nil {
		t.Fatal("Alice-specific deny policy allowed Alice")
	}
	h2Drain(t, instance)
	if h2Dropped(instance) <= beforeDrops {
		t.Fatal("account-specific client drop was not counted")
	}
	if !h2SameTargets(aliceBefore, aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, bobRecorder.snapshot()) {
		t.Fatal("denied Alice traffic reached a recorder")
	}
	if err := h2Roundtrip(t, port, bob, material["fingerprint"], backendPort); err != nil {
		t.Fatal("Alice restriction blocked Bob:", err)
	}
	h2Drain(t, instance)
	bobBefore = bobRecorder.snapshot()
	if len(bobBefore) != 2 || bobBefore[1] != wantTarget {
		t.Fatalf("Bob target after Alice restriction = %v", bobBefore)
	}
	aliceBefore = aliceRecorder.snapshot()

	rotated := h2Secret(t, protocol)
	if err := instance.Reconfigure(options(port, rotated, bob, material, aliceAddress, bobAddress, nil, nil)); err != nil {
		t.Fatal("credential rotation failed:", err)
	}
	if err := h2Roundtrip(t, port, rotated, material["fingerprint"], backendPort); err != nil {
		t.Fatal("rotated credential failed:", err)
	}
	h2Drain(t, instance)
	if err := instance.Reconfigure(options(port, rotated, bob, material, aliceAddress, bobAddress, nil, nil)); err != nil {
		t.Fatal("policy removal failed:", err)
	}
	aliceBefore, bobBefore = aliceRecorder.snapshot(), bobRecorder.snapshot()
	oldAuth := h2Roundtrip(t, port, alice, material["fingerprint"], backendPort)
	if oldAuth == nil || !strings.Contains(oldAuth.Error(), "authentication failed, status code: 404") || strings.Contains(strings.ToLower(oldAuth.Error()), "timeout") {
		t.Fatalf("old credential error = %v, want Hysteria2 authentication status 404", oldAuth)
	}
	h2Drain(t, instance)
	if !h2SameTargets(aliceBefore, aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, bobRecorder.snapshot()) {
		t.Fatal("old credential reached an account recorder")
	}
	if err := h2Roundtrip(t, port, rotated, material["fingerprint"], backendPort); err != nil {
		t.Fatal("pinned client stopped serving after failed authentication:", err)
	}
	h2Drain(t, instance)

	oldPort := port
	port = servertest.StartOnFreePort(t, func(candidate int) error {
		if candidate == oldPort {
			return syscall.EADDRINUSE
		}
		startErr := instance.Reconfigure(options(candidate, rotated, bob, material, aliceAddress, bobAddress, nil, nil))
		if startErr != nil {
			var cliErr *cli.Error
			if errors.As(startErr, &cliErr) && cliErr.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			if errors.Is(startErr, syscall.Errno(10048)) || errors.Is(startErr, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(startErr.Error()), "address already in use") || strings.Contains(strings.ToLower(startErr.Error()), "bind: only one usage") {
				return syscall.EADDRINUSE
			}
			return startErr
		}
		return nil
	})
	newPort := port
	if newPort == oldPort {
		t.Fatal("replacement listener reused old port")
	}
	if err := h2Roundtrip(t, newPort, rotated, material["fingerprint"], backendPort); err != nil {
		t.Fatal("changed port did not serve:", err)
	}
	h2Drain(t, instance)
	oldProbe, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)))
	if err != nil {
		t.Fatal("old UDP port not freed:", err)
	}
	_ = oldProbe.Close()
	if err := instance.Close(); err != nil {
		t.Fatal("instance close failed:", err)
	}
	closed = true
	newProbe, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(newPort)))
	if err != nil {
		t.Fatal("Close did not free UDP:", err)
	}
	_ = newProbe.Close()

}

func TestHysteria2ImmediateCloseReleasesUDP(t *testing.T) {
	protocol, ok := servers.Lookup("hysteria2")
	if !ok {
		t.Fatal("Hysteria2 protocol is not registered")
	}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	secret := h2Secret(t, protocol)
	home := testutil.TempHome(t)
	var instance *core.Instance
	port := servertest.StartOnFreePort(t, func(candidate int) error {
		started, startErr := core.Start(context.Background(), core.Options{
			HomeDir: home,
			Servers: []servers.Spec{{
				Name: "entry", Protocol: "hysteria2", Bind: "127.0.0.1", Port: candidate,
				Accounts: []string{"alice"}, Params: map[string]string{},
				Material: servers.SecretMap(material),
				Users:    servers.UserSecrets{"alice": {"password": secret}},
			}},
		})
		if startErr != nil {
			return startErr
		}
		instance = started
		return nil
	})
	t.Cleanup(func() {
		if instance != nil {
			_ = instance.Close()
		}
	})
	if err := instance.Close(); err != nil {
		t.Fatal("immediate Close failed:", err)
	}
	probe, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal("immediate Close did not release UDP:", err)
	}
	_ = probe.Close()
}

func h2Secret(t *testing.T, protocol servers.Protocol) string {
	t.Helper()
	secret, err := protocol.NewSecret("user", nil)
	if err != nil {
		t.Fatal("secret generation failed:", err)
	}
	return secret["password"]
}

func h2NodeMap(address string) map[string]any {
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

func h2SameTargets(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

func h2Dropped(instance *core.Instance) uint64 {
	var dropped uint64
	for _, status := range instance.Servers() {
		dropped += status.DroppedClients
	}
	return dropped
}

func h2Roundtrip(t *testing.T, port int, password, pin string, destinationPort int) error {
	err := h2RoundtripOnce(t, port, password, pin, destinationPort)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "handshake timeout") {
		return h2RoundtripOnce(t, port, password, pin, destinationPort)
	}
	return err
}

func h2RoundtripOnce(t *testing.T, port int, password, pin string, destinationPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), h2Timeout)
	defer cancel()
	client, err := outbound.NewHysteria2(outbound.Hysteria2Option{Name: "h2", Server: "127.0.0.1", Port: port, Password: password, SNI: "localhost", Fingerprint: pin})
	if err != nil {
		return err
	}
	defer client.Close()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: uint16(destinationPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(h2Timeout)); err != nil {
		return err
	}
	payload := []byte("hysteria2-route")
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

func h2Drain(t *testing.T, instance *core.Instance) {
	t.Helper()
	timer := time.NewTimer(h2Timeout)
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
			t.Fatal("Hysteria2 connections did not drain")
		}
	}
}

type h2Rec struct {
	mu      sync.Mutex
	targets []string
	workers sync.WaitGroup
}

func (recorder *h2Rec) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.targets...)
}

func h2Recorder(t *testing.T) (string, *h2Rec) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listen failed:", err)
	}
	recorder := &h2Rec{}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			recorder.workers.Add(1)
			go func(conn net.Conn) {
				defer recorder.workers.Done()
				recorder.serve(conn)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		recorder.workers.Wait()
	})
	return listener.Addr().String(), recorder
}

func (recorder *h2Rec) serve(conn net.Conn) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(h2Timeout)); err != nil {
		return
	}
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 5 || header[1] == 0 {
		return
	}
	methods := make([]byte, int(header[1]))
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
	host, err := h2ReadSOCKSHost(conn, request[3])
	if err != nil {
		return
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(portBytes[0])<<8|int(portBytes[1])))
	recorder.mu.Lock()
	recorder.targets = append(recorder.targets, target)
	recorder.mu.Unlock()
	upstream, err := net.DialTimeout("tcp", target, h2Timeout)
	if err != nil {
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	if err := upstream.SetDeadline(time.Now().Add(h2Timeout)); err != nil {
		return
	}
	var copies sync.WaitGroup
	copies.Add(2)
	finished := make(chan struct{}, 2)
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
	<-finished
	_ = conn.Close()
	_ = upstream.Close()
	<-finished
	copies.Wait()
}

func h2ReadSOCKSHost(conn net.Conn, addressType byte) (string, error) {
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
		return "", errors.New("unsupported SOCKS address type")
	}
}

func h2Echo(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("echo listen failed:", err)
	}
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			workers.Add(1)
			go func(conn net.Conn) {
				defer workers.Done()
				defer conn.Close()
				if err := conn.SetDeadline(time.Now().Add(h2Timeout)); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		workers.Wait()
	})
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal("invalid echo listener address:", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("invalid echo listener port:", err)
	}
	return "127.0.0.1", port
}
