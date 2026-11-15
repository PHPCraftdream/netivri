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

// h2RouteRun holds the state shared by the phases of TestHysteria2CoreRoutesAuthPolicyRotationAndClose.
type h2RouteRun struct {
	t             *testing.T
	protocol      servers.Protocol
	alice         string
	bob           string
	material      map[string]string
	aliceAddress  string
	bobAddress    string
	aliceRecorder *h2Rec
	bobRecorder   *h2Rec
	backendPort   int
	wantTarget    string
	home          string
	port          int
	rotated       string
	instance      *core.Instance
	closed        bool
}

func TestHysteria2CoreRoutesAuthPolicyRotationAndClose(t *testing.T) {
	r := newH2RouteRun(t)
	r.start()
	defer func() {
		if r.instance != nil && !r.closed {
			defer func() { _ = r.instance.Close() }()
			h2Drain(t, r.instance)
		}
	}()
	servertest.TrustForTest(t, r.material)
	aliceBefore, bobBefore := r.checkPinnedRoutes()
	r.checkWrongAuth(aliceBefore, bobBefore)
	r.checkGlobalDeny()
	r.checkAccountDeny()
	r.checkRotation()
	r.checkOldCredential()
	oldPort, newPort := r.checkPortChange()
	r.checkOldPortFreedAndClose(oldPort, newPort)
}

func newH2RouteRun(t *testing.T) *h2RouteRun {
	t.Helper()
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
	return &h2RouteRun{
		t: t, protocol: protocol, alice: alice, bob: bob, material: material,
		aliceAddress: aliceAddress, bobAddress: bobAddress,
		aliceRecorder: aliceRecorder, bobRecorder: bobRecorder,
		backendPort: backendPort,
		wantTarget:  net.JoinHostPort(backendHost, strconv.Itoa(backendPort)),
		home:        testutil.TempHome(t),
	}
}

