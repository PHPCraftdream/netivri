package servers

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/transport/anytls/padding"
	"go.yaml.in/yaml/v3"
)

func TestAnyTLSValidation(t *testing.T) {
	validPadding := "stop=0\n0=30-30\n1=100-400,c"
	for _, tc := range []struct {
		name   string
		params map[string]string
		valid  bool
	}{
		{"empty", nil, true}, {"empty padding default", map[string]string{"padding_scheme": ""}, true},
		{"valid padding", map[string]string{"padding_scheme": validPadding}, true},
		{"vendor default padding", map[string]string{"padding_scheme": string(padding.DefaultPaddingScheme)}, true},
		{"first packet checkmark", map[string]string{"padding_scheme": "stop=1\n0=c"}, false},
		{"first packet trailing checkmark", map[string]string{"padding_scheme": "stop=1\n0=30-30,c"}, true},
		{"later checkmark", map[string]string{"padding_scheme": "stop=2\n0=30-30\n1=100-400"}, true},
		{"first packet zero range", map[string]string{"padding_scheme": "stop=1\n0=0-0"}, false},
		{"stop max", map[string]string{"padding_scheme": "stop=1000"}, true},
		{"stop too large", map[string]string{"padding_scheme": "stop=1001"}, false},
		{"payload too large", map[string]string{"padding_scheme": "stop=1\n0=65536-70000"}, false},
		{"sign", map[string]string{"padding_scheme": "stop=+1"}, false},
		{"invalid chars", map[string]string{"padding_scheme": "stop=1\n0=1-2;boom"}, false},
		{"malformed line", map[string]string{"padding_scheme": "stop=1\n0=1"}, false},
		{"duplicate key", map[string]string{"padding_scheme": "stop=1\n0=1-2\n0=3-4"}, false},
		{"noncanonical key", map[string]string{"padding_scheme": "stop=1\n00=1-2"}, false},
		{"unknown", map[string]string{"other": "do-not-echo"}, false},
		{"unsupported alpn", map[string]string{"alpn": "h2"}, false},
		{"tls pair", map[string]string{"cert_file": "cert", "key_file": "key"}, true},
		{"tls unpaired", map[string]string{"cert_file": "cert"}, false},
		{"invalid public host", map[string]string{"public_host": "bad/host"}, false},
		{"empty public host metadata", map[string]string{"public_host": ""}, true},
		{"invalid sni", map[string]string{"sni": "bad/name"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (anyTLS{}).Validate(tc.params)
			if (err == nil) != tc.valid {
				t.Fatal("unexpected validation result")
			}
			if err != nil && (strings.Contains(err.Error(), "do-not-echo") || strings.Contains(err.Error(), "boom")) {
				t.Fatal("validation error disclosed parameter value")
			}
		})
	}
	if err := (anyTLS{}).Validate(map[string]string{"padding_scheme": "stop=1\n0=c"}); err == nil || !strings.Contains(err.Error(), "padding_scheme") || strings.Contains(err.Error(), "stop=1\n0=c") {
		t.Fatal("invalid padding error did not identify parameter safely")
	}
	if (anyTLS{}).SingleUser() {
		t.Fatal("AnyTLS unexpectedly requires a single account")
	}
	if _, ok := Lookup("anytls"); !ok {
		t.Fatal("AnyTLS registry entry missing")
	}
}

func TestAnyTLSNewSecret(t *testing.T) {
	first, err := (anyTLS{}).NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	second, err := (anyTLS{}).NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first["password"])
	if err != nil || len(decoded) != anyTLSMinPasswordBytes || first["password"] == second["password"] {
		t.Fatal("generated secret shape or uniqueness differs")
	}
}

func TestAnyTLSNewMaterialHostSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]string
		dns    string
		ip     string
	}{
		{"public host priority", map[string]string{"public_host": "public.example.test", "sni": "sni.example.test"}, "public.example.test", ""},
		{"empty public host falls back", map[string]string{"public_host": "", "sni": "sni.example.test"}, "sni.example.test", ""},
		{"sni fallback", map[string]string{"sni": "sni.example.test"}, "sni.example.test", ""},
		{"default host", nil, "localhost", "127.0.0.1"},
		{"explicit IP", map[string]string{"public_host": "192.0.2.7"}, "", "192.0.2.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			material, err := (anyTLS{}).NewMaterial(tc.params)
			if err != nil {
				t.Fatal("material generation failed")
			}
			block, _ := pem.Decode([]byte(material[tlsCertKey]))
			if block == nil {
				t.Fatal("generated PEM missing")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal("generated certificate invalid")
			}
			if tc.dns != "" && (len(cert.DNSNames) != 1 || cert.DNSNames[0] != tc.dns) {
				t.Fatal("generated DNS SAN differs")
			}
			if tc.ip != "" && (len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP(tc.ip)) || cert.VerifyHostname(tc.ip) != nil) {
				t.Fatal("generated IP SAN differs")
			}
		})
	}
	if _, err := (anyTLS{}).NewMaterial(map[string]string{"public_host": "bad/host"}); err == nil || !strings.Contains(err.Error(), "public_host") {
		t.Fatal("invalid public host was not rejected")
	}
}

