//go:build integration

package servers_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
	v5 "github.com/metacubex/mihomo/transport/tuic/v5"
	"github.com/metacubex/quic-go"
)

const tuicIntegrationTimeout = 20 * time.Second

func TestTUICCoreStartReconfigureAndClose(t *testing.T) {
	protocol, ok := servers.Lookup("tuic")
	if !ok {
		t.Fatal("TUIC protocol is not registered")
	}
	params := map[string]string{"alpn": "h3", "sni": "localhost", "congestion": "cubic"}
	material, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal("TLS material generation failed")
	}
	alice, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("Alice secret generation failed")
	}
	bob, err := protocol.NewSecret("bob", nil)
	if err != nil {
		t.Fatal("Bob secret generation failed")
	}
	rotated, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("rotated secret generation failed")
	}
	aliceRecorder, aliceRoute := startTrojanRecorder(t)
	bobRecorder, bobRoute := startTrojanRecorder(t)
	host, backendPort := startTrojanEcho(t)
	home := t.TempDir()
	var instance *core.Instance
	var alicePort, bobPort int
	alicePort = servertest.StartOnFreePort(t, func(p int) error {
		candidate, e := core.Start(context.Background(), tuicOptions(t, home, p, 0, alice, bob, material, aliceRoute, bobRoute, nil, nil))
		if e != nil {
			return portCollision(e)
		}
		instance = candidate
		return nil
	})
	closed := false
	defer func() {
		if !closed {
			defer instance.Close()
			waitTUICConnections(t, instance)
		}
	}()
	bobPort = servertest.StartOnFreePort(t, func(p int) error {
		if p == alicePort {
			return syscall.EADDRINUSE
		}
		if e := instance.Reconfigure(tuicOptions(t, home, alicePort, p, alice, bob, material, aliceRoute, bobRoute, nil, nil)); e != nil {
			return portCollision(e)
		}
		return nil
	})
	servertest.TrustForTest(t, material)
	if err := tuicRoundTrip(t, alicePort, alice, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("Alice TUIC round trip failed")
	}
	waitTUICConnections(t, instance)
	if got := aliceRecorder.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(host, strconv.Itoa(backendPort)) {
		t.Fatal("Alice traffic did not reach its backend")
	}
	if len(bobRecorder.snapshot()) != 0 {
		t.Fatal("Alice traffic reached Bob backend")
	}
	if err := tuicAuthenticationFailure(t, alicePort, badTUICSecret(alice), material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("wrong credentials were not rejected as authentication")
	}
	waitTUICConnections(t, instance)
	if err := tuicAuthenticationFailure(t, alicePort, bob, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("wrong UUID was not rejected as authentication")
	}
	waitTUICConnections(t, instance)
	if len(aliceRecorder.snapshot()) != 1 || len(bobRecorder.snapshot()) != 0 {
		t.Fatal("wrong UUID changed backend recorder counts")
	}
	if err := tuicRoundTrip(t, bobPort, bob, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("Bob TUIC round trip failed")
	}
	waitTUICConnections(t, instance)
	if got := bobRecorder.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(host, strconv.Itoa(backendPort)) {
		t.Fatal("Bob traffic did not reach its backend")
	}
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	baselineDrops := tuicDropped(instance, "tuic-alice")
	if err := instance.Reconfigure(tuicOptions(t, home, alicePort, bobPort, alice, bob, material, aliceRoute, bobRoute, blocked, nil)); err != nil {
		t.Fatal("client policy update failed")
	}
	if err := tuicRoundTrip(t, alicePort, alice, material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("disallowed client accepted")
	}
	waitTUICConnections(t, instance)
	if tuicDropped(instance, "tuic-alice") <= baselineDrops {
		t.Fatal("disallowed client was not counted")
	}
	if len(aliceRecorder.snapshot()) != 1 || len(bobRecorder.snapshot()) != 1 {
		t.Fatal("global filter changed backend recorder counts")
	}
	if err := instance.Reconfigure(tuicOptions(t, home, alicePort, bobPort, alice, bob, material, aliceRoute, bobRoute, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("per-account policy update failed")
	}
	aliceDrops := tuicDropped(instance, "tuic-alice")
	if err := tuicRoundTrip(t, alicePort, alice, material["fingerprint"], host, backendPort); err == nil {
		t.Fatal("per-account disallowed client accepted")
	}
	waitTUICConnections(t, instance)
	if tuicDropped(instance, "tuic-alice") <= aliceDrops || len(aliceRecorder.snapshot()) != 1 {
		t.Fatal("per-account filter was not counted or changed Alice route")
	}
	if err := tuicRoundTrip(t, bobPort, bob, material["fingerprint"], host, backendPort); err != nil {
		t.Fatal("Bob failed under Alice per-account filter")
	}
	waitTUICConnections(t, instance)
	if len(bobRecorder.snapshot()) != 2 {
		t.Fatal("Bob route changed under Alice per-account filter")
	}
	if err := instance.Reconfigure(tuicOptions(t, home, alicePort, bobPort, alice, bob, material, aliceRoute, bobRoute, nil, nil)); err != nil {
		t.Fatal("client policy reset failed")
	}
	newMaterial, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal("TLS rotation generation failed")
	}
	if err := instance.Reconfigure(tuicOptions(t, home, alicePort, bobPort, rotated, bob, newMaterial, aliceRoute, bobRoute, nil, nil)); err != nil {
		t.Fatal("credential rotation failed")
	}
	servertest.TrustForTest(t, newMaterial)
	if err := tuicRoundTrip(t, alicePort, rotated, newMaterial["fingerprint"], host, backendPort); err != nil {
		t.Fatal("rotated credential failed")
	}
	waitTUICConnections(t, instance)
	if err := tuicAuthenticationFailure(t, alicePort, alice, newMaterial["fingerprint"], host, backendPort); err != nil {
		t.Fatal("old credential was not rejected as authentication")
	}
	waitTUICConnections(t, instance)
	if err := tuicRoundTrip(t, bobPort, bob, newMaterial["fingerprint"], host, backendPort); err != nil {
		t.Fatal("Bob failed after Alice rotation")
	}
	waitTUICConnections(t, instance)
	newPort := servertest.StartOnFreePort(t, func(p int) error {
		if p == alicePort || p == bobPort {
			return syscall.EADDRINUSE
		}
		if e := instance.Reconfigure(tuicOptions(t, home, p, bobPort, rotated, bob, newMaterial, aliceRoute, bobRoute, nil, nil)); e != nil {
			return portCollision(e)
		}
		return nil
	})
	probe, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(alicePort)))
	if err != nil {
		t.Fatal("old UDP port was not released")
	}
	_ = probe.Close()
	if err := tuicRoundTrip(t, newPort, rotated, newMaterial["fingerprint"], host, backendPort); err != nil {
		t.Fatal("new UDP port failed")
	}
	waitTUICConnections(t, instance)
	if err := instance.Close(); err != nil {
		t.Fatal("instance close failed")
	}
	closed = true
	for _, p := range []int{alicePort, bobPort, newPort} {
		pc, e := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if e != nil {
			t.Fatal("Close did not free UDP port")
		}
		_ = pc.Close()
	}
}

