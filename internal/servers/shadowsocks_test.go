package servers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"testing"
)

type testProtocol string

func (p testProtocol) Name() string                                                 { return string(p) }
func (testProtocol) SingleUser() bool                                               { return false }
func (testProtocol) Validate(map[string]string) error                               { return nil }
func (testProtocol) NewMaterial(map[string]string) (map[string]string, error)       { return nil, nil }
func (testProtocol) NewSecret(string, map[string]string) (map[string]string, error) { return nil, nil }
func (testProtocol) Start(Spec, Env) (io.Closer, error)                             { return nil, nil }
func (testProtocol) Link(Spec, string, string) (string, error)                      { return "", nil }

func TestRegistry(t *testing.T) {
	for _, n := range []string{"z-test", "a-test"} {
		Register(testProtocol(n))
	}
	defer func() {
		registryMu.Lock()
		delete(registry, "z-test")
		delete(registry, "a-test")
		registryMu.Unlock()
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("duplicate registration did not panic")
			}
		}()
		Register(testProtocol("a-test"))
	}()
	p, ok := Lookup("a-test")
	if !ok || p.Name() != "a-test" {
		t.Fatal("lookup failed")
	}
	if got := Names(); !sort.StringsAreSorted(got) {
		t.Fatalf("names not sorted: %v", got)
	}
}

func TestSpecValidationAndRedaction(t *testing.T) {
	ordinaryUsers := map[string]string{"password": "password-value"}
	s := Spec{Name: "edge-1", Protocol: "shadowsocks", Port: 8388, Accounts: []string{"alice"}, Params: map[string]string{"unsafe": "parameter-value"}, Material: SecretMap{"key": "private-value"}, Users: UserSecrets{"alice": ordinaryUsers}}
	if got := fmt.Sprintf("%v %+v %#v", s.Users, s.Users, s.Users); strings.Contains(got, "password-value") {
		t.Fatalf("nested user secret formatting exposed value: %s", got)
	}
	if got := fmt.Sprintf("%v %+v %#v", s, s, s); strings.Contains(got, "value") {
		t.Fatalf("Spec formatting exposed map values: %s", got)
	}
	if got := fmt.Sprintf("%s %q %#v", SecretMap{"secret": "password-value"}, SecretMap{"secret": "password-value"}, SecretMap{"secret": "password-value"}); strings.Contains(got, "password-value") {
		t.Fatalf("secret map formatting exposed value: %s", got)
	}
	if err := s.Validate(); err == nil {
		t.Fatal("unknown parameter accepted")
	}
	s.Params = nil
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.ListenAddress() != "0.0.0.0:8388" {
		t.Fatalf("address %q", s.ListenAddress())
	}
	if !s.Equal(s.Clone()) {
		t.Fatal("clone differs")
	}
}