func (r *h2RouteRun) options(port int, alicePassword, bobPassword string, currentMaterial map[string]string, aliceNode, bobNode string, allowed []netip.Prefix, accounts map[string][]netip.Prefix) core.Options {
	return core.Options{
		HomeDir:       r.home,
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

func (r *h2RouteRun) start() {
	r.port = servertest.StartOnFreePort(r.t, func(candidate int) error {
		started, startErr := core.Start(context.Background(), r.options(candidate, r.alice, r.bob, r.material, r.aliceAddress, r.bobAddress, nil, nil))
		if startErr != nil {
			return startErr
		}
		r.instance = started
		return nil
	})
}

func (r *h2RouteRun) checkPinnedRoutes() ([]string, []string) {
	t := r.t
	if err := h2Roundtrip(t, r.port, r.alice, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("pinned Alice client failed:", err)
	}
	h2Drain(t, r.instance)
	if got := r.aliceRecorder.snapshot(); len(got) != 1 || got[0] != r.wantTarget {
		t.Fatalf("Alice target = %v, want exactly [%s]", got, r.wantTarget)
	}
	if got := r.bobRecorder.snapshot(); len(got) != 0 {
		t.Fatalf("Alice traffic reached Bob route: %v", got)
	}
	aliceBefore := r.aliceRecorder.snapshot()

	if err := h2Roundtrip(t, r.port, r.bob, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("pinned Bob client failed:", err)
	}
	h2Drain(t, r.instance)
	if got := r.bobRecorder.snapshot(); len(got) != 1 || got[0] != r.wantTarget {
		t.Fatalf("Bob target = %v, want exactly [%s]", got, r.wantTarget)
	}
	if got := r.aliceRecorder.snapshot(); !h2SameTargets(aliceBefore, got) {
		t.Fatalf("Bob traffic changed Alice route: %v", got)
	}
	return r.aliceRecorder.snapshot(), r.bobRecorder.snapshot()
}

func (r *h2RouteRun) checkWrongAuth(aliceBefore, bobBefore []string) {
	t := r.t
	wrongAuth := h2Roundtrip(t, r.port, "incorrect-password", r.material["fingerprint"], r.backendPort)
	if wrongAuth == nil || !strings.Contains(wrongAuth.Error(), "authentication failed, status code: 404") || strings.Contains(strings.ToLower(wrongAuth.Error()), "timeout") {
		t.Fatalf("wrong credential error = %v, want Hysteria2 authentication status 404", wrongAuth)
	}
	h2Drain(t, r.instance)
	if !h2SameTargets(aliceBefore, r.aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, r.bobRecorder.snapshot()) {
		t.Fatal("wrong credentials reached an account recorder")
	}
}

func (r *h2RouteRun) checkGlobalDeny() {
	t := r.t
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := r.instance.Reconfigure(r.options(r.port, r.alice, r.bob, r.material, r.aliceAddress, r.bobAddress, blocked, nil)); err != nil {
		t.Fatal("global policy update failed:", err)
	}
	beforeDrops := h2Dropped(r.instance)
	aliceBefore, bobBefore := r.aliceRecorder.snapshot(), r.bobRecorder.snapshot()
	if err := h2Roundtrip(t, r.port, r.alice, r.material["fingerprint"], r.backendPort); err == nil {
		t.Fatal("global deny policy allowed Alice")
	}
	h2Drain(t, r.instance)
	if h2Dropped(r.instance) <= beforeDrops {
		t.Fatal("global client drop was not counted")
	}
	if !h2SameTargets(aliceBefore, r.aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, r.bobRecorder.snapshot()) {
		t.Fatal("globally denied traffic reached a recorder")
	}
}

func (r *h2RouteRun) checkAccountDeny() {
	t := r.t
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := r.instance.Reconfigure(r.options(r.port, r.alice, r.bob, r.material, r.aliceAddress, r.bobAddress, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("account policy update failed:", err)
	}
	beforeDrops := h2Dropped(r.instance)
	aliceBefore, bobBefore := r.aliceRecorder.snapshot(), r.bobRecorder.snapshot()
	if err := h2Roundtrip(t, r.port, r.alice, r.material["fingerprint"], r.backendPort); err == nil {
		t.Fatal("Alice-specific deny policy allowed Alice")
	}
	h2Drain(t, r.instance)
	if h2Dropped(r.instance) <= beforeDrops {
		t.Fatal("account-specific client drop was not counted")
	}
	if !h2SameTargets(aliceBefore, r.aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, r.bobRecorder.snapshot()) {
		t.Fatal("denied Alice traffic reached a recorder")
	}
	if err := h2Roundtrip(t, r.port, r.bob, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("Alice restriction blocked Bob:", err)
	}
	h2Drain(t, r.instance)
	bobBefore = r.bobRecorder.snapshot()
	if len(bobBefore) != 2 || bobBefore[1] != r.wantTarget {
		t.Fatalf("Bob target after Alice restriction = %v", bobBefore)
	}
}

func (r *h2RouteRun) checkRotation() {
	t := r.t
	r.rotated = h2Secret(t, r.protocol)
	if err := r.instance.Reconfigure(r.options(r.port, r.rotated, r.bob, r.material, r.aliceAddress, r.bobAddress, nil, nil)); err != nil {
		t.Fatal("credential rotation failed:", err)
	}
	if err := h2Roundtrip(t, r.port, r.rotated, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("rotated credential failed:", err)
	}
	h2Drain(t, r.instance)
}

func (r *h2RouteRun) checkOldCredential() {
	t := r.t
	if err := r.instance.Reconfigure(r.options(r.port, r.rotated, r.bob, r.material, r.aliceAddress, r.bobAddress, nil, nil)); err != nil {
		t.Fatal("policy removal failed:", err)
	}
	aliceBefore, bobBefore := r.aliceRecorder.snapshot(), r.bobRecorder.snapshot()
	oldAuth := h2Roundtrip(t, r.port, r.alice, r.material["fingerprint"], r.backendPort)
	if oldAuth == nil || !strings.Contains(oldAuth.Error(), "authentication failed, status code: 404") || strings.Contains(strings.ToLower(oldAuth.Error()), "timeout") {
		t.Fatalf("old credential error = %v, want Hysteria2 authentication status 404", oldAuth)
	}
	h2Drain(t, r.instance)
	if !h2SameTargets(aliceBefore, r.aliceRecorder.snapshot()) || !h2SameTargets(bobBefore, r.bobRecorder.snapshot()) {
		t.Fatal("old credential reached an account recorder")
	}
	if err := h2Roundtrip(t, r.port, r.rotated, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("pinned client stopped serving after failed authentication:", err)
	}
	h2Drain(t, r.instance)
}

// checkPortChange moves the listener to a new port and returns the old and new ports.
func (r *h2RouteRun) checkPortChange() (int, int) {
	t := r.t
	oldPort := r.port
	r.port = servertest.StartOnFreePort(t, func(candidate int) error {
		if candidate == oldPort {
			return syscall.EADDRINUSE
		}
		startErr := r.instance.Reconfigure(r.options(candidate, r.rotated, r.bob, r.material, r.aliceAddress, r.bobAddress, nil, nil))
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
	newPort := r.port
	if newPort == oldPort {
		t.Fatal("replacement listener reused old port")
	}
	if err := h2Roundtrip(t, newPort, r.rotated, r.material["fingerprint"], r.backendPort); err != nil {
		t.Fatal("changed port did not serve:", err)
	}
	h2Drain(t, r.instance)
	return oldPort, newPort
}

func (r *h2RouteRun) checkOldPortFreedAndClose(oldPort, newPort int) {
	t := r.t
	oldProbe, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort)))
	if err != nil {
		t.Fatal("old UDP port not freed:", err)
	}
	_ = oldProbe.Close()
	if err := r.instance.Close(); err != nil {
		t.Fatal("instance close failed:", err)
	}
	r.closed = true
	newProbe, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(newPort)))
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
	probe, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
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
	defer func() { _ = client.Close() }()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: servertest.MustUint16(t, destinationPort)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
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
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
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
	defer func() { _ = conn.Close() }()
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
	upstream, err := (&net.Dialer{Timeout: h2Timeout}).DialContext(context.Background(), "tcp", target)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
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
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
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
				defer func() { _ = conn.Close() }()
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
