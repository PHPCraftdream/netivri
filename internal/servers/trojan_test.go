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
	"testing"

	"github.com/metacubex/mihomo/common/convert"
	"go.yaml.in/yaml/v3"
)

func TestTrojanStartRejectsDuplicatePasswordsBeforeBinding(t *testing.T) {
	password := "same-secret"
	spec := Spec{
		Name: "edge", Protocol: "trojan", Port: 443, Accounts: []string{"alice", "bob"},
		Params:   map[string]string{},
		Material: SecretMap{"cert_pem": "certificate", "key_pem": "private key"},
		Users:    UserSecrets{"alice": {"password": password}, "bob": {"password": password}},
	}
	if _, err := (trojanServer{}).Start(spec, Env{}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatal("duplicate account passwords were not rejected before bind")
	}
}

func TestTrojanStartErrorRedacts(t *testing.T) {
	err := trojanStartError{cause: fmt.Errorf("secret payload: %w", net.ErrClosed)}
	for _, value := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(value, "secret payload") {
			t.Fatal("start error leaked cause")
		}
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatal("start error lost classification")
	}
}

func TestTrojanValidationAndMaterial(t *testing.T) {
	p := trojanServer{}
	for _, key := range []string{"fallback", "unknown", "udp"} {
		err := p.Validate(map[string]string{key: "sensitive"})
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Errorf("%s validation failed safely", key)
		}
	}
	for _, params := range []map[string]string{{"cert_file": "x"}, {"sni": "bad/name"}, {"alpn": ""}, {"public_host": "bad/name"}} {
		if err := p.Validate(params); err == nil {
			t.Errorf("invalid parameters accepted")
		}
	}
	material, err := p.NewMaterial(map[string]string{"sni": "example.test"})
	if err != nil {
		t.Fatal("material generation failed")
	}
	block, _ := pem.Decode([]byte(material["cert_pem"]))
	if block == nil {
		t.Fatal("certificate PEM missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) != 1 || cert.DNSNames[0] != "example.test" {
		t.Fatal("certificate SAN mismatch")
	}
	if material["fingerprint"] != TLSFingerprint(material) {
		t.Fatal("fingerprint mismatch")
	}
}
func TestTrojanHostileALPNIsQuoted(t *testing.T) {
	p := trojanServer{}
	material, err := p.NewMaterial(nil)
	if err != nil {
		t.Fatal("material generation failed")
	}
	spec := Spec{Name: "edge", Protocol: "trojan", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{"alpn": "a]b,quote\\\"x"}, Material: SecretMap(material), Users: UserSecrets{"alice": {"password": "sample"}}}
	snippet, err := p.Mihomo(spec, "alice", "localhost")
	if err != nil {
		t.Fatal("snippet generation failed")
	}
	var parsed struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &parsed); err != nil || len(parsed.Proxies) != 1 {
		t.Fatal("hostile ALPN snippet did not parse")
	}
	alpn, ok := parsed.Proxies[0]["alpn"].([]any)
	if !ok || len(alpn) != 2 || alpn[0] != "a]b" || alpn[1] != "quote\\\"x" {
		t.Fatal("hostile ALPN values changed")
	}
}

func TestTrojanSecretLinksSnippetAndRedaction(t *testing.T) {
	p := trojanServer{}
	a, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	b, _ := p.NewSecret("alice", nil)
	decoded, err := base64.RawURLEncoding.DecodeString(a["password"])
	if err != nil || len(decoded) != 32 || a["password"] == b["password"] {
		t.Fatal("invalid generated secret")
	}
	material, err := p.NewMaterial(map[string]string{"public_host": "example.test"})
	if err != nil {
		t.Fatal("material generation failed")
	}
	specialPassword := "space +%@:secret"
	s := Spec{Name: "edge", Protocol: "trojan", Port: 443, Accounts: []string{"alice", "bob"}, Params: map[string]string{"sni": "origin.test", "alpn": "h2,http/1.1"}, Material: SecretMap(material), Users: UserSecrets{"alice": {"password": specialPassword}, "bob": {"password": "bob-password"}}}
	link, err := p.Link(s, "alice", "2001:db8::1")
	if err != nil {
		t.Fatal("link generation failed")
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal("link parse failed")
	}
	password := u.User.Username()
	q := u.Query()
	if password != specialPassword || u.Host != "[2001:db8::1]:443" || u.Fragment != "edge" || q.Get("security") != "tls" || q.Get("sni") != "origin.test" || q.Get("type") != "tcp" || q.Get("allowInsecure") != "0" || q.Get("pcs") != TLSFingerprint(material) || q.Has("pinSHA256") || q.Get("alpn") != "h2,http/1.1" {
		t.Fatal("URI round trip mismatch")
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 {
		t.Fatal("Mihomo URI conversion failed")
	}
	proxyFromURI := converted[0]
	if proxyFromURI["type"] != "trojan" || proxyFromURI["server"] != "2001:db8::1" || proxyFromURI["port"] != "443" || proxyFromURI["password"] != specialPassword || proxyFromURI["sni"] != "origin.test" || proxyFromURI["fingerprint"] != TLSFingerprint(material) || proxyFromURI["skip-cert-verify"] != false || proxyFromURI["network"] != "tcp" || proxyFromURI["udp"] != true {
		t.Fatal("Mihomo URI conversion fields mismatch")
	}
	if alpn, ok := proxyFromURI["alpn"].([]string); !ok || len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatal("Mihomo URI conversion ALPN mismatch")
	}
	snippet, err := p.Mihomo(s, "alice", "2001:db8::1")
	if err != nil {
		t.Fatal("snippet generation failed")
	}
	var parsed struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &parsed); err != nil || len(parsed.Proxies) != 1 {
		t.Fatal("Mihomo snippet parse failed")
	}
	proxy := parsed.Proxies[0]
	if proxy["name"] != "edge" || proxy["server"] != "2001:db8::1" || proxy["port"] != 443 || proxy["type"] != "trojan" || proxy["password"] != specialPassword || proxy["sni"] != "origin.test" || proxy["fingerprint"] != TLSFingerprint(material) || proxy["skip-cert-verify"] != false || proxy["udp"] != false {
		t.Fatal("Mihomo snippet fields mismatch")
	}
	if alpn, ok := proxy["alpn"].([]any); !ok || len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatal("Mihomo ALPN values mismatch")
	}
	for _, v := range []string{fmt.Sprint(s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s)} {
		if strings.Contains(v, specialPassword) || strings.Contains(v, "bob-password") || strings.Contains(v, material["key_pem"]) || strings.Contains(v, material["cert_pem"]) || strings.Contains(v, material["fingerprint"]) || strings.Contains(v, "origin.test") || strings.Contains(v, "h2,http/1.1") {
			t.Fatal("Spec format leaked secret or parameter values")
		}
	}
	if got := net.JoinHostPort("2001:db8::1", "443"); got != "[2001:db8::1]:443" {
		t.Fatal("IPv6 formatting failed")
	}
}
