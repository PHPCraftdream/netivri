package servers

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

func validVLESSSpec(t *testing.T) Spec {
	t.Helper()
	p := vlessReality{}
	material, err := p.NewMaterial(nil)
	if err != nil {
		t.Fatal("material generation failed")
	}
	secret, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	return Spec{
		Name: "vless-edge", Protocol: "vless-reality", Port: 443,
		Accounts: []string{"alice", "bob"}, Material: SecretMap(material),
		Users: UserSecrets{"alice": secret, "bob": {"uuid": "9ec25a83-95a7-44ab-84b4-bc0dc92b8134"}},
	}
}

func TestVLESSRealityGeneration(t *testing.T) {
	s := validVLESSSpec(t)
	raw, err := base64.RawURLEncoding.DecodeString(s.Material["private_key"])
	if err != nil || len(raw) != 32 {
		t.Fatal("invalid generated private key")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal("invalid X25519 key")
	}
	pub, err := base64.RawURLEncoding.DecodeString(s.Material["public_key"])
	if err != nil || len(pub) != 32 || !bytes.Equal(key.PublicKey().Bytes(), pub) {
		t.Fatal("generated public key does not match")
	}
	id, err := uuid.Parse(s.Users["alice"]["uuid"])
	if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 {
		t.Fatal("generated UUID is not v4")
	}
	ids := strings.Split(s.Material["short_ids"], ",")
	if len(ids) < 2 {
		t.Fatal("expected multiple short ids")
	}
	for _, id := range ids {
		rawID, err := hex.DecodeString(id)
		if err != nil || len(rawID) != 4 {
			t.Fatal("invalid generated short id")
		}
	}
}

func TestVLESSRealityParams(t *testing.T) {
	p := vlessReality{}
	cases := []struct {
		name   string
		params map[string]string
		valid  bool
	}{
		{"defaults", nil, true},
		{"valid parameters", map[string]string{"dest": "example.org:443", "sni": "example.org", "flow": defaultRealityFlow, "fingerprint": "chrome"}, true},
		{"IPv6 destination with DNS SNI", map[string]string{"dest": "[2001:db8::1]:443", "sni": "example.org"}, true},
		{"IP destination without SNI", map[string]string{"dest": "[2001:db8::1]:443"}, false},
		{"IP SNI", map[string]string{"sni": "2001:db8::1"}, false},
		{"zero port", map[string]string{"dest": "host:0"}, false},
		{"large port", map[string]string{"dest": "host:65536"}, false},
		{"malformed port", map[string]string{"dest": "host:bad"}, false},
		{"missing port", map[string]string{"dest": "host"}, false},
		{"empty DNS label", map[string]string{"dest": "bad..host:443"}, false},
		{"leading hyphen", map[string]string{"dest": "-bad.host:443"}, false},
		{"control character", map[string]string{"dest": "host\n:443"}, false},
		{"invalid SNI", map[string]string{"sni": "bad/name"}, false},
		{"empty SNI label", map[string]string{"sni": "bad..host"}, false},
		{"invalid flow", map[string]string{"flow": "bad"}, false},
		{"unsupported fingerprint", map[string]string{"fingerprint": "unsupported"}, false},
		{"android fingerprint", map[string]string{"fingerprint": "android"}, false},
		{"legacy fingerprint", map[string]string{"fingerprint": "chrome120"}, false},
		{"junk fingerprint", map[string]string{"fingerprint": "not-a-fingerprint"}, false},
		{"unknown secret parameter", map[string]string{"private_key": "secret"}, false},
	}
	for _, fp := range []string{"chrome", "firefox", "safari", "ios", "edge", "random", "randomized"} {
		if err := p.Validate(map[string]string{"fingerprint": fp}); err != nil {
			t.Errorf("accepted fingerprint %q: %v", fp, err)
		}
	}
	for _, fp := range []string{"android", "360", "qq", "chrome120", "firefox120", "safari16", "chrome_psk", "chrome_psk_shuffle", "chrome_padding_psk_shuffle", "chrome_pq", "chrome_pq_psk", "junk"} {
		err := p.Validate(map[string]string{"fingerprint": fp})
		if err == nil || !strings.Contains(err.Error(), "fingerprint") || strings.Contains(err.Error(), fp) {
			t.Errorf("fingerprint error did not safely identify key for %q: %v", fp, err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Validate(tc.params)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v", err == nil, tc.valid)
			}
		})
	}
	if realityDest(nil) != defaultRealityDest || realitySNI(nil) != "www.microsoft.com" {
		t.Fatal("default Reality endpoint mismatch")
	}
	material := validVLESSSpec(t).Material
	for _, shortID := range []string{"", "g1", "123", "0123456789abcdef00"} {
		badMaterial := SecretMap{
			"private_key": material["private_key"],
			"public_key":  material["public_key"],
			"short_ids":   shortID,
		}
		if validMaterial(badMaterial) {
			t.Errorf("accepted invalid short ID %q", shortID)
		}
	}
}

func TestVLESSRealityRegistry(t *testing.T) {
	protocol, ok := Lookup("vless-reality")
	if !ok || protocol.Name() != "vless-reality" || protocol.SingleUser() {
		t.Fatal("VLESS Reality registry entry mismatch")
	}
}

func TestVLESSRealityDefaultLink(t *testing.T) {
	s := validVLESSSpec(t)
	link, err := (vlessReality{}).Link(s, "alice", "edge.example.org")
	if err != nil {
		t.Fatal("default link generation failed")
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal("invalid default URI")
	}
	query := u.Query()
	if query.Get("sni") != "www.microsoft.com" || query.Get("fp") != defaultRealityFingerprint || query.Get("flow") != defaultRealityFlow || query.Get("encryption") != "none" || query.Get("security") != "reality" || query.Get("type") != "tcp" {
		t.Fatal("default URI parameters mismatch")
	}
}

