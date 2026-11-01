package servers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

func TestSnellValidationAndSecrets(t *testing.T) {
	p := snell{}
	cases := []struct {
		name   string
		params map[string]string
		valid  bool
	}{
		{"defaults", nil, true}, {"v2", map[string]string{"version": "2"}, true}, {"v3", map[string]string{"version": "3"}, true},
		{"obfs none", map[string]string{"obfs": "none"}, true}, {"obfs http", map[string]string{"obfs": "http"}, true}, {"obfs tls", map[string]string{"obfs": "tls"}, true},
		{"host", map[string]string{"obfs": "http", "obfs_host": "example.org"}, true}, {"IPv6 host", map[string]string{"obfs": "tls", "obfs_host": "2001:db8::1"}, true},
		{"bad version", map[string]string{"version": "4"}, false}, {"bad obfs", map[string]string{"obfs": "shadow-tls"}, false}, {"host without mode", map[string]string{"obfs_host": "secret-value"}, false}, {"unsafe host", map[string]string{"obfs": "tls", "obfs_host": "bad/value"}, false}, {"udp rejected", map[string]string{"udp": "true"}, false}, {"unknown", map[string]string{"x": "secret-value"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Validate(tc.params)
			if (err == nil) != tc.valid {
				t.Fatal("validation result mismatch")
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatal("validation leaked value")
			}
		})
	}
	a, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(a["psk"])
	if err != nil || len(raw) < 24 || !snellPSKValid(a["psk"]) || a["psk"] == b["psk"] {
		t.Fatal("generated PSK invalid or repeated")
	}
	for _, key := range []string{"", "short", base64.RawURLEncoding.EncodeToString([]byte("short")), base64.RawURLEncoding.EncodeToString(make([]byte, 24)) + "="} {
		if snellPSKValid(key) {
			t.Fatal("malformed PSK accepted")
		}
	}
	if _, err := p.NewMaterial(map[string]string{"udp": "true"}); err == nil {
		t.Fatal("invalid material parameters accepted")
	}
	m, err := p.NewMaterial(nil)
	if err != nil || len(m) != 0 {
		t.Fatal("unexpected Snell material")
	}
}

func TestSnellMihomoSnippetRoundTripAndErrors(t *testing.T) {
	p := snell{}
	for _, tc := range []struct{ version, obfs, host string }{{"2", "none", ""}, {"2", "http", "proxy.example"}, {"2", "http", ""}, {"3", "tls", "proxy.example"}} {
		params := map[string]string{"version": tc.version, "obfs": tc.obfs}
		if tc.host != "" {
			params["obfs_host"] = tc.host
		}
		key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
		s := Spec{Name: "snell.case", Protocol: "snell", Port: 8388, Accounts: []string{"alice"}, Params: params, Users: UserSecrets{"alice": {"psk": key}}}
		if err := s.Validate(); err != nil {
			t.Fatal("valid Snell spec rejected")
		}
		for _, f := range []string{fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s)} {
			if strings.Contains(f, key) {
				t.Fatal("Spec formatting exposed PSK")
			}
		}
		snippet, err := p.Mihomo(s, "alice", "2001:db8::1")
		if err != nil {
			t.Fatal("snippet generation failed")
		}
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &doc); err != nil || len(doc.Proxies) != 1 {
			t.Fatal("snippet YAML import failed")
		}
		proxy, err := adapter.ParseProxy(doc.Proxies[0])
		if err != nil {
			t.Fatal("Mihomo rejected snippet")
		}
		if proxy.Name() != s.Name || proxy.Type() != C.Snell || proxy.Addr() != "[2001:db8::1]:8388" {
			t.Fatal("imported proxy fields mismatch")
		}
		if doc.Proxies[0]["psk"] != key || fmt.Sprint(doc.Proxies[0]["version"]) != tc.version || doc.Proxies[0]["server"] != "2001:db8::1" || fmt.Sprint(doc.Proxies[0]["port"]) != "8388" || doc.Proxies[0]["udp"] != false {
			t.Fatal("imported proxy configuration mismatch")
		}
		_ = proxy.Close()
		if tc.obfs != "none" {
			obfs, ok := doc.Proxies[0]["obfs-opts"].(map[string]any)
			wantHost := tc.host
			if wantHost == "" {
				wantHost = "bing.com"
			}
			if !ok || obfs["mode"] != tc.obfs || obfs["host"] != wantHost {
				t.Fatal("imported obfs fields mismatch")
			}
		}
	}
	if _, err := convert.ConvertsV2Ray([]byte("snell://host.example:8388#invalid")); err == nil {
		t.Fatal("unsupported Snell URI unexpectedly converted")
	}
	if _, err := convert.ConvertsV2Ray(nil); err == nil {
		t.Fatal("empty converter input should fail")
	}
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	s := Spec{Name: "snell", Protocol: "snell", Port: 8388, Accounts: []string{"alice"}, Users: UserSecrets{"alice": {"psk": key}}}
	if _, err := p.Link(s, "alice", "host.example"); err == nil || !strings.Contains(err.Error(), "URI") {
		t.Fatal("Link should report no URI")
	}
	if _, err := p.Mihomo(s, "bob", "host.example"); err == nil {
		t.Fatal("wrong login accepted")
	}
	safe := snellStartError{cause: fmt.Errorf("secret=%s: %w", key, syscall.EADDRINUSE)}
	for _, f := range []string{safe.Error(), fmt.Sprint(safe), fmt.Sprintf("%#v", safe), safe.String(), safe.GoString()} {
		if strings.Contains(f, key) {
			t.Fatal("startup error leaked PSK")
		}
	}
	if !errors.Is(safe, syscall.EADDRINUSE) {
		t.Fatal("safe error lost Is classification")
	}
}

type snellCaptureTunnel struct {
	user string
}

func (t *snellCaptureTunnel) HandleTCPConn(_ net.Conn, metadata *C.Metadata) {
	t.user = metadata.InUser
}
func (*snellCaptureTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}
func (*snellCaptureTunnel) NatTable() C.NatTable                     { return nil }

func TestSnellPinnedTunnelUsesConfiguredAccount(t *testing.T) {
	inner := &snellCaptureTunnel{}
	metadata := &C.Metadata{InUser: "bob"}
	pinned := snellPinnedTunnel{inner: inner, user: "alice"}
	pinned.HandleTCPConn(nil, metadata)
	if inner.user != "alice" || metadata.InUser != "alice" {
		t.Fatal("Snell tunnel did not pin configured account")
	}
}