func TestSpecValidateAccounts(t *testing.T) {
	valid := Spec{Name: "edge", Protocol: "shadowsocks", Port: 9, Accounts: []string{"alice_1"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	for _, accounts := range [][]string{{"*"}, {"bad/name"}, {"bad name"}, {"alice", "alice"}, {""}, {"alice", "bob"}} {
		t.Run(fmt.Sprint(accounts), func(t *testing.T) {
			s := valid.Clone()
			s.Accounts = accounts
			if err := s.Validate(); err == nil {
				t.Errorf("invalid accounts accepted")
			}
		})
	}
}

func TestSpecValidateFields(t *testing.T) {
	valid := Spec{Name: "edge", Protocol: "shadowsocks", Port: 9, Accounts: []string{"alice"}}
	longName := strings.Repeat("a", 33)
	cases := []struct {
		name string
		edit func(*Spec)
	}{
		{"empty name", func(s *Spec) { s.Name = "" }},
		{"long name", func(s *Spec) { s.Name = longName }},
		{"invalid name", func(s *Spec) { s.Name = "bad/name" }},
		{"port zero", func(s *Spec) { s.Port = 0 }},
		{"port negative", func(s *Spec) { s.Port = -1 }},
		{"port above max", func(s *Spec) { s.Port = 65536 }},
		{"unknown protocol", func(s *Spec) { s.Protocol = "unknown" }},
		{"no accounts", func(s *Spec) { s.Accounts = nil }},
		{"unknown parameter", func(s *Spec) { s.Params = map[string]string{"unknown": "secret-value"} }},
		{"unknown cipher", func(s *Spec) { s.Params = map[string]string{"cipher": "unknown"} }},
		{"invalid udp", func(s *Spec) { s.Params = map[string]string{"udp": "sometimes"} }},
		{"unsupported udp", func(s *Spec) { s.Params = map[string]string{"udp": "true"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid.Clone()
			tc.edit(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid spec accepted")
			} else if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("validation error exposed a parameter value")
			}
		})
	}
}

func TestSafeStartErrorRedactsAndPreservesIs(t *testing.T) {
	err := safeStartError{cause: fmt.Errorf("bind failed with password=must-not-leak: %w", syscall.EADDRINUSE)}
	for _, formatted := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), err.String(), err.GoString()} {
		if strings.Contains(formatted, "must-not-leak") {
			t.Fatal("safe start error exposed its cause")
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal("safe start error lost errors.Is classification")
	}
}
func TestMapEqualityChecksKeys(t *testing.T) {
	if equalMap(map[string]string{"x": ""}, map[string]string{"y": ""}) {
		t.Fatal("maps with different empty-valued keys compared equal")
	}
	if equalUsers(UserSecrets{"alice": {"x": ""}}, UserSecrets{"alice": {"y": ""}}) {
		t.Fatal("user maps with different empty-valued keys compared equal")
	}
	if equalUsers(UserSecrets{"alice": {}}, UserSecrets{"bob": {}}) {
		t.Fatal("different empty user entries compared equal")
	}
}
func TestShadowsocksCipherWhitelistAndGeneratedKeys(t *testing.T) {
	p := shadowsocks{}
	valid := map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32, "aes-128-gcm": 16, "aes-256-gcm": 32, "chacha20-ietf-poly1305": 32}
	for cipher, size := range valid {
		params := map[string]string{"cipher": cipher}
		if err := p.Validate(params); err != nil {
			t.Errorf("%s rejected: %v", cipher, err)
			continue
		}
		secret, err := p.NewSecret("alice", params)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if strings.HasPrefix(cipher, "2022-") {
			raw, err = base64.StdEncoding.DecodeString(secret["password"])
		} else {
			raw, err = base64.RawURLEncoding.DecodeString(secret["password"])
		}
		if err != nil || len(raw) != size {
			t.Errorf("%s key size=%d err=%v", cipher, len(raw), err)
		}
	}
	for _, params := range []map[string]string{nil, {}, {"udp": "false"}} {
		if err := p.Validate(params); err != nil {
			t.Errorf("accepted UDP-disabled params rejected: %v", err)
		}
	}
	if err := p.Validate(map[string]string{"udp": "true"}); err == nil || !strings.Contains(err.Error(), "udp") || !strings.Contains(err.Error(), "UDP through account routes is not supported yet") {
		t.Fatalf("udp=true error = %v", err)
	}
	for _, cipher := range []string{"aes-192-gcm", "rc4-md5", "dummy", "nope"} {
		if err := p.Validate(map[string]string{"cipher": cipher}); err == nil {
			t.Errorf("forbidden cipher %q accepted", cipher)
		}
	}
}

func TestShadowsocksLinkRoundTrip(t *testing.T) {
	for cipher := range map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32, "aes-128-gcm": 16, "aes-256-gcm": 32, "chacha20-ietf-poly1305": 32} {
		t.Run(cipher, func(t *testing.T) {
			password := "YctPZ6U7xPPcU+gp3u+0tx/tRizJN9K8y+uKlW2qjlI="
			for _, host := range []string{"example.org", "2001:db8::7"} {
				spec := Spec{Name: "ss.edge_1", Protocol: "shadowsocks", Port: 8388, Accounts: []string{"alice"}, Params: map[string]string{"cipher": cipher}, Users: UserSecrets{"alice": {"password": password}}}
				link, err := (shadowsocks{}).Link(spec, "alice", host)
				if err != nil {
					t.Fatal(err)
				}
				userinfo := strings.TrimPrefix(strings.SplitN(link, "@", 2)[0], "ss://")
				if strings.HasPrefix(cipher, "2022-") {
					parts := strings.SplitN(userinfo, ":", 2)
					if len(parts) != 2 || parts[0] != cipher {
						t.Fatalf("2022 method mismatch for %s", cipher)
					}
					decoded, err := url.QueryUnescape(parts[1])
					if err != nil || decoded != password {
						t.Fatalf("2022 password round-trip failed for %s", cipher)
					}
					if !strings.Contains(link, cipher+":"+url.QueryEscape(password)+"@") || !strings.Contains(userinfo, "%2B") || !strings.Contains(userinfo, "%2F") || !strings.Contains(userinfo, "%3D") {
						t.Fatalf("2022 userinfo encoding mismatch for %s", cipher)
					}
				} else {
					decoded, err := base64.RawURLEncoding.DecodeString(userinfo)
					if err != nil {
						t.Fatal(err)
					}
					parts := strings.SplitN(string(decoded), ":", 2)
					if len(parts) != 2 || parts[0] != cipher || parts[1] != password {
						t.Fatalf("SIP002 payload round-trip failed for %s", cipher)
					}
					if strings.Contains(userinfo, "=") {
						t.Fatal("SIP002 userinfo unexpectedly padded")
					}
				}
				if !strings.HasSuffix(link, "#ss.edge_1") {
					t.Fatalf("fragment mismatch for %s", cipher)
				}
				if !strings.Contains(link, net.JoinHostPort(host, "8388")) {
					t.Fatalf("host mismatch for %s", cipher)
				}
			}
		})
	}
}
