package servers

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	mihomoyaml "github.com/metacubex/mihomo/common/yaml"
)

func TestShadowquicValidation(t *testing.T) {
	p := shadowquic{}
	for _, tc := range []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"valid", map[string]string{"sni": "node.example", "public_host": "public.example", "alpn": "h3,hq-interop", "quic_versions": "v1", "max_idle_time": "30000", "max_datagram_frame_size": "1400", "recv_window_conn": "1000", "recv_window": "2000", "disable_mtu_discovery": "true", "congestion_controller": "bbr", "cert_file": "cert.pem", "key_file": "key.pem"}, ""},
		{"valid cubic controller", map[string]string{"congestion_controller": "cubic"}, ""},
		{"valid new reno controller", map[string]string{"congestion_controller": "new_reno"}, ""},
		{"invalid controller", map[string]string{"congestion_controller": "reno"}, "invalid congestion_controller"},
		{"empty controller", map[string]string{"congestion_controller": ""}, "invalid congestion_controller"},
		{"invalid idle time", map[string]string{"max_idle_time": "0"}, "invalid max_idle_time"},
		{"invalid idle time empty", map[string]string{"max_idle_time": ""}, "invalid max_idle_time"},
		{"invalid datagram size", map[string]string{"max_datagram_frame_size": "0"}, "invalid max_datagram_frame_size"},
		{"invalid stream window", map[string]string{"recv_window_conn": "0"}, "invalid recv_window_conn"},
		{"invalid connection window", map[string]string{"recv_window": "0"}, "invalid recv_window"},
		{"invalid MTU flag", map[string]string{"disable_mtu_discovery": "yes"}, "invalid disable_mtu_discovery"},
		{"invalid ALPN", map[string]string{"alpn": "h3, hq"}, "invalid alpn"},
		{"invalid QUIC versions", map[string]string{"quic_versions": "not-a-version"}, "invalid quic_versions"},
		{"invalid SNI", map[string]string{"sni": "bad host"}, "invalid sni"},
		{"invalid public host", map[string]string{"public_host": "bad host"}, "invalid public_host"},
		{"unknown key", map[string]string{"private-key": "must-not-leak"}, `unknown parameter "private-key"`},
		{"unknown old parameter", map[string]string{"up": "100"}, `unknown parameter "up"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Validate(tc.params)
			if tc.want == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.want != "" && (err == nil || err.Error() != tc.want || strings.Contains(err.Error(), "must-not-leak")) {
				t.Fatalf("unexpected safe validation result: %v", err)
			}
		})
	}
}

func TestShadowquicMaterialDefaultsAndTLSFiles(t *testing.T) {
	p := shadowquic{}
	for _, tc := range []struct {
		name   string
		params map[string]string
		host   string
		ips    []string
	}{
		{"public host", map[string]string{"public_host": "public.example", "sni": "other.example"}, "public.example", nil},
		{"sni fallback", map[string]string{"sni": "sni.example"}, "sni.example", nil},
		{"localhost default", nil, "localhost", []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := p.NewMaterial(tc.params)
			if err != nil {
				t.Fatal("NewMaterial failed")
			}
			certPEM, keyPEM := TLSFiles(m)
			if certPEM == "" || keyPEM == "" {
				t.Fatal("TLSFiles returned empty material")
			}
			block, _ := pem.Decode([]byte(certPEM))
			if block == nil {
				t.Fatal("invalid certificate PEM")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal("invalid certificate")
			}
			if cert.Subject.CommonName != tc.host {
				t.Fatalf("certificate host mismatch: %q", cert.Subject.CommonName)
			}
			if tc.host != "localhost" && (len(cert.DNSNames) != 1 || cert.DNSNames[0] != tc.host) {
				t.Fatal("certificate DNS SAN mismatch")
			}
			for _, ip := range tc.ips {
				found := false
				for _, got := range cert.IPAddresses {
					if got.Equal(net.ParseIP(ip)) {
						found = true
					}
				}
				if !found {
					t.Fatal("certificate IP SAN missing")
				}
			}
		})
	}
}

func TestShadowquicSecretsAndSpecRedaction(t *testing.T) {
	p := shadowquic{}
	first, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("NewSecret failed")
	}
	second, err := p.NewSecret("bob", nil)
	if err != nil {
		t.Fatal("NewSecret failed")
	}
	for _, secret := range []map[string]string{first, second} {
		raw, err := base64.RawURLEncoding.DecodeString(secret["password"])
		if err != nil || len(raw) != 32 {
			t.Fatal("generated password is not a 32-byte secret")
		}
	}
	if first["password"] == second["password"] {
		t.Fatal("generated secrets are not unique")
	}
	spec := Spec{Name: "edge", Protocol: "shadowquic", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"password": "hidden-param"}, Material: SecretMap{"key_pem": "hidden-material"}, Users: UserSecrets{"alice": first}}
	for _, got := range []string{fmt.Sprint(spec), fmt.Sprintf("%v", spec), fmt.Sprintf("%+v", spec), fmt.Sprintf("%#v", spec)} {
		for _, secret := range []string{first["password"], "hidden-param", "hidden-material"} {
			if strings.Contains(got, secret) {
				t.Fatal("Spec formatting leaked a secret")
			}
		}
	}
}

func TestShadowquicStartErrorIsSafe(t *testing.T) {
	err := shadowquicStartError{cause: fmt.Errorf("bind failed with credential hidden-value: %w", syscall.EADDRINUSE)}
	for _, got := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(got, "hidden-value") {
			t.Fatal("start error leaked its cause")
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal("start error does not preserve errors.Is")
	}
}

func TestShadowquicMihomoSnippetRoundTrip(t *testing.T) {
	p := shadowquic{}
	secret, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("NewSecret failed")
	}
	material, err := p.NewMaterial(map[string]string{"public_host": "edge.example.org"})
	if err != nil {
		t.Fatal("NewMaterial failed")
	}
	spec := Spec{Name: "edge", Protocol: "shadowquic", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"sni": "origin.example", "alpn": "h3,hq-interop", "quic_versions": "v1"}, Material: SecretMap(material), Users: UserSecrets{"alice": secret}}
	snippet, err := p.Mihomo(spec, "alice", "edge.example.org")
	if err != nil {
		t.Fatal("Mihomo generation failed")
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := mihomoyaml.Unmarshal([]byte(snippet), &document); err != nil || len(document.Proxies) != 1 {
		t.Fatal("snippet YAML could not be parsed")
	}
	mapping := document.Proxies[0]
	proxy, err := adapter.ParseProxy(mapping)
	if err != nil {
		t.Fatal("Mihomo rejected generated ShadowQUIC proxy")
	}
	t.Cleanup(func() { _ = proxy.Close() })
	if mapping["type"] != "shadowquic" || mapping["name"] != "edge" || mapping["server"] != "edge.example.org" || mapping["port"] != 443 || mapping["username"] != "alice" || mapping["password"] != secret["password"] || mapping["sni"] != "origin.example" || mapping["alpn"] == nil || mapping["quic-versions"] == nil || mapping["udp"] != false {
		t.Fatal("generated Mihomo fields differ")
	}
	if fmt.Sprint(mapping["alpn"]) != "[h3 hq-interop]" || fmt.Sprint(mapping["quic-versions"]) != "[v1]" {
		t.Fatal("generated ALPN or QUIC versions differ")
	}
	result, err := subscription.ParseDetailed([]byte(snippet))
	if err != nil || len(result.Nodes) != 1 || result.Nodes[0].Type != "shadowquic" {
		t.Fatal("subscription parser did not accept the generated snippet")
	}
	if fmt.Sprint(result.Nodes[0].Raw["alpn"]) != "[h3 hq-interop]" || fmt.Sprint(result.Nodes[0].Raw["quic-versions"]) != "[v1]" {
		t.Fatal("subscription parser did not retain ALPN or QUIC versions")
	}
	if proxies, err := convert.ConvertsV2Ray([]byte("shadowquic://user:password@localhost:443#edge")); err == nil || len(proxies) != 0 {
		t.Fatal("V2Ray converter unexpectedly accepted unsupported ShadowQUIC URI")
	}
}

func TestShadowquicUDPBoundMultiuser(t *testing.T) {
	p, ok := Lookup("shadowquic")
	if !ok || p.SingleUser() {
		t.Fatal("ShadowQUIC registration mismatch")
	}
	transport, ok := p.(UDPBound)
	if !ok || !transport.UDPBound() {
		t.Fatal("ShadowQUIC must be UDP-bound")
	}
}
