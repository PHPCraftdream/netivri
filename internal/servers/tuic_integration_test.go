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

// tuicRun holds the state shared by the phases of TestTUICCoreStartReconfigureAndClose.
type tuicRun struct {
	t             *testing.T
	protocol      servers.Protocol
	params        map[string]string
	material      map[string]string
	newMaterial   map[string]string
	alice         map[string]string
	bob           map[string]string
	rotated       map[string]string
	aliceRecorder *trojanRecorder
	bobRecorder   *trojanRecorder
	aliceRoute    string
	bobRoute      string
	host          string
	backendPort   int
	home          string
	instance      *core.Instance
	alicePort     int
	bobPort       int
	closed        bool
}

func TestTUICCoreStartReconfigureAndClose(t *testing.T) {
	r := newTUICRun(t)
	defer func() {
		if !r.closed {
			defer func() { _ = r.instance.Close() }()
			waitTUICConnections(t, r.instance)
		}
	}()
	r.startBobPort()
	servertest.TrustForTest(t, r.material)
	r.checkAliceRouteAndAuthFailures()
	r.checkBobRoute()
	r.checkGlobalPolicy()
	r.checkAccountPolicy()
	r.checkRotation()
	r.checkNewPortAndClose()
}

func newTUICRun(t *testing.T) *tuicRun {
	t.Helper()
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
	r := &tuicRun{
		t: t, protocol: protocol, params: params, material: material,
		alice: alice, bob: bob, rotated: rotated,
		aliceRecorder: aliceRecorder, bobRecorder: bobRecorder,
		aliceRoute: aliceRoute, bobRoute: bobRoute,
		host: host, backendPort: backendPort, home: t.TempDir(),
	}
	r.alicePort = servertest.StartOnFreePort(t, func(p int) error {
		candidate, e := core.Start(context.Background(), tuicOptions(t, r.home, p, 0, r.alice, r.bob, r.material, r.aliceRoute, r.bobRoute, nil, nil))
		if e != nil {
			return portCollision(e)
		}
		r.instance = candidate
		return nil
	})
	return r
}

func (r *tuicRun) startBobPort() {
	r.bobPort = servertest.StartOnFreePort(r.t, func(p int) error {
		if p == r.alicePort {
			return syscall.EADDRINUSE
		}
		if e := r.instance.Reconfigure(tuicOptions(r.t, r.home, r.alicePort, p, r.alice, r.bob, r.material, r.aliceRoute, r.bobRoute, nil, nil)); e != nil {
			return portCollision(e)
		}
		return nil
	})
}

