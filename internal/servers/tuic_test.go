package servers

import (
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	quic "github.com/metacubex/quic-go"
	"go.yaml.in/yaml/v3"
)

func tuicTestSpec(t *testing.T) Spec {
	t.Helper()
	p := tuicProtocol{}
	material, err := p.NewMaterial(nil)
	if err != nil {
		t.Fatal("material generation failed")
	}
	secret, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	return Spec{Name: "edge.test", Protocol: "tuic", Bind: "127.0.0.1", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{}, Material: SecretMap(material), Users: UserSecrets{"alice": secret}}
}

func TestTUICRegisteredSingleUserUDPBound(t *testing.T) {
	p, ok := Lookup("tuic")
	if !ok || p.Name() != "tuic" || !p.SingleUser() {
		t.Fatal("TUIC registration contract mismatch")
	}
	udp, ok := p.(interface{ UDPBound() bool })
	if !ok || !udp.UDPBound() {
		t.Fatal("TUIC must be UDP-bound")
	}
}

func TestTUICValidate(t *testing.T) {
	p := tuicProtocol{}
	valid := []map[string]string{
		nil, {}, {"public_host": "edge.example"}, {"sni": "edge.example"}, {"alpn": "h3,hq-29"},
		{"congestion": "bbr"}, {"congestion": "cubic"}, {"congestion": "new_reno"},
		{"udp_relay_mode": "native"}, {"udp_relay_mode": "quic"},
		{"cert_file": "cert.pem", "key_file": "key.pem"},
	}
	for _, params := range valid {
		if err := p.Validate(params); err != nil {
			t.Fatal("valid parameter case rejected")
		}
	}
	invalid := []map[string]string{
		{"unknown": "do-not-leak-this-value"}, {"congestion": "invalid"}, {"udp_relay_mode": "invalid"},
		{"public_host": "bad host"}, {"sni": "bad host"}, {"alpn": ""}, {"cert_file": "only-cert"},
		{"cert_file": "", "key_file": "key.pem"},
	}
	for _, params := range invalid {
		err := p.Validate(params)
		if err == nil {
			t.Fatal("invalid parameters accepted")
		}
		if strings.Contains(err.Error(), "do-not-leak-this-value") {
			t.Fatal("validation exposed parameter value")
		}
	}
}

func TestTUICSecretFormatAndUniqueness(t *testing.T) {
	p := tuicProtocol{}
	a, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	b, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	for _, secret := range []map[string]string{a, b} {
		if !validTUICSecret(secret) {
			t.Fatal("generated credential failed validation")
		}
		id, _ := hex.DecodeString(strings.ReplaceAll(secret["uuid"], "-", ""))
		if len(secret["uuid"]) != 36 || len(id) != 16 || id[6]>>4 != 4 || id[8]>>6 != 2 || len(secret["password"]) != 64 {
			t.Fatal("generated credential format invalid")
		}
		if _, err := hex.DecodeString(secret["password"]); err != nil {
			t.Fatal("generated password encoding invalid")
		}
	}
	if a["uuid"] == b["uuid"] || a["password"] == b["password"] {
		t.Fatal("generated credentials repeated")
	}
	for _, bad := range []string{"12345678123442349234123456789abc", "12345678-1234-5234-9234-123456789abc", "12345678-1234-4234-1234-123456789abc", "12345678_1234-4234-9234-123456789abc"} {
		if validTUICSecret(map[string]string{"uuid": bad, "password": "x"}) {
			t.Fatal("malformed UUID accepted")
		}
	}
}

func TestTUICNewMaterialCertificates(t *testing.T) {
	p := tuicProtocol{}
	cases := []struct {
		params map[string]string
		host   string
	}{
		{map[string]string{"public_host": "public.example", "sni": "sni.example"}, "public.example"},
		{map[string]string{"sni": "sni.example"}, "sni.example"},
		{nil, "localhost"},
	}
	for _, tc := range cases {
		material, err := p.NewMaterial(tc.params)
		if err != nil {
			t.Fatal("material generation failed")
		}
		block, _ := pem.Decode([]byte(material["cert_pem"]))
		if block == nil {
			t.Fatal("generated certificate PEM missing")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal("generated certificate invalid")
		}
		if err := cert.VerifyHostname(tc.host); err != nil {
			t.Fatal("certificate SAN does not match selected host")
		}
		if material["fingerprint"] == "" || TLSFingerprint(material) != material["fingerprint"] {
			t.Fatal("certificate fingerprint mismatch")
		}
	}
	params := map[string]string{"cert_file": filepath.Join(t.TempDir(), "absent-cert.pem"), "key_file": filepath.Join(t.TempDir(), "absent-key.pem")}
	if _, err := p.NewMaterial(params); err == nil || strings.Contains(err.Error(), filepath.Dir(params["cert_file"])) {
		t.Fatal("missing TLS files did not fail safely")
	}
}

