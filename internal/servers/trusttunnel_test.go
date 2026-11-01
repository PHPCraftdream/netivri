package servers

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"go.yaml.in/yaml/v3"
)

func TestTrustTunnelValidationAndSecrets(t *testing.T) {
	p := trusttunnelProtocol{}
	for _, params := range []map[string]string{nil, {"network": "tcp"}, {"alpn": "h2"}, {"udp": "false", "quic": "false"}} {
		if err := p.Validate(params); err != nil {
			t.Fatalf("valid params rejected: %v", err)
		}
	}
	for _, params := range []map[string]string{{"unknown": "private"}, {"network": "udp"}, {"alpn": "h3"}, {"udp": "true"}, {"quic": "true"}, {"cert_file": "private-path"}, {"sni": "bad/name"}} {
		if err := p.Validate(params); err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "private-path") {
			t.Fatalf("invalid params accepted or leaked: %v", err)
		}
	}
	if _, err := p.NewSecret("alice", map[string]string{"udp": "true"}); err == nil {
		t.Fatal("NewSecret accepted unsupported UDP")
	}
	first, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []map[string]string{first, second} {
		raw, err := base64.RawURLEncoding.DecodeString(secret["password"])
		if err != nil || len(raw) != trusttunnelSecretBytes {
			t.Fatalf("invalid secret encoding/size: %v", err)
		}
	}
	if first["password"] == second["password"] {
		t.Fatal("generated secrets not unique")
	}
}

func TestTrustTunnelSnippetAndImporter(t *testing.T) {
	p := trusttunnelProtocol{}
	material, err := p.NewMaterial(map[string]string{"public_host": "example.org", "sni": "origin.example"})
	if err != nil {
		t.Fatal("material generation failed")
	}
	secret, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	spec := Spec{Name: "edge", Protocol: "trusttunnel", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"sni": "origin.example"}, Material: SecretMap(material), Users: UserSecrets{"alice": secret}}
	if _, err := p.Link(spec, "alice", "example.org"); err == nil || !strings.Contains(err.Error(), "standard URI") {
		t.Fatalf("Link error: %v", err)
	}
	for _, host := range []string{"example.org", "2001:db8::1"} {
		snippet, err := p.Mihomo(spec, "alice", host)
		if err != nil {
			t.Fatalf("snippet for %q: %v", host, err)
		}
		var document struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &document); err != nil || len(document.Proxies) != 1 {
			t.Fatalf("invalid snippet YAML: %v", err)
		}
		fields := document.Proxies[0]
		if fields["name"] != "edge" || fields["type"] != "trusttunnel" || fields["server"] != host || fields["port"] != 443 || fields["username"] != "alice" || fields["password"] != secret["password"] || fields["sni"] != "origin.example" || fields["fingerprint"] != material["fingerprint"] || fields["udp"] != false || fields["quic"] != false {
			t.Fatal("snippet fields mismatch")
		}
		proxy, err := adapter.ParseProxy(fields)
		if err != nil {
			t.Fatalf("adapter parser: %v", err)
		}
		_ = proxy.Close()
	}
	for _, host := range []string{"", "bad/host"} {
		if _, err := p.Mihomo(spec, "alice", host); err == nil {
			t.Fatalf("invalid host %q accepted", host)
		}
	}
	converted, err := convert.ConvertsV2Ray([]byte("trusttunnel://alice:pass@example.org:443#edge"))
	if err == nil || len(converted) != 0 {
		t.Fatal("TrustTunnel URI is unsupported by the Mihomo converter")
	}
	bad := spec.Clone()
	bad.Params = map[string]string{"unknown": "sensitive"}
	if err := bad.Validate(); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("validation exposed value: %v", err)
	}
	formatted := fmt.Sprintf("%v %+v %#v", spec, spec, spec)
	if strings.Contains(formatted, secret["password"]) || strings.Contains(formatted, material["key_pem"]) {
		t.Fatal("Spec formatting exposed secrets")
	}
}

func TestTrustTunnelTLSHostPrecedence(t *testing.T) {
	p := trusttunnelProtocol{}
	for _, tc := range []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"public_host", map[string]string{"public_host": "public.example", "sni": "client.example"}, "public.example"},
		{"sni fallback", map[string]string{"sni": "sni.example"}, "sni.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			material, err := p.NewMaterial(tc.params)
			if err != nil {
				t.Fatal("material generation failed")
			}
			block, _ := pem.Decode([]byte(material["cert_pem"]))
			if block == nil {
				t.Fatal("certificate PEM missing")
			}
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != tc.want {
				t.Fatalf("certificate SAN did not select %q", tc.want)
			}
		})
	}
}

func TestTrustTunnelSecretAndStartErrorSafety(t *testing.T) {
	for _, value := range []string{"", "bad", "AAAA"} {
		if trusttunnelSecretValid(value) {
			t.Fatalf("invalid secret accepted: %q", value)
		}
	}
	err := trusttunnelSafeStartError{cause: fmt.Errorf("private password: %w", syscall.EADDRINUSE)}
	for _, value := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(value, "private password") {
			t.Fatal("start error leaked cause")
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal("safe error lost classification")
	}
}
