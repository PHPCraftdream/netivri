package servers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/common/convert"
	"go.yaml.in/yaml/v3"
)

func TestHysteria2RegisteredUDP(t *testing.T) {
	p, ok := Lookup("hysteria2")
	if !ok || p.Name() != "hysteria2" || p.SingleUser() {
		t.Fatal("Hysteria2 registration mismatch")
	}
	u, ok := p.(UDPBound)
	if !ok || !u.UDPBound() {
		t.Fatal("must bind UDP")
	}
}

func TestHysteria2Validation(t *testing.T) {
	p := hysteria2{}
	limit := uint64(math.MaxInt64 / 1_000_000)
	for _, value := range []string{"1", "10", "1000", fmt.Sprint(limit)} {
		if err := p.Validate(map[string]string{"up": value}); err != nil {
			t.Errorf("rejected valid bandwidth %q: %v", value, err)
		}
		if err := p.Validate(map[string]string{"down": value}); err != nil {
			t.Errorf("rejected valid bandwidth %q: %v", value, err)
		}
	}
	for _, value := range []string{"1.5", "1e2", "0", "-1", "+1", "NaN", "+Inf", "-Inf", "18446744073709551616", fmt.Sprint(limit + 1), "secret"} {
		for _, direction := range []string{"up", "down"} {
			if err := p.Validate(map[string]string{direction: value}); err == nil || strings.Contains(err.Error(), value) {
				t.Errorf("invalid or leaked %s=%q: %v", direction, value, err)
			}
		}
	}
	for _, params := range []map[string]string{
		{"masquerade": "secret"}, {"sni": "bad host"}, {"public_host": "bad host"},
		{"obfs": "unsupported"}, {"cert_file": "one.pem"}, {"key_file": "secret.pem"},
		{"alpn": ""}, {"alpn": "has space"},
	} {
		if err := p.Validate(params); err == nil || strings.Contains(err.Error(), "secret.pem") || strings.Contains(err.Error(), "secret") {
			t.Errorf("invalid parameters were accepted or leaked: %v", err)
		}
	}
	if err := p.Validate(map[string]string{"up": "10", "down": "20"}); err != nil {
		t.Fatalf("valid bandwidth hints rejected: %v", err)
	}
}

func TestHysteria2MaterialSecrets(t *testing.T) {
	p := hysteria2{}
	a, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.NewSecret("bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{a["password"], b["password"]} {
		raw, err := base64.RawURLEncoding.DecodeString(secret)
		if err != nil || len(raw) != 32 {
			t.Fatal("invalid generated password")
		}
	}
	if a["password"] == b["password"] {
		t.Fatal("password collision")
	}
	material, err := p.NewMaterial(map[string]string{"public_host": "edge.example", "sni": "origin.example", "obfs": "salamander"})
	if err != nil {
		t.Fatal(err)
	}
	if material["obfs_password"] == "" || TLSFingerprint(material) == "" {
		t.Fatal("missing material")
	}
	leaf, err := readTLSLeaf(material)
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "edge.example" {
		t.Fatal("certificate host precedence failed")
	}
}

func TestHysteria2URIImporterAndMihomo(t *testing.T) {
	p := hysteria2{}
	material, err := p.NewMaterial(map[string]string{"obfs": "salamander"})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "edge", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 443, Accounts: []string{"alice", "bob"}, Params: map[string]string{"sni": "origin.example", "obfs": "salamander", "up": "10", "down": "20"}, Material: SecretMap(material), Users: UserSecrets{"alice": secret, "bob": {"password": "other"}}}
	link, err := p.Link(spec, "alice", "edge.example")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.User.String() != secret["password"] || u.Query().Get("pinSHA256") != TLSFingerprint(material) || u.Query().Has("pcs") || u.Query().Get("sni") != "origin.example" || u.Query().Get("insecure") != "0" || u.Query().Get("up") != "10" || u.Query().Get("down") != "20" {
		t.Fatal("URI output mismatch")
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 {
		t.Fatalf("import failed: %v", err)
	}
	proxy := converted[0]
	if proxy["type"] != "hysteria2" || proxy["password"] != secret["password"] || proxy["fingerprint"] != TLSFingerprint(material) || proxy["obfs-password"] != material["obfs_password"] || proxy["up"] != "10" || proxy["down"] != "20" || proxy["server"] != "edge.example" || proxy["port"] != "443" || proxy["name"] != "edge" || proxy["sni"] != "origin.example" || proxy["skip-cert-verify"] != false || proxy["obfs"] != "salamander" {
		t.Fatal("imported fields differ")
	}
	snippet, err := p.Mihomo(spec, "alice", "edge.example")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &config); err != nil || len(config.Proxies) != 1 {
		t.Fatal("invalid Mihomo YAML")
	}
	if config.Proxies[0]["name"] != "edge" || config.Proxies[0]["type"] != "hysteria2" || config.Proxies[0]["server"] != "edge.example" || config.Proxies[0]["port"] != 443 || config.Proxies[0]["password"] != secret["password"] || config.Proxies[0]["sni"] != "origin.example" || config.Proxies[0]["skip-cert-verify"] != false || config.Proxies[0]["udp"] != false || config.Proxies[0]["fingerprint"] != TLSFingerprint(material) || config.Proxies[0]["obfs"] != "salamander" || config.Proxies[0]["obfs-password"] != material["obfs_password"] || config.Proxies[0]["up"] != "10" || config.Proxies[0]["down"] != "20" {
		t.Fatal("Mihomo fields differ")
	}
}

