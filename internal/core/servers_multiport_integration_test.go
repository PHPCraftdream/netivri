//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/auth"
	C "github.com/metacubex/mihomo/constant"
)

// multiportFixture is the state shared by the phases of
// TestTrojanAnyTLSShareEndpointReloadRollback.
type multiportFixture struct {
	host        string
	backendPort int
	port        int
	movedPort   int
	home        string
	tp          map[string]string
	xp          map[string]string
	tm          map[string]string
	xm          map[string]string
	ts          map[string]string
	xs          map[string]string
	inst        *Instance
}

// newMultiportFixture starts the echo backend and makes the Trojan and AnyTLS
// materials and secrets, with two free ports for the AnyTLS entry.
func newMultiportFixture(t *testing.T) *multiportFixture {
	t.Helper()
	f := &multiportFixture{}
	backend := echoServer(t)
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	f.backendPort, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := servers.Lookup("trojan")
	if !ok {
		t.Fatal("Trojan unavailable")
	}
	ap, ok := servers.Lookup("anytls")
	if !ok {
		t.Fatal("AnyTLS unavailable")
	}
	f.tp = map[string]string{"sni": "trojan.multiport.test", "public_host": "trojan.multiport.test"}
	f.xp = map[string]string{"sni": "anytls.multiport.test", "public_host": "anytls.multiport.test"}
	f.tm, err = p.NewMaterial(f.tp)
	if err != nil {
		t.Fatal(err)
	}
	f.xm, err = ap.NewMaterial(f.xp)
	if err != nil {
		t.Fatal(err)
	}
	f.ts, err = p.NewSecret("alice", f.tp)
	if err != nil {
		t.Fatal(err)
	}
	f.xs, err = ap.NewSecret("bob", f.xp)
	if err != nil {
		t.Fatal(err)
	}
	f.port = servertest.FreePort(t)
	f.movedPort = servertest.FreePort(t)
	for f.movedPort == f.port {
		f.movedPort = servertest.FreePort(t)
	}
	f.home = testutil.TempHome(t)
	return f
}

// makeSpecs lists the entries: the AnyTLS entry on anyPort and, when
// includeTrojan is set, the Trojan share on the fixture port.
func (f *multiportFixture) makeSpecs(anyPort int, includeTrojan bool) []servers.Spec {
	out := []servers.Spec{}
	if includeTrojan {
		out = append(out, servers.Spec{Name: "entry-trojan", Protocol: "trojan", Bind: "127.0.0.1", Port: f.port, Accounts: []string{"alice"}, Params: f.tp, Material: servers.SecretMap(f.tm), Users: servers.UserSecrets{"alice": servers.SecretMap(f.ts)}}.Clone())
	}
	return append(out, servers.Spec{Name: "entry-anytls", Protocol: "anytls", Bind: "127.0.0.1", Port: anyPort, Accounts: []string{"bob"}, Params: f.xp, Material: servers.SecretMap(f.xm), Users: servers.UserSecrets{"bob": servers.SecretMap(f.xs)}}.Clone())
}

// options is the instance options over the entry list specs.
func (f *multiportFixture) options(specs []servers.Spec) Options {
	return Options{HomeDir: f.home, Authenticator: auth.NewAuthenticator(nil), Servers: specs}
}

// start opens the instance with both entries on their first ports.
func (f *multiportFixture) start(ctx context.Context, t *testing.T) {
	t.Helper()
	inst, err := Start(ctx, f.options(f.makeSpecs(f.port, true)))
	if err != nil {
		t.Fatal(err)
	}
	f.inst = inst
}

// closeAtCleanup closes the instance when the test ends, after its connections.
func (f *multiportFixture) closeAtCleanup(t *testing.T) {
	t.Helper()
	inst := f.inst
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if err := inst.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
}

// dialHeld opens the Trojan connection that stays open across the reloads.
func (f *multiportFixture) dialHeld(ctx context.Context, t *testing.T, held C.ProxyAdapter) net.Conn {
	t.Helper()
	cctx, ccancel := context.WithTimeout(ctx, 8*time.Second)
	heldConn, err := held.DialContext(cctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(f.host), DstPort: toUint16(t, f.backendPort)})
	ccancel()
	if err != nil {
		t.Fatal(err)
	}
	return heldConn
}

