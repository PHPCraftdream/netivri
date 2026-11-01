package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestRegisteredServerLinksImportAsMihomoNodes(t *testing.T) {
	wantTypes := map[string]string{"shadowsocks": "ss", "trojan": "trojan", "vmess": "vmess", "anytls": "anytls", "hysteria2": "hysteria2"}
	for _, protocol := range servers.Registered() {
		t.Run(protocol.Name(), func(t *testing.T) {
			if _, ok := wantTypes[protocol.Name()]; !ok {
				// Each protocol proves its own links in its unit tests; a
				// protocol without a mapping here is not checked centrally.
				t.Skipf("protocol %q has no central import mapping", protocol.Name())
			}
			params := map[string]string{}
			if protocol.Name() == "shadowsocks" {
				params["cipher"] = "aes-128-gcm"
			} else if protocol.Name() == "trojan" {
				params["public_host"] = "edge.example.org"
			}
			assertLinkImports(t, protocol, params, "edge")
		})
	}
}

func TestRegisteredServerLinksImportAsMihomoNodesDefaultShadowsocks(t *testing.T) {
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("Shadowsocks protocol is not registered")
	}
	assertLinkImports(t, protocol, map[string]string{}, "edge")
}

func assertLinkImports(t *testing.T, protocol servers.Protocol, params map[string]string, name string) {
	t.Helper()
	material, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal("protocol material generation failed")
	}
	secret, err := protocol.NewSecret("alice", params)
	if err != nil {
		t.Fatal("protocol secret generation failed")
	}
	spec := servers.Spec{Name: name, Protocol: protocol.Name(), Bind: "127.0.0.1", Port: 8388, Accounts: []string{"alice"}, Params: params, Material: servers.SecretMap(material), Users: servers.UserSecrets{"alice": secret}}
	for _, generate := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"uri", func() (string, error) { return protocol.Link(spec, "alice", "edge.example.org") }},
		{"mihomo", func() (string, error) {
			linker, ok := protocol.(servers.MihomoLinker)
			if !ok {
				return "", fmt.Errorf("no Mihomo linker")
			}
			return linker.Mihomo(spec, "alice", "edge.example.org")
		}},
	} {
		t.Run(generate.name, func(t *testing.T) {
			text, err := generate.fn()
			if err != nil {
				t.Fatal("link generation failed")
			}
			parsed, err := subscription.ParseDetailed([]byte(text))
			if err != nil {
				t.Fatal("link import failed")
			}
			if len(parsed.Nodes) != 1 || parsed.Skipped.Invalid+parsed.Skipped.Banner != 0 {
				t.Fatalf("expected one node and zero skips; got %d nodes, %d skips", len(parsed.Nodes), parsed.Skipped.Invalid+parsed.Skipped.Banner)
			}
			node := parsed.Nodes[0]
			wantType := map[string]string{"shadowsocks": "ss", "trojan": "trojan", "vmess": "vmess", "anytls": "anytls", "hysteria2": "hysteria2"}[protocol.Name()]
			if node.Name != spec.Name || node.Type != wantType || node.Server != "edge.example.org" || node.Port != spec.Port {
				t.Fatalf("imported node identity mismatch for %q", protocol.Name())
			}
		})
	}
}

func serverErrorSummary(stdout, stderr string) string {
	type envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, raw := range []string{stderr, stdout} {
		var env envelope
		if json.Unmarshal([]byte(raw), &env) == nil && (env.Error.Code != "" || env.Error.Message != "") {
			urlPattern := regexp.MustCompile(`\S*://\S+`)
			message := urlPattern.ReplaceAllString(env.Error.Message, "[redacted]")
			if strings.Contains(message, "@") {
				message = "[redacted]"
			}
			return fmt.Sprintf("%s: %s", env.Error.Code, message)
		}
	}
	return "command failed"
}

func TestMappedIPv4LoopbackPublicHostIsNotBanner(t *testing.T) {
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("Shadowsocks protocol is not registered")
	}
	params := map[string]string{"cipher": "aes-128-gcm"}
	material, err := protocol.NewMaterial(params)
	if err != nil {
		t.Fatal("protocol material generation failed")
	}
	secret, err := protocol.NewSecret("alice", params)
	if err != nil {
		t.Fatal("protocol secret generation failed")
	}
	spec := servers.Spec{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 8388, Accounts: []string{"alice"}, Params: params, Material: servers.SecretMap(material), Users: servers.UserSecrets{"alice": secret}}
	plainURI, err := protocol.Link(spec, "alice", "127.0.0.1")
	if err != nil {
		t.Fatal("plain loopback link generation failed")
	}
	plain, plainErr := subscription.ParseDetailed([]byte(plainURI))
	if plainErr == nil || len(plain.Nodes) != 0 || plain.Skipped.Banner != 1 {
		t.Fatalf("plain loopback was not classified as a banner: nodes=%d skipped=%+v", len(plain.Nodes), plain.Skipped)
	}
	uri, err := protocol.Link(spec, "alice", "::ffff:127.0.0.1")
	if err != nil {
		t.Fatal("link generation failed")
	}
	parsed, err := subscription.ParseDetailed([]byte(uri))
	if err != nil || len(parsed.Nodes) != 1 || parsed.Skipped.Invalid+parsed.Skipped.Banner != 0 {
		t.Fatalf("mapped loopback link was not imported: nodes=%d skipped=%+v", len(parsed.Nodes), parsed.Skipped)
	}
	node := parsed.Nodes[0]
	if node.Name != spec.Name || node.Type != "ss" || node.Server != "::ffff:127.0.0.1" || node.Port != spec.Port {
		t.Fatalf("mapped loopback node identity mismatch")
	}
}

func TestServerErrorSummaryRedactsAndSelectsEnvelope(t *testing.T) {
	got := serverErrorSummary(`{"error":{"code":"SUBSCRIPTION_EMPTY","message":"invalid link https://secret.example/x"}}`, "")
	if !strings.Contains(got, "SUBSCRIPTION_EMPTY") || strings.Contains(got, "secret.example") {
		t.Fatalf("unexpected summary: %q", got)
	}
	if got := serverErrorSummary(`{"error":{"code":"SUBSCRIPTION_EMPTY","message":"no nodes found in subscription body"}}`, ""); got != "SUBSCRIPTION_EMPTY: no nodes found in subscription body" {
		t.Fatalf("unexpected exact summary: %q", got)
	}
	got = serverErrorSummary(`{"error":{"code":"X","message":"invalid link ss://password@host.example:8388"}}`, "")
	if strings.Contains(got, "password") || strings.Contains(got, "host.example") {
		t.Fatalf("credentials leaked in summary: %q", got)
	}
	if got := serverErrorSummary("not json", ""); got != "command failed" {
		t.Fatalf("unexpected fallback: %q", got)
	}
}