func portCollision(err error) error {
	var ce *cli.Error
	if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
		return syscall.EADDRINUSE
	}
	return err
}
func tuicOptions(t *testing.T, home string, alicePort, bobPort int, alice, bob, material map[string]string, aliceRoute, bobRoute string, allowed []netip.Prefix, peraccount map[string][]netip.Prefix) core.Options {
	t.Helper()
	params := map[string]string{"alpn": "h3", "sni": "localhost", "congestion": "cubic"}
	specs := []servers.Spec{{
		Name: "tuic-alice", Protocol: "tuic", Bind: "127.0.0.1", Port: alicePort,
		Accounts: []string{"alice"}, Params: params, Material: servers.SecretMap(material),
		Users: servers.UserSecrets{"alice": alice},
	}}
	if bobPort > 0 {
		specs = append(specs, servers.Spec{
			Name: "tuic-bob", Protocol: "tuic", Bind: "127.0.0.1", Port: bobPort,
			Accounts: []string{"bob"}, Params: params, Material: servers.SecretMap(material),
			Users: servers.UserSecrets{"bob": bob},
		})
	}
	return core.Options{
		HomeDir: home, Authenticator: auth.NewAuthenticator(nil),
		RouteNodes:   map[string][]map[string]any{"alice": {tuicSOCKSNode(aliceRoute)}, "bob": {tuicSOCKSNode(bobRoute)}},
		AccountRoute: map[string]string{"alice": "alice", "bob": "bob"}, Servers: specs,
		AllowedClients: allowed, AccountAllowedClients: peraccount,
	}
}
func tuicSOCKSNode(address string) map[string]any {
	_, p, e := net.SplitHostPort(address)
	if e != nil {
		panic("invalid test recorder address")
	}
	n, e := strconv.Atoi(p)
	if e != nil {
		panic("invalid test recorder port")
	}
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": n}
}
func badTUICSecret(in map[string]string) map[string]string {
	return map[string]string{"uuid": in["uuid"], "password": "wrong-password"}
}
func tuicRoundTrip(t *testing.T, port int, secret map[string]string, fingerprint, host string, backendPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), tuicIntegrationTimeout)
	defer cancel()
	client, err := outbound.NewTuic(outbound.TuicOption{Name: "tuic-test", Server: "127.0.0.1", Port: port, UUID: secret["uuid"], Password: secret["password"], SNI: "localhost", ALPN: []string{"h3"}, CongestionController: "cubic", Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	defer client.Close()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(tuicIntegrationTimeout)); err != nil {
		return err
	}
	payload := []byte("tuic-echo")
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
func tuicAuthenticationFailure(t *testing.T, port int, secret map[string]string, fingerprint, host string, backendPort int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), tuicIntegrationTimeout)
	defer cancel()
	client, err := outbound.NewTuic(outbound.TuicOption{Name: "tuic-bad", Server: "127.0.0.1", Port: port, UUID: secret["uuid"], Password: secret["password"], SNI: "localhost", ALPN: []string{"h3"}, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	defer client.Close()
	classify := func(err error) bool {
		var app *quic.ApplicationError
		return errors.As(err, &app) && app.ErrorCode == v5.AuthenticationFailed
	}
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
	if err != nil {
		if classify(err) {
			return nil
		}
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(tuicIntegrationTimeout)); err != nil {
		return err
	}
	if _, err = conn.Write([]byte("auth-check")); err != nil {
		if classify(err) {
			return nil
		}
		return err
	}
	_, err = conn.Read(make([]byte, 1))
	if classify(err) {
		return nil
	}
	return errors.New("connection did not fail with TUIC authentication error")
}
func tuicDropped(inst *core.Instance, name string) uint64 {
	for _, server := range inst.Servers() {
		if server.Name == name {
			return server.DroppedClients
		}
	}
	return 0
}

func waitTUICConnections(t *testing.T, inst *core.Instance) {
	t.Helper()
	timer := time.NewTimer(tuicIntegrationTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		n := 0
		for _, s := range inst.Servers() {
			n += s.Connections
		}
		if n == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("TUIC connections did not drain")
		}
	}
}