func TestTUICLinkRoundTrip(t *testing.T) {
	p := tuicProtocol{}
	spec := tuicTestSpec(t)
	password := "space +%@: /"
	spec.Users["alice"]["password"] = password
	link, err := p.Link(spec, "alice", "2001:db8::7")
	if err != nil {
		t.Fatal("link generation failed")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal("link parse failed")
	}
	password, ok := parsed.User.Password()
	if parsed.Scheme != "tuic" || parsed.User.Username() != spec.Users["alice"]["uuid"] || !ok || password != spec.Users["alice"]["password"] || parsed.Host != "[2001:db8::7]:443" {
		t.Fatal("URI credential or endpoint round-trip failed")
	}
	for key, want := range map[string]string{"sni": "2001:db8::7", "alpn": "h3", "congestion_control": "bbr", "udp_relay_mode": "native", "allow_insecure": "0"} {
		if parsed.Query().Get(key) != want {
			t.Fatalf("URI option missing: %s", key)
		}
	}
	spec.Params = map[string]string{"sni": "tls.example", "alpn": "h3,hq-29", "congestion": "cubic", "udp_relay_mode": "quic"}
	link, err = p.Link(spec, "alice", "edge.example")
	if err != nil {
		t.Fatal("custom link generation failed")
	}
	parsed, err = url.Parse(link)
	if err != nil {
		t.Fatal("custom link parse failed")
	}
	for key, want := range map[string]string{"sni": "tls.example", "alpn": "h3,hq-29", "congestion_control": "cubic", "udp_relay_mode": "quic"} {
		if parsed.Query().Get(key) != want {
			t.Fatalf("custom URI option mismatch: %s", key)
		}
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 {
		t.Fatal("TUIC URI conversion failed")
	}
	proxyFromURI := converted[0]
	for key, want := range map[string]any{"type": "tuic", "server": "edge.example", "port": "443", "uuid": spec.Users["alice"]["uuid"], "password": spec.Users["alice"]["password"], "sni": "tls.example", "congestion-controller": "cubic", "udp-relay-mode": "quic", "udp": true} {
		if fmt.Sprint(proxyFromURI[key]) != fmt.Sprint(want) {
			t.Fatalf("converted field mismatch: %s", key)
		}
	}
	if got, ok := proxyFromURI["alpn"].([]string); !ok || strings.Join(got, ",") != "h3,hq-29" {
		t.Fatal("converted ALPN mismatch")
	}
	for _, key := range []string{"fingerprint", "skip-cert-verify", "certificate", "private-key"} {
		if _, ok := proxyFromURI[key]; ok {
			t.Fatalf("converter unexpectedly provided %s", key)
		}
	}
	if parsed.Query().Has("pcs") || parsed.Query().Has("pinSHA256") {
		t.Fatal("TUIC URI must not claim unsupported pin query")
	}
}

func TestTUICMihomoSnippetRoundTrip(t *testing.T) {
	p := tuicProtocol{}
	spec := tuicTestSpec(t)
	spec.Params = map[string]string{"sni": "tls.example", "alpn": "h3,hq-29", "congestion": "new_reno", "udp_relay_mode": "quic"}
	snippet, err := p.Mihomo(spec, "alice", "server.example")
	if err != nil {
		t.Fatal("Mihomo snippet generation failed")
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &document); err != nil || len(document.Proxies) != 1 {
		t.Fatal("Mihomo YAML failed to parse")
	}
	proxy := document.Proxies[0]
	for key, want := range map[string]any{"name": "edge.test", "type": "tuic", "server": "server.example", "port": 443, "uuid": spec.Users["alice"]["uuid"], "password": spec.Users["alice"]["password"], "sni": "tls.example", "congestion-controller": "new_reno", "udp-relay-mode": "quic", "fingerprint": TLSFingerprint(spec.Material), "skip-cert-verify": false, "udp": false} {
		if fmt.Sprint(proxy[key]) != fmt.Sprint(want) {
			t.Fatalf("Mihomo YAML field mismatch: %s", key)
		}
	}
	if got, ok := proxy["alpn"].([]any); !ok || len(got) != 2 || got[0] != "h3" || got[1] != "hq-29" {
		t.Fatal("Mihomo ALPN values did not round-trip")
	}
	defaults := tuicTestSpec(t)
	defaultSnippet, err := p.Mihomo(defaults, "alice", "default.example")
	if err != nil {
		t.Fatal("default Mihomo snippet generation failed")
	}
	if err := yaml.Unmarshal([]byte(defaultSnippet), &document); err != nil || len(document.Proxies) != 1 {
		t.Fatal("default Mihomo YAML failed to parse")
	}
	proxy = document.Proxies[0]
	for key, want := range map[string]any{"sni": "default.example", "congestion-controller": "bbr", "udp-relay-mode": "native", "udp": false} {
		if fmt.Sprint(proxy[key]) != fmt.Sprint(want) {
			t.Fatalf("default Mihomo YAML field mismatch: %s", key)
		}
	}
	if got, ok := proxy["alpn"].([]any); !ok || len(got) != 1 || got[0] != "h3" {
		t.Fatal("default Mihomo ALPN mismatch")
	}
}

func TestTUICAPIErrorsAndRedaction(t *testing.T) {
	p := tuicProtocol{}
	spec := tuicTestSpec(t)
	for _, key := range []string{"uuid", "password", "cert_pem", "key_pem", "fingerprint", "sni", "alpn"} {
		spec.Params[key] = "secret-value-" + key
		spec.Material[key] = "secret-material-" + key
	}
	for _, formatted := range []string{fmt.Sprintf("%v", spec), fmt.Sprintf("%+v", spec), fmt.Sprintf("%#v", spec)} {
		for _, secretValue := range []string{"secret-value-uuid", "secret-value-password", "secret-material-cert_pem", "secret-material-key_pem", "secret-material-fingerprint", "secret-value-sni", "secret-value-alpn"} {
			if strings.Contains(formatted, secretValue) {
				t.Fatal("Spec formatting exposed sensitive value")
			}
		}
	}
	spec = tuicTestSpec(t)
	secret := spec.Users["alice"]["password"]
	if _, err := p.Link(spec, "bob", "host.example"); err == nil {
		t.Fatal("wrong account accepted")
	}
	if _, err := p.Link(spec, "alice", ""); err == nil {
		t.Fatal("empty host accepted")
	}
	bad := spec.Clone()
	bad.Users["alice"]["uuid"] = "malformed"
	if _, err := p.Link(bad, "alice", "host.example"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("malformed secret error unsafe")
	}
	if _, err := p.Mihomo(spec, "bob", "host.example"); err == nil {
		t.Fatal("snippet accepted wrong account")
	}
	if _, err := p.Mihomo(spec, "alice", ""); err == nil {
		t.Fatal("snippet accepted empty host")
	}
	if _, err := p.Start(spec, Env{}); err == nil || !strings.Contains(err.Error(), "tunnel and listen config") || strings.Contains(err.Error(), secret) {
		t.Fatal("Start did not reject invalid environment safely")
	}
	startErr := tuicStartError{cause: fmt.Errorf("sensitive TLS detail: %w", net.ErrClosed)}
	for _, value := range []string{startErr.Error(), fmt.Sprint(startErr), fmt.Sprintf("%+v", startErr), fmt.Sprintf("%#v", startErr), startErr.String(), startErr.GoString()} {
		if strings.Contains(value, "sensitive TLS detail") {
			t.Fatal("start error leaked cause")
		}
	}
	if !errors.Is(startErr, net.ErrClosed) {
		t.Fatal("start error lost classification")
	}

}

type tuicRecordingTunnel struct {
	C.Tunnel
	tcp, udp *C.Metadata
}

func (r *tuicRecordingTunnel) HandleTCPConn(_ net.Conn, m *C.Metadata)      { r.tcp = m }
func (r *tuicRecordingTunnel) HandleUDPPacket(_ C.UDPPacket, m *C.Metadata) { r.udp = m }

func TestTUICScopedTunnelSetsLogin(t *testing.T) {
	r := &tuicRecordingTunnel{}
	scoped := tuicScopedTunnel{Tunnel: r, login: "alice"}
	tcp, udp := &C.Metadata{InUser: "wire-identity"}, &C.Metadata{InUser: "wire-identity"}
	scoped.HandleTCPConn(nil, tcp)
	scoped.HandleUDPPacket(nil, udp)
	if r.tcp != tcp || r.udp != udp || tcp.InUser != "alice" || udp.InUser != "alice" {
		t.Fatal("scoped tunnel did not preserve metadata pointers and set login")
	}
}

type tuicTestCloseFunc func() error

func (f tuicTestCloseFunc) Close() error { return f() }

type tuicTestPacket struct {
	net.PacketConn
	closes int
}

func (p *tuicTestPacket) Close() error { p.closes++; return nil }

func TestTUICCloserClosesOnce(t *testing.T) {
	serverCloses := 0
	pc := &tuicTestPacket{}
	c := tuicCloser{
		server: tuicTestCloseFunc(func() error { serverCloses++; return nil }),
		packet: pc, serveDone: make(chan error, 1),
	}
	c.serveDone <- quic.ErrServerClosed
	if err := c.Close(); err != nil {
		t.Fatal("first close failed")
	}
	if err := c.Close(); err != nil {
		t.Fatal("second close failed")
	}
	if serverCloses != 1 || pc.closes != 1 {
		t.Fatal("closer invoked underlying Close more than once")
	}
}
