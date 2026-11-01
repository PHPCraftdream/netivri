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

func TestTrojanAnyTLSShareEndpointReloadRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	backend := echoServer(t)
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(portText)
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
	tp := map[string]string{"sni": "trojan.multiport.test", "public_host": "trojan.multiport.test"}
	xp := map[string]string{"sni": "anytls.multiport.test", "public_host": "anytls.multiport.test"}
	tm, err := p.NewMaterial(tp)
	if err != nil {
		t.Fatal(err)
	}
	xm, err := ap.NewMaterial(xp)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := p.NewSecret("alice", tp)
	if err != nil {
		t.Fatal(err)
	}
	xs, err := ap.NewSecret("bob", xp)
	if err != nil {
		t.Fatal(err)
	}
	port := servertest.FreePort(t)
	movedPort := servertest.FreePort(t)
	for movedPort == port {
		movedPort = servertest.FreePort(t)
	}
	makeSpecs := func(anyPort int, includeTrojan, includeAny bool) []servers.Spec {
		out := []servers.Spec{}
		if includeTrojan {
			out = append(out, servers.Spec{Name: "entry-trojan", Protocol: "trojan", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: tp, Material: servers.SecretMap(tm), Users: servers.UserSecrets{"alice": servers.SecretMap(ts)}}.Clone())
		}
		if includeAny {
			out = append(out, servers.Spec{Name: "entry-anytls", Protocol: "anytls", Bind: "127.0.0.1", Port: anyPort, Accounts: []string{"bob"}, Params: xp, Material: servers.SecretMap(xm), Users: servers.UserSecrets{"bob": servers.SecretMap(xs)}}.Clone())
		}
		return out
	}
	home := testutil.TempHome(t)
	options := func(specs []servers.Spec) Options {
		return Options{HomeDir: home, Authenticator: auth.NewAuthenticator(nil), Servers: specs}
	}
	inst, err := Start(ctx, options(makeSpecs(port, true, true)))
	if err != nil {
		t.Fatal(err)
	}
	held, err := outbound.NewTrojan(outbound.TrojanOption{Name: "held-trojan", Server: "127.0.0.1", Port: port, Password: ts["password"], SNI: tp["sni"], Fingerprint: tm["fingerprint"]})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cctx, ccancel := context.WithTimeout(ctx, 8*time.Second)
	heldConn, err := held.DialContext(cctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
	ccancel()
	if err != nil {
		t.Fatal(err)
	}
	defer heldConn.Close()
	exchangeHeld := func() error {
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
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if err := inst.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if err := exchangeHeld(); err != nil {
		t.Fatalf("initial held Trojan: %v", err)
	}
	check := func(protocol, secret, sni, fingerprint string, targetPort int) error {
		cctx, ccancel := context.WithTimeout(ctx, 8*time.Second)
		defer ccancel()
		var client C.ProxyAdapter
		var e error
		if protocol == "trojan" {
			client, e = outbound.NewTrojan(outbound.TrojanOption{Name: protocol + "-client", Server: "127.0.0.1", Port: targetPort, Password: secret, SNI: sni, Fingerprint: fingerprint})
		} else {
			client, e = outbound.NewAnyTLS(outbound.AnyTLSOption{Name: "anytls", Server: "127.0.0.1", Port: targetPort, Password: secret, SNI: sni, Fingerprint: fingerprint, ClientFingerprint: "chrome", UDP: false})
		}
		if e != nil {
			return e
		}
		defer client.Close()
		conn, e := client.DialContext(cctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
		if e != nil {
			return e
		}
		defer conn.Close()
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
	if err := check("trojan", ts["password"], tp["sni"], tm["fingerprint"], port); err != nil {
		t.Fatalf("Trojan: %v", err)
	}
	if err := exchangeHeld(); err != nil {
		t.Fatalf("held Trojan before reload: %v", err)
	}
	if err := check("anytls", xs["password"], xp["sni"], xm["fingerprint"], port); err != nil {
		t.Fatalf("AnyTLS: %v", err)
	}
	if len(inst.Servers()) != 2 {
		t.Fatalf("server infos: %d", len(inst.Servers()))
	}
	if err := inst.Reconfigure(options(makeSpecs(movedPort, true, true))); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if err := check("trojan", ts["password"], tp["sni"], tm["fingerprint"], port); err != nil {
		t.Fatalf("Trojan after moving AnyTLS: %v", err)
	}
	if err := check("anytls", xs["password"], xp["sni"], xm["fingerprint"], movedPort); err != nil {
		t.Fatalf("AnyTLS after moving: %v", err)
	}
	if err := exchangeHeld(); err != nil {
		t.Fatalf("held Trojan after move: %v", err)
	}
	if err := inst.Reconfigure(options(makeSpecs(port, true, true))); err != nil {
		t.Fatalf("move AnyTLS back: %v", err)
	}
	if err := exchangeHeld(); err != nil {
		t.Fatalf("held Trojan after moving back: %v", err)
	}
	if err := check("trojan", ts["password"], tp["sni"], tm["fingerprint"], port); err != nil {
		t.Fatalf("Trojan after moving back: %v", err)
	}
	if err := check("anytls", xs["password"], xp["sni"], xm["fingerprint"], port); err != nil {
		t.Fatalf("AnyTLS after moving back: %v", err)
	}
	if err := inst.Reconfigure(options(makeSpecs(port, false, true))); err != nil {
		t.Fatalf("remove Trojan: %v", err)
	}
	if err := exchangeHeld(); err != nil {
		t.Fatalf("held Trojan after removal: %v", err)
	}
	if err := inst.Reconfigure(options(makeSpecs(port, true, true))); err != nil {
		t.Fatalf("restore: %v", err)
	}
	bad := makeSpecs(port, true, true)
	bad[0].Material["cert_pem"] = "invalid TLS material"
	if err := inst.Reconfigure(options(bad)); err == nil {
		t.Fatal("invalid TLS material reload succeeded")
	}
	if err := check("anytls", xs["password"], xp["sni"], xm["fingerprint"], port); err != nil {
		t.Fatalf("AnyTLS after rollback: %v", err)
	}
	if err := exchangeHeld(); err != nil {
		t.Fatalf("held Trojan after rollback: %v", err)
	}
	if err := check("trojan", ts["password"], tp["sni"], tm["fingerprint"], port); err != nil {
		t.Fatalf("Trojan after rollback: %v", err)
	}
}
