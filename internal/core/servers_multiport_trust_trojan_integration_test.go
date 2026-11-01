//go:build integration

package core

import (
	"context"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/auth"
)

func TestMultiportTrustTunnelAndTrojanClients(t *testing.T) {
	backendIP, backendPort := multiportEchoTarget(t)
	trojanProtocol, ok := servers.Lookup("trojan")
	if !ok {
		t.Fatal("Trojan unavailable")
	}
	trustProtocol, ok := servers.Lookup("trusttunnel")
	if !ok {
		t.Fatal("TrustTunnel unavailable")
	}
	trojanParams := map[string]string{"public_host": "trojan.multiport.test", "sni": "trojan.multiport.test", "alpn": "h2"}
	trustParams := map[string]string{"public_host": "trust.multiport.test", "sni": "trust.multiport.test", "alpn": "h2"}
	trojanMaterial, err := trojanProtocol.NewMaterial(trojanParams)
	if err != nil {
		t.Fatal(err)
	}
	trustMaterial, err := trustProtocol.NewMaterial(trustParams)
	if err != nil {
		t.Fatal(err)
	}
	trojanSecret, err := trojanProtocol.NewSecret("alice", trojanParams)
	if err != nil {
		t.Fatal(err)
	}
	trustSecret, err := trustProtocol.NewSecret("bob", trustParams)
	if err != nil {
		t.Fatal(err)
	}
	port := servertest.FreePort(t)
	specs := []servers.Spec{
		{Name: "multiport-trojan", Protocol: "trojan", Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: trojanParams, Material: servers.SecretMap(trojanMaterial), Users: servers.UserSecrets{"alice": servers.SecretMap(trojanSecret)}},
		{Name: "multiport-trust", Protocol: "trusttunnel", Bind: "127.0.0.1", Port: port, Accounts: []string{"bob"}, Params: trustParams, Material: servers.SecretMap(trustMaterial), Users: servers.UserSecrets{"bob": servers.SecretMap(trustSecret)}},
	}
	servertest.TrustForTest(t, trojanMaterial)
	servertest.TrustForTest(t, trustMaterial)
	inst, err := Start(context.Background(), Options{HomeDir: t.TempDir(), Authenticator: auth.NewAuthenticator(nil), Servers: specs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		waitServerConnections(t, inst)
		if err := inst.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	trojanClient, err := outbound.NewTrojan(outbound.TrojanOption{Name: "multiport-trojan-client", Server: "127.0.0.1", Port: port, Password: trojanSecret["password"], SNI: trojanParams["sni"], Fingerprint: trojanMaterial["fingerprint"], ALPN: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer trojanClient.Close()
	t.Run("trojan", func(t *testing.T) { multiportRoundTrip(t, trojanClient, backendIP, backendPort) })
	trustClient, err := outbound.NewTrustTunnel(outbound.TrustTunnelOption{Name: "multiport-trust-client", Server: "127.0.0.1", Port: port, UserName: "bob", Password: trustSecret["password"], SNI: trustParams["sni"], Fingerprint: trustMaterial["fingerprint"], ALPN: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer trustClient.Close()
	t.Run("trusttunnel", func(t *testing.T) { multiportRoundTrip(t, trustClient, backendIP, backendPort) })
}