func (r *tuicRun) checkAliceRouteAndAuthFailures() {
	t := r.t
	if err := tuicRoundTrip(t, r.alicePort, r.alice, r.material["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("Alice TUIC round trip failed")
	}
	waitTUICConnections(t, r.instance)
	if got := r.aliceRecorder.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(r.host, strconv.Itoa(r.backendPort)) {
		t.Fatal("Alice traffic did not reach its backend")
	}
	if len(r.bobRecorder.snapshot()) != 0 {
		t.Fatal("Alice traffic reached Bob backend")
	}
	if err := tuicAuthenticationFailure(t, r.alicePort, badTUICSecret(r.alice), r.material["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("wrong credentials were not rejected as authentication")
	}
	waitTUICConnections(t, r.instance)
	if err := tuicAuthenticationFailure(t, r.alicePort, r.bob, r.material["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("wrong UUID was not rejected as authentication")
	}
	waitTUICConnections(t, r.instance)
	if len(r.aliceRecorder.snapshot()) != 1 || len(r.bobRecorder.snapshot()) != 0 {
		t.Fatal("wrong UUID changed backend recorder counts")
	}
}

func (r *tuicRun) checkBobRoute() {
	t := r.t
	if err := tuicRoundTrip(t, r.bobPort, r.bob, r.material["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("Bob TUIC round trip failed")
	}
	waitTUICConnections(t, r.instance)
	if got := r.bobRecorder.snapshot(); len(got) != 1 || got[0] != net.JoinHostPort(r.host, strconv.Itoa(r.backendPort)) {
		t.Fatal("Bob traffic did not reach its backend")
	}
}

func (r *tuicRun) checkGlobalPolicy() {
	t := r.t
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	baselineDrops := tuicDropped(r.instance)
	if err := r.instance.Reconfigure(tuicOptions(t, r.home, r.alicePort, r.bobPort, r.alice, r.bob, r.material, r.aliceRoute, r.bobRoute, blocked, nil)); err != nil {
		t.Fatal("client policy update failed")
	}
	if err := tuicRoundTrip(t, r.alicePort, r.alice, r.material["fingerprint"], r.host, r.backendPort); err == nil {
		t.Fatal("disallowed client accepted")
	}
	waitTUICConnections(t, r.instance)
	if tuicDropped(r.instance) <= baselineDrops {
		t.Fatal("disallowed client was not counted")
	}
	if len(r.aliceRecorder.snapshot()) != 1 || len(r.bobRecorder.snapshot()) != 1 {
		t.Fatal("global filter changed backend recorder counts")
	}
}

func (r *tuicRun) checkAccountPolicy() {
	t := r.t
	blocked := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err := r.instance.Reconfigure(tuicOptions(t, r.home, r.alicePort, r.bobPort, r.alice, r.bob, r.material, r.aliceRoute, r.bobRoute, nil, map[string][]netip.Prefix{"alice": blocked})); err != nil {
		t.Fatal("per-account policy update failed")
	}
	aliceDrops := tuicDropped(r.instance)
	if err := tuicRoundTrip(t, r.alicePort, r.alice, r.material["fingerprint"], r.host, r.backendPort); err == nil {
		t.Fatal("per-account disallowed client accepted")
	}
	waitTUICConnections(t, r.instance)
	if tuicDropped(r.instance) <= aliceDrops || len(r.aliceRecorder.snapshot()) != 1 {
		t.Fatal("per-account filter was not counted or changed Alice route")
	}
	if err := tuicRoundTrip(t, r.bobPort, r.bob, r.material["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("Bob failed under Alice per-account filter")
	}
	waitTUICConnections(t, r.instance)
	if len(r.bobRecorder.snapshot()) != 2 {
		t.Fatal("Bob route changed under Alice per-account filter")
	}
	if err := r.instance.Reconfigure(tuicOptions(t, r.home, r.alicePort, r.bobPort, r.alice, r.bob, r.material, r.aliceRoute, r.bobRoute, nil, nil)); err != nil {
		t.Fatal("client policy reset failed")
	}
}

func (r *tuicRun) checkRotation() {
	t := r.t
	newMaterial, err := r.protocol.NewMaterial(r.params)
	if err != nil {
		t.Fatal("TLS rotation generation failed")
	}
	r.newMaterial = newMaterial
	if err := r.instance.Reconfigure(tuicOptions(t, r.home, r.alicePort, r.bobPort, r.rotated, r.bob, newMaterial, r.aliceRoute, r.bobRoute, nil, nil)); err != nil {
		t.Fatal("credential rotation failed")
	}
	servertest.TrustForTest(t, newMaterial)
	if err := tuicRoundTrip(t, r.alicePort, r.rotated, newMaterial["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("rotated credential failed")
	}
	waitTUICConnections(t, r.instance)
	if err := tuicAuthenticationFailure(t, r.alicePort, r.alice, newMaterial["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("old credential was not rejected as authentication")
	}
	waitTUICConnections(t, r.instance)
	if err := tuicRoundTrip(t, r.bobPort, r.bob, newMaterial["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("Bob failed after Alice rotation")
	}
	waitTUICConnections(t, r.instance)
}

func (r *tuicRun) checkNewPortAndClose() {
	t := r.t
	newPort := servertest.StartOnFreePort(t, func(p int) error {
		if p == r.alicePort || p == r.bobPort {
			return syscall.EADDRINUSE
		}
		if e := r.instance.Reconfigure(tuicOptions(t, r.home, p, r.bobPort, r.rotated, r.bob, r.newMaterial, r.aliceRoute, r.bobRoute, nil, nil)); e != nil {
			return portCollision(e)
		}
		return nil
	})
	probe, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.alicePort)))
	if err != nil {
		t.Fatal("old UDP port was not released")
	}
	_ = probe.Close()
	if err := tuicRoundTrip(t, newPort, r.rotated, r.newMaterial["fingerprint"], r.host, r.backendPort); err != nil {
		t.Fatal("new UDP port failed")
	}
	waitTUICConnections(t, r.instance)
	if err := r.instance.Close(); err != nil {
		t.Fatal("instance close failed")
	}
	r.closed = true
	for _, p := range []int{r.alicePort, r.bobPort, newPort} {
		pc, e := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
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
	defer func() { _ = client.Close() }()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: servertest.MustUint16(t, backendPort)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
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
	defer func() { _ = client.Close() }()
	classify := func(err error) bool {
		var app *quic.ApplicationError
		return errors.As(err, &app) && app.ErrorCode == v5.AuthenticationFailed
	}
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: servertest.MustUint16(t, backendPort)})
	if err != nil {
		if classify(err) {
			return nil
		}
		return err
	}
	defer func() { _ = conn.Close() }()
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
func tuicDropped(inst *core.Instance) uint64 {
	for _, server := range inst.Servers() {
		if server.Name == "tuic-alice" {
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