// exchangeHeldEcho writes one payload over the held connection and reads it back.
func exchangeHeldEcho(heldConn net.Conn) error {
	if err := heldConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return err
	}
	payload := []byte("held-trojan-echo")
	if _, err := heldConn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(heldConn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// check dials the echo backend through the entry of protocol on targetPort and
// checks the echo: Trojan with Alice's secret, AnyTLS with Bob's.
func (f *multiportFixture) check(ctx context.Context, t *testing.T, protocol string, targetPort int) error {
	cctx, ccancel := context.WithTimeout(ctx, 8*time.Second)
	defer ccancel()
	var client C.ProxyAdapter
	var e error
	if protocol == "trojan" {
		client, e = outbound.NewTrojan(outbound.TrojanOption{Name: protocol + "-client", Server: "127.0.0.1", Port: targetPort, Password: f.ts["password"], SNI: f.tp["sni"], Fingerprint: f.tm["fingerprint"]})
	} else {
		client, e = outbound.NewAnyTLS(outbound.AnyTLSOption{Name: "anytls", Server: "127.0.0.1", Port: targetPort, Password: f.xs["password"], SNI: f.xp["sni"], Fingerprint: f.xm["fingerprint"], ClientFingerprint: "chrome", UDP: false})
	}
	if e != nil {
		return e
	}
	defer func() { _ = client.Close() }()
	conn, e := client.DialContext(cctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(f.host), DstPort: toUint16(t, f.backendPort)})
	if e != nil {
		return e
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return err
	}
	payload := []byte("multiport-echo")
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

// moveAnyTLS moves the AnyTLS entry to its second port and checks both entries
// and the held connection.
func (f *multiportFixture) moveAnyTLS(ctx context.Context, t *testing.T, heldConn net.Conn) {
	t.Helper()
	if err := f.inst.Reconfigure(f.options(f.makeSpecs(f.movedPort, true))); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if err := f.check(ctx, t, "trojan", f.port); err != nil {
		t.Fatalf("Trojan after moving AnyTLS: %v", err)
	}
	if err := f.check(ctx, t, "anytls", f.movedPort); err != nil {
		t.Fatalf("AnyTLS after moving: %v", err)
	}
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("held Trojan after move: %v", err)
	}
}

// moveAnyTLSBack moves the AnyTLS entry back to the first port and checks both
// entries and the held connection.
func (f *multiportFixture) moveAnyTLSBack(ctx context.Context, t *testing.T, heldConn net.Conn) {
	t.Helper()
	if err := f.inst.Reconfigure(f.options(f.makeSpecs(f.port, true))); err != nil {
		t.Fatalf("move AnyTLS back: %v", err)
	}
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("held Trojan after moving back: %v", err)
	}
	if err := f.check(ctx, t, "trojan", f.port); err != nil {
		t.Fatalf("Trojan after moving back: %v", err)
	}
	if err := f.check(ctx, t, "anytls", f.port); err != nil {
		t.Fatalf("AnyTLS after moving back: %v", err)
	}
}

// removeTrojan drops the Trojan entry, checks that the held connection
// survives, and restores the entry.
func (f *multiportFixture) removeTrojan(t *testing.T, heldConn net.Conn) {
	t.Helper()
	if err := f.inst.Reconfigure(f.options(f.makeSpecs(f.port, false))); err != nil {
		t.Fatalf("remove Trojan: %v", err)
	}
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("held Trojan after removal: %v", err)
	}
	if err := f.inst.Reconfigure(f.options(f.makeSpecs(f.port, true))); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

// rollbackRejected reloads with invalid TLS material, which must be refused
// and leave both entries and the held connection as they were.
func (f *multiportFixture) rollbackRejected(ctx context.Context, t *testing.T, heldConn net.Conn) {
	t.Helper()
	bad := f.makeSpecs(f.port, true)
	bad[0].Material["cert_pem"] = "invalid TLS material"
	if err := f.inst.Reconfigure(f.options(bad)); err == nil {
		t.Fatal("invalid TLS material reload succeeded")
	}
	if err := f.check(ctx, t, "anytls", f.port); err != nil {
		t.Fatalf("AnyTLS after rollback: %v", err)
	}
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("held Trojan after rollback: %v", err)
	}
	if err := f.check(ctx, t, "trojan", f.port); err != nil {
		t.Fatalf("Trojan after rollback: %v", err)
	}
}

func TestTrojanAnyTLSShareEndpointReloadRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := newMultiportFixture(t)
	f.start(ctx, t)
	held, err := outbound.NewTrojan(outbound.TrojanOption{Name: "held-trojan", Server: "127.0.0.1", Port: f.port, Password: f.ts["password"], SNI: f.tp["sni"], Fingerprint: f.tm["fingerprint"]})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	heldConn := f.dialHeld(ctx, t, held)
	defer func() { _ = heldConn.Close() }()
	f.closeAtCleanup(t)
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("initial held Trojan: %v", err)
	}
	if err := f.check(ctx, t, "trojan", f.port); err != nil {
		t.Fatalf("Trojan: %v", err)
	}
	if err := exchangeHeldEcho(heldConn); err != nil {
		t.Fatalf("held Trojan before reload: %v", err)
	}
	if err := f.check(ctx, t, "anytls", f.port); err != nil {
		t.Fatalf("AnyTLS: %v", err)
	}
	if len(f.inst.Servers()) != 2 {
		t.Fatalf("server infos: %d", len(f.inst.Servers()))
	}
	f.moveAnyTLS(ctx, t, heldConn)
	f.moveAnyTLSBack(ctx, t, heldConn)
	f.removeTrojan(t, heldConn)
	f.rollbackRejected(ctx, t, heldConn)
}