func TestAnyTLSLink(t *testing.T) {
	protocol := anyTLS{}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "entry.example.test", "sni": "tls.example.test"})
	if err != nil {
		t.Fatal("material generation failed")
	}
	secret, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	material[tlsFingerprintKey] = strings.Repeat("stale", 3)
	spec := Spec{Name: "edge-one", Protocol: "anytls", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"sni": "tls.example.test"}, Material: SecretMap(material), Users: UserSecrets{"alice": secret}}
	rejected := spec.Clone()
	rejected.Params["padding_scheme"] = "stop=1\n0=c"
	if output, err := protocol.Link(rejected, "alice", "entry.example.test"); err == nil || output != "" || !strings.Contains(err.Error(), "padding_scheme") || strings.Contains(err.Error(), "stop=1") {
		t.Fatal("Link did not safely reject invalid padding")
	}
	wantFingerprint := TLSFingerprint(material)
	for _, host := range []string{"entry.example.test", "127.0.0.1", "2001:db8::5"} {
		link, err := protocol.Link(spec, "alice", host)
		if err != nil {
			t.Fatal("link generation failed")
		}
		parsed, err := url.Parse(link)
		if err != nil {
			t.Fatal("link parse failed")
		}
		password, hasPassword := parsed.User.Password()
		if parsed.Scheme != "anytls" || hasPassword || parsed.User.Username() != secret["password"] || password != "" || parsed.Host != net.JoinHostPort(host, "443") || parsed.Query().Get("sni") != "tls.example.test" || parsed.Query().Get("insecure") != "0" || parsed.Query().Get("hpkp") != wantFingerprint || parsed.Fragment != spec.Name {
			t.Fatal("link round-trip differs")
		}
	}
	for _, bad := range []struct {
		name string
		spec Spec
		user string
		host string
	}{
		{"wrong protocol", Spec{Protocol: "shadowsocks"}, "alice", "host"},
		{"nonmember", spec, "bob", "host"},
		{"no host", spec, "alice", ""},
		{"missing secret", func() Spec { s := spec.Clone(); delete(s.Users["alice"], "password"); return s }(), "alice", "host"},
		{"missing material", func() Spec { s := spec.Clone(); s.Material = nil; return s }(), "alice", "host"},
	} {
		if _, err := protocol.Link(bad.spec, bad.user, bad.host); err == nil {
			t.Errorf("%s accepted", bad.name)
		}
	}
}

func TestAnyTLSMihomoSnippet(t *testing.T) {
	protocol := anyTLS{}
	material, err := protocol.NewMaterial(map[string]string{"public_host": "entry.example.test"})
	if err != nil {
		t.Fatal("material generation failed")
	}
	secret, err := protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	spec := Spec{Name: "edge-one", Protocol: "anytls", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"sni": "entry.example.test"}, Material: SecretMap(material), Users: UserSecrets{"alice": secret}}
	rejected := spec.Clone()
	rejected.Params["padding_scheme"] = "stop=1\n0=c"
	if output, err := protocol.Mihomo(rejected, "alice", "entry.example.test"); err == nil || output != "" || !strings.Contains(err.Error(), "padding_scheme") || strings.Contains(err.Error(), "stop=1") {
		t.Fatal("Mihomo did not safely reject invalid padding")
	}
	snippet, err := protocol.Mihomo(spec, "alice", "entry.example.test")
	if err != nil {
		t.Fatal("snippet generation failed")
	}
	var decoded struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &decoded); err != nil || len(decoded.Proxies) != 1 {
		t.Fatal("snippet YAML invalid")
	}
	proxy := decoded.Proxies[0]
	if proxy["name"] != spec.Name || proxy["type"] != "anytls" || proxy["server"] != "entry.example.test" || proxy["port"] != 443 || proxy["password"] != secret["password"] || proxy["sni"] != "entry.example.test" || proxy["fingerprint"] != TLSFingerprint(material) || proxy["client-fingerprint"] != "chrome" || proxy["idle-session-check-interval"] != 30 || proxy["skip-cert-verify"] != false || proxy["udp"] != false {
		t.Fatal("snippet fields differ")
	}
	for _, bad := range []struct {
		name string
		spec Spec
		user string
		host string
	}{
		{"wrong protocol", Spec{Protocol: "shadowsocks"}, "alice", "host"},
		{"nonmember", spec, "bob", "host"},
		{"no host", spec, "alice", ""},
		{"missing secret", func() Spec { s := spec.Clone(); delete(s.Users["alice"], "password"); return s }(), "alice", "host"},
		{"missing material", func() Spec { s := spec.Clone(); s.Material = nil; return s }(), "alice", "host"},
	} {
		if _, err := protocol.Mihomo(bad.spec, bad.user, bad.host); err == nil {
			t.Errorf("%s accepted", bad.name)
		}
	}
}

func TestSafeAnyTLSStartErrorRedactsAndPreservesIs(t *testing.T) {
	err := safeAnyTLSStartError{cause: fmt.Errorf("bind exposed-secret: %w", syscall.EADDRINUSE)}
	for _, rendered := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(rendered, "exposed-secret") {
			t.Fatal("listener error disclosed cause")
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal("listener error lost cause classification")
	}
}

func TestAnyTLSSpecRedaction(t *testing.T) {
	spec := Spec{Name: "edge", Protocol: "anytls", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"padding_scheme": "private-padding"}, Material: SecretMap{"private": "private-material"}, Users: UserSecrets{"alice": {"password": "private-password"}}}
	for _, rendered := range []string{fmt.Sprint(spec), fmt.Sprintf("%+v", spec), fmt.Sprintf("%#v", spec)} {
		if strings.Contains(rendered, "private-padding") || strings.Contains(rendered, "private-password") || strings.Contains(rendered, "private-material") {
			t.Fatal("spec formatting disclosed secret")
		}
	}
}
