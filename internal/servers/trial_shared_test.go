package servers

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const trialBind = "127.0.0.1"

func ssSpec(name, cipher, password string) Spec {
	return Spec{Name: name, Bind: trialBind, Port: 443, Protocol: "shadowsocks", Accounts: []string{"u"}, Params: map[string]string{"cipher": cipher}, Users: UserSecrets{"u": {"password": password}}}
}
func vmessSpec(name string, count int, uuid string) Spec {
	s := Spec{Name: name, Bind: trialBind, Port: 443, Protocol: "vmess", Params: map[string]string{}}
	for i := 0; i < count; i++ {
		login := fmt.Sprintf("u%d", i)
		s.Accounts = append(s.Accounts, login)
		if uuid != "" {
			s.Users = appendSecret(s.Users, login, uuid)
		}
	}
	return s
}
func appendSecret(users UserSecrets, login, id string) UserSecrets {
	if users == nil {
		users = UserSecrets{}
	}
	users[login] = map[string]string{"uuid": id}
	return users
}
func tlsSpec(name, sni string, def bool) Spec {
	return Spec{Name: name, Bind: trialBind, Port: 443, Protocol: "trojan", Default: def, Params: map[string]string{"sni": sni}}
}
func requireAmbiguous(t *testing.T, err error) *cli.Error {
	t.Helper()
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("error=%v, want SERVER_PORT_AMBIGUOUS cli.Error", err)
	}
	return ce
}

func TestValidateSharedPortMixedTrialGroup(t *testing.T) {
	password := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if err := ValidateSharedPort([]Spec{ssSpec("ss", "2022-blake3-aes-128-gcm", password), tlsSpec("tls", "tls.example", false)}); err != nil {
		t.Fatalf("valid mixed group rejected: %v", err)
	}
}

func TestValidateSharedPortTrialDefaultAndLimits(t *testing.T) {
	trialDefault := vmessSpec("trial", 1, "")
	trialDefault.Default = true
	ce := requireAmbiguous(t, ValidateSharedPort([]Spec{trialDefault}))
	if ce.Hint == "" {
		t.Fatal("trial default lacks safe hint")
	}

	seventeen := make([]Spec, 17)
	for i := range seventeen {
		seventeen[i] = Spec{Name: fmt.Sprintf("ss%d", i), Bind: trialBind, Port: 443, Protocol: "shadowsocks", Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}}
	}
	requireAmbiguous(t, ValidateSharedPort(seventeen))

	a := vmessSpec("vmess-a", 512, "")
	b := vmessSpec("vmess-b", 513, "")
	requireAmbiguous(t, ValidateSharedPort([]Spec{a, b}))
	if err := ValidateSharedPort([]Spec{vmessSpec("vmess-a", 512, ""), vmessSpec("vmess-b", 512, "")}); err != nil {
		t.Fatalf("1024 VMess users rejected: %v", err)
	}
}

func TestValidateSharedPortDuplicateRawPSKAcrossCiphers(t *testing.T) {
	for _, tc := range []struct {
		name, cipher string
		size         int
	}{
		{name: "same AES-128", cipher: "2022-blake3-aes-128-gcm", size: 16},
		{name: "same 32-byte AES-256", cipher: "2022-blake3-aes-256-gcm", size: 32},
		{name: "AES-256 and ChaCha20", cipher: "2022-blake3-chacha20-poly1305", size: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			psk := base64.StdEncoding.EncodeToString(make([]byte, tc.size))
			a := ssSpec("a", "2022-blake3-aes-256-gcm", psk)
			if tc.size == 16 {
				a = ssSpec("a", "2022-blake3-aes-128-gcm", psk)
			}
			b := ssSpec("b", tc.cipher, psk)
			for _, spec := range []Spec{a, b} {
				if _, err := NewTrial(spec); err != nil {
					t.Fatal("invalid test fixture")
				}
			}

			// Distinct valid keys are the positive control: this pair must be accepted.
			distinct := ssSpec("b", tc.cipher, base64.StdEncoding.EncodeToString(bytesOf(tc.size, 1)))
			if err := ValidateSharedPort([]Spec{a, distinct}); err != nil {
				t.Fatal("distinct valid keys rejected")
			}

			err := ValidateSharedPort([]Spec{a, b})
			ce, ok := err.(*cli.Error)
			if !ok || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Message != "shared endpoint has invalid or ambiguous trial credentials" || ce.Hint != "use distinct valid trial credentials" {
				t.Fatal("duplicate credential error did not match the safe ambiguity contract")
			}
			for _, rendered := range []string{ce.Error(), fmt.Sprintf("%#v", ce)} {
				if strings.Contains(rendered, psk) || strings.Contains(rendered, fmt.Sprintf("%x", make([]byte, tc.size))) {
					t.Fatal("credential leaked in error formatting")
				}
			}
		})
	}
}

func bytesOf(size int, value byte) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = value
	}
	return b
}

func TestValidateSharedPortCanonicalDuplicateUUID(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	a := vmessSpec("a", 1, id)
	b := vmessSpec("b", 1, "550E8400-E29B-41D4-A716-446655440000")
	ce := requireAmbiguous(t, ValidateSharedPort([]Spec{a, b}))
	if ce.Hint == "" {
		t.Fatal("duplicate UUID error lacks safe hint")
	}
}

func TestValidateSharedPortTrialSecretsMissingVersusInvalid(t *testing.T) {
	tls := tlsSpec("tls", "tls.example", false)
	missing := ssSpec("ss", "2022-blake3-aes-128-gcm", "")
	missing.Users = nil
	if err := ValidateSharedPort([]Spec{missing, tls}); err != nil {
		t.Fatalf("absent secret config rejected: %v", err)
	}
	invalid := ssSpec("ss", "2022-blake3-aes-128-gcm", "present-but-invalid")
	ce := requireAmbiguous(t, ValidateSharedPort([]Spec{invalid, tls}))
	if ce.Hint == "" {
		t.Fatal("invalid secret error lacks safe hint")
	}
}

func TestValidateSharedPortTLSValidationErrorKeysUnchanged(t *testing.T) {
	t.Run("overlap", func(t *testing.T) {
		err := ValidateSharedPort([]Spec{tlsSpec("a", "same.example", false), tlsSpec("b", "SAME.EXAMPLE", false)})
		ce := requireAmbiguous(t, err)
		if ce.Message != "shared server TLS match keys overlap" {
			t.Fatalf("message=%q", ce.Message)
		}
	})
	t.Run("default overlap", func(t *testing.T) {
		err := ValidateSharedPort([]Spec{tlsSpec("default", "same.example", true), tlsSpec("other", "SAME.EXAMPLE", false)})
		ce := requireAmbiguous(t, err)
		if ce.Message != "shared server TLS match keys overlap" {
			t.Fatalf("message=%q", ce.Message)
		}
	})
	t.Run("missing SNI", func(t *testing.T) {
		noSNI := Spec{Name: "no-sni", Bind: trialBind, Port: 443, Protocol: "trojan", Params: map[string]string{}}
		err := ValidateSharedPort([]Spec{noSNI, tlsSpec("other", "other.example", false)})
		ce := requireAmbiguous(t, err)
		if ce.Message != "shared server needs explicit SNI" {
			t.Fatalf("message=%q", ce.Message)
		}
	})
}
