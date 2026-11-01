package servers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
)

const testVMessUUID = "9d9a8a29-64ae-4c1b-b418-350548e2f011"

func TestVMessValidationAndGeneratedUUID(t *testing.T) {
	protocol := vmess{}
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{name: "empty defaults"},
		{name: "default cipher case", params: map[string]string{"cipher": "AUTO"}, want: "auto"},
		{name: "AES case", params: map[string]string{"cipher": "AES-128-GCM"}, want: "aes-128-gcm"},
		{name: "chacha", params: map[string]string{"cipher": "chacha20-poly1305"}, want: "chacha20-poly1305"},
		{name: "none", params: map[string]string{"cipher": "none"}, want: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := protocol.Validate(tc.params); err != nil {
				t.Fatal("valid cipher rejected")
			}
			if got := vmessCipher(tc.params); tc.want != "" && got != tc.want {
				t.Fatal("cipher normalization mismatch")
			}
			secret, err := protocol.NewSecret("alice", tc.params)
			if err != nil {
				t.Fatal("UUID generation failed")
			}
			id, err := uuid.Parse(secret["uuid"])
			if err != nil || id == uuid.Nil || id.Version() != 4 {
				t.Fatal("generated UUID is not v4")
			}
		})
	}
	for _, params := range []map[string]string{{"cipher": "aes-256-gcm"}, {"cipher": "bad"}, {"other": "value"}} {
		if err := protocol.Validate(params); err == nil {
			t.Fatal("invalid parameters accepted")
		}
	}
	if vmessCipher(nil) != "auto" {
		t.Fatal("empty cipher default mismatch")
	}
}
func vmessTestSpec() Spec {
	return Spec{Name: "entry-1", Protocol: "vmess", Port: 443, Accounts: []string{"alice"}, Params: map[string]string{}, Users: UserSecrets{"alice": {"uuid": testVMessUUID}}}
}
func TestVMessLinkRoundTripAndMihomo(t *testing.T) {
	protocol := vmess{}
	spec := vmessTestSpec()
	spec.Params["cipher"] = "auto"
	for _, host := range []string{"edge.example", "2001:db8::8"} {
		link, err := protocol.Link(spec, "alice", host)
		if err != nil {
			t.Fatal("link generation failed")
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
		if err != nil {
			t.Fatal("link base64 invalid")
		}
		got := map[string]string{}
		if err = json.Unmarshal(data, &got); err != nil {
			t.Fatal("link JSON invalid")
		}
		want := map[string]string{"v": "2", "ps": "entry-1", "add": host, "port": "443", "id": testVMessUUID, "aid": "0", "scy": "auto", "net": "tcp", "type": "none", "host": "", "path": "", "tls": ""}
		if len(got) != len(want) {
			t.Fatal("link fields incomplete")
		}
		for key, value := range want {
			if got[key] != value {
				t.Fatalf("link field %s mismatch", key)
			}
		}
		snippet, err := protocol.Mihomo(spec, "alice", host)
		if err != nil {
			t.Fatal("Mihomo snippet generation failed")
		}
		var config struct {
			Proxies []struct {
				Name    string `yaml:"name"`
				Type    string `yaml:"type"`
				Server  string `yaml:"server"`
				Port    int    `yaml:"port"`
				UUID    string `yaml:"uuid"`
				AlterID int    `yaml:"alterId"`
				Cipher  string `yaml:"cipher"`
				Network string `yaml:"network"`
				TLS     bool   `yaml:"tls"`
				UDP     bool   `yaml:"udp"`
			} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &config); err != nil {
			t.Fatal("Mihomo snippet YAML invalid")
		}
		if len(config.Proxies) != 1 {
			t.Fatal("Mihomo snippet proxy count mismatch")
		}
		proxy := config.Proxies[0]
		if proxy.Name != "entry-1" || proxy.Type != "vmess" || proxy.Server != host || proxy.Port != 443 || proxy.UUID != testVMessUUID || proxy.AlterID != 0 || proxy.Cipher != "auto" || proxy.Network != "tcp" || proxy.TLS || proxy.UDP {
			t.Fatal("Mihomo snippet fields mismatch")
		}
	}
}
func TestVMessLinkRejectsInvalidInputs(t *testing.T) {
	protocol := vmess{}
	valid := vmessTestSpec()
	cases := []struct {
		name        string
		spec        Spec
		login, host string
	}{
		{name: "wrong protocol", spec: func() Spec { s := valid.Clone(); s.Protocol = "other"; return s }(), login: "alice", host: "edge.example"},
		{name: "missing account", spec: valid, login: "bob", host: "edge.example"},
		{name: "missing host", spec: valid, login: "alice"},
		{name: "missing UUID", spec: func() Spec { s := valid.Clone(); delete(s.Users["alice"], "uuid"); return s }(), login: "alice", host: "edge.example"},
		{name: "invalid UUID", spec: func() Spec { s := valid.Clone(); s.Users["alice"]["uuid"] = "bad"; return s }(), login: "alice", host: "edge.example"},
		{name: "non-v4 UUID", spec: func() Spec {
			s := valid.Clone()
			s.Users["alice"]["uuid"] = "00000000-0000-0000-0000-000000000001"
			return s
		}(), login: "alice", host: "edge.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := protocol.Link(tc.spec, tc.login, tc.host); err == nil {
				t.Fatal("invalid link inputs accepted")
			}
			if _, err := protocol.Mihomo(tc.spec, tc.login, tc.host); err == nil {
				t.Fatal("invalid snippet inputs accepted")
			}
		})
	}
}
func TestCanonicalVMessUUID(t *testing.T) {
	cases := []struct {
		name, value, want string
		valid             bool
	}{
		{name: "canonical v4", value: testVMessUUID, want: testVMessUUID, valid: true},
		{name: "uppercase v4", value: "9D9A8A29-64AE-4C1B-B418-350548E2F011", want: testVMessUUID, valid: true},
		{name: "bad syntax", value: "invalid"},
		{name: "non-v4", value: "00000000-0000-0000-0000-000000000001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalVMessUUID(tc.value)
			if (err == nil) != tc.valid {
				t.Fatal("UUID validation mismatch")
			}
			if tc.valid && got != tc.want {
				t.Fatal("UUID canonicalization mismatch")
			}
		})
	}
}
func TestSafeVMessStartErrorRedactsAndPreservesIs(t *testing.T) {
	cause := errors.New("bind failed")
	err := safeVMessStartError{cause: fmt.Errorf("cause contains secret-marker: %w", cause)}
	for _, text := range []string{err.Error(), err.String(), err.GoString(), fmt.Sprintf("%#v", err)} {
		if strings.Contains(text, "secret-marker") {
			t.Fatal("start error formatting leaked cause")
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is did not preserve cause")
	}
}