func TestVLESSRealityLinkRoundTripAndMihomoYAML(t *testing.T) {
	s := validVLESSSpec(t)
	s.Params = map[string]string{"dest": "[2001:db8::1]:443", "sni": "reality.example.org", "fingerprint": "firefox"}
	for _, host := range []string{"edge.example.org", "2001:db8::7"} {
		link, err := (vlessReality{}).Link(s, "alice", host)
		if err != nil {
			t.Fatal("link generation failed")
		}
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal("invalid URI")
		}
		if u.Scheme != "vless" || u.User.Username() != s.Users["alice"]["uuid"] || u.Host != netJoin(host, "443") || u.Fragment != s.Name {
			t.Fatal("URI fields mismatch")
		}
		ids := strings.Split(s.Material["short_ids"], ",")
		for k, want := range map[string]string{
			"encryption": "none", "flow": defaultRealityFlow, "security": "reality",
			"sni": "reality.example.org", "fp": "firefox", "pbk": s.Material["public_key"], "sid": ids[0], "type": "tcp",
		} {
			if u.Query().Get(k) != want {
				t.Errorf("URI parameter %q mismatch", k)
			}
		}
		imported, err := convert.ConvertsV2Ray([]byte(link))
		if err != nil || len(imported) != 1 {
			t.Fatal("VLESS URI import failed")
		}
		importedProxy := imported[0]
		port, ok := importedProxy["port"].(string)
		if !ok || port != "443" {
			t.Fatalf("imported port has type %T, want string", importedProxy["port"])
		}
		for key, want := range map[string]any{
			"name": s.Name, "type": "vless", "server": host, "port": "443",
			"uuid": s.Users["alice"]["uuid"], "udp": true, "tls": true,
			"servername": "reality.example.org", "client-fingerprint": "firefox",
			"flow": defaultRealityFlow, "encryption": "none", "network": "tcp",
		} {
			if got, ok := importedProxy[key]; !ok || got != want {
				t.Errorf("imported field %q mismatch", key)
			}
		}
		importedReality, ok := importedProxy["reality-opts"].(map[string]any)
		if !ok || importedReality["public-key"] != s.Material["public_key"] || importedReality["short-id"] != ids[0] {
			t.Fatal("imported Reality fields mismatch")
		}
		text, err := (vlessReality{}).Mihomo(s, "alice", host)
		if err != nil {
			t.Fatal("Mihomo config generation failed")
		}
		var root struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(text), &root); err != nil || len(root.Proxies) != 1 {
			t.Fatal("invalid Mihomo YAML")
		}
		proxy := root.Proxies[0]
		if proxy["type"] != "vless" || proxy["name"] != s.Name || proxy["server"] != host || proxy["port"] != 443 || proxy["uuid"] != s.Users["alice"]["uuid"] || proxy["flow"] != defaultRealityFlow || proxy["tls"] != true || proxy["udp"] != false || proxy["servername"] != "reality.example.org" || proxy["client-fingerprint"] != "firefox" || proxy["network"] != "tcp" || proxy["encryption"] != "none" {
			t.Fatal("Mihomo fields mismatch")
		}
		reality, ok := proxy["reality-opts"].(map[string]any)
		if !ok || reality["public-key"] != s.Material["public_key"] || reality["short-id"] != ids[0] {
			t.Fatal("Mihomo Reality fields mismatch")
		}
	}
}

func netJoin(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

type countingListenConfig struct{ calls int }

func (c *countingListenConfig) Listen(context.Context, string, string) (net.Listener, error) {
	c.calls++
	return nil, errors.New("unexpected listen")
}
func (c *countingListenConfig) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	c.calls++
	return nil, errors.New("unexpected listen packet")
}

type emptyTunnel struct{}

func (emptyTunnel) HandleTCPConn(net.Conn, *C.Metadata)      {}
func (emptyTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (emptyTunnel) NatTable() C.NatTable                     { return nil }

func TestVLESSRealityValidationRedaction(t *testing.T) {
	s := validVLESSSpec(t)
	listen := &countingListenConfig{}
	env := Env{Tunnel: emptyTunnel{}, ListenConfig: listen}
	if _, err := (vlessReality{}).Link(s, "mallory", "host"); err == nil {
		t.Fatal("unconfigured account accepted")
	}
	bad := s.Clone()
	bad.Users["alice"]["uuid"] = "invalid-secret-uuid"
	if _, err := (vlessReality{}).Link(bad, "alice", "host"); err == nil || strings.Contains(err.Error(), "invalid-secret-uuid") {
		t.Fatal("invalid UUID or unsafe error")
	}
	bad = s.Clone()
	bad.Users["bob"]["uuid"] = bad.Users["alice"]["uuid"]
	if _, err := (vlessReality{}).Start(bad, env); err == nil {
		t.Fatal("duplicate UUID accepted")
	}
	bad = s.Clone()
	bad.Material["private_key"] = "invalid-material-secret"
	if _, err := (vlessReality{}).Start(bad, env); err == nil {
		t.Fatal("invalid material accepted")
	}
	if listen.calls != 0 {
		t.Fatal("invalid spec acquired a listener")
	}
	cause := errors.New("private-key-secret")
	err := safeVlessStartError{cause}
	if strings.Contains(err.Error(), "private-key-secret") || !errors.Is(err, cause) {
		t.Fatal("unsafe start error behavior")
	}
	for _, formatted := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(formatted, "private-key-secret") {
			t.Fatal("formatted start error exposed cause")
		}
	}
}