func TestHysteria2LinkSafeErrorsAndSpecRedaction(t *testing.T) {
	p := hysteria2{}
	spec := Spec{Name: "edge", Protocol: "hysteria2", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{}, Material: SecretMap{"cert_pem": "x", "key_pem": "y"}, Users: UserSecrets{"alice": {"password": "sensitive secret"}}}
	if _, err := p.Link(spec, "alice", "localhost"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("unsafe URI password handling")
	}
	material, err := p.NewMaterial(nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.Material = SecretMap(material)
	spec.Users["alice"]["password"] = "reserved:/?#@!"
	if _, err := p.Link(spec, "alice", "localhost"); err == nil || strings.Contains(err.Error(), "reserved") {
		t.Fatal("unsupported URI password was accepted or leaked")
	}
	mihomo, err := p.Mihomo(spec, "alice", "localhost")
	if err != nil {
		t.Fatalf("Mihomo rejected reserved password: %v", err)
	}
	var config struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(mihomo), &config); err != nil || len(config.Proxies) != 1 || config.Proxies[0]["password"] != "reserved:/?#@!" {
		t.Fatal("Mihomo reserved password mismatch")
	}
	for _, formatted := range []string{fmt.Sprint(spec), fmt.Sprintf("%v", spec), fmt.Sprintf("%+v", spec), fmt.Sprintf("%#v", spec)} {
		if strings.Contains(formatted, "sensitive") || strings.Contains(formatted, "cert_pem") || strings.Contains(formatted, "reserved:/?#@!") || (spec.Material["key_pem"] != "" && strings.Contains(formatted, spec.Material["key_pem"])) {
			t.Fatal("Spec leaked secrets")
		}
	}
	if _, err := p.Start(Spec{Protocol: "hysteria2", Name: "bad"}, Env{}); err == nil {
		t.Fatal("invalid Start succeeded")
	}
}

func TestHysteria2StartRejectsMissingAndDuplicatePasswords(t *testing.T) {
	p, ok := Lookup("hysteria2")
	if !ok {
		t.Fatal("Hysteria2 is not registered")
	}
	base := Spec{Protocol: "hysteria2", Name: "edge", Port: 443, Accounts: []string{"alice", "bob"}, Params: map[string]string{}}

	missing := base
	missing.Users = UserSecrets{"alice": {"password": "alice-password"}}
	if _, err := p.Start(missing, Env{}); err == nil || !strings.Contains(err.Error(), "secret missing") {
		t.Fatalf("missing password error = %v", err)
	}

	duplicate := base
	duplicate.Users = UserSecrets{"alice": {"password": "same-password"}, "bob": {"password": "same-password"}}
	if _, err := p.Start(duplicate, Env{}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate password error = %v", err)
	}
}

func TestHysteria2StartErrorsRedactAndWrap(t *testing.T) {
	err := hysteria2StartError{cause: fmt.Errorf("bind failed: secret=%s: %w", "hidden-value", syscall.EADDRINUSE)}
	for _, formatted := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(formatted, "hidden-value") {
			t.Fatalf("start error leaked cause: %q", formatted)
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal("wrapped bind cause is not discoverable")
	}
}
