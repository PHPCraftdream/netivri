package servers

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/metacubex/sing-shadowsocks/shadowaead_2022"
	singvmess "github.com/metacubex/sing-vmess"
	"golang.org/x/crypto/chacha20poly1305"
)

type Verdict uint8

const (
	Reject Verdict = iota
	Accept
	NeedMore
)

type Trial interface {
	MinBytes() int
	Try([]byte, time.Time) (Verdict, []byte)
}
type TrialAuthenticator interface{ NewTrial(Spec) (Trial, error) }

type trialAuth struct {
	protocol string
	min      int
	keys     []trialKey
	method   string
}
type trialKey struct {
	login string
	raw   []byte
	cmd   [16]byte
	auth  cipher.Block
}

func (t *trialAuth) String() string   { return "<redacted>" }
func (t *trialAuth) GoString() string { return "<redacted>" }
func (t *trialAuth) MinBytes() int    { return t.min }

type ssTrialAuthenticator struct{}
type vmessTrialAuthenticator struct{}

func (ssTrialAuthenticator) NewTrial(s Spec) (Trial, error)    { return newTrial(s) }
func (vmessTrialAuthenticator) NewTrial(s Spec) (Trial, error) { return newTrial(s) }
func NewTrial(s Spec) (Trial, error)                           { return newTrial(s) }
func newTrial(s Spec) (Trial, error) {
	if s.Protocol != "shadowsocks" && s.Protocol != "vmess" {
		return nil, errors.New("trial authentication unsupported")
	}
	if len(s.Accounts) == 0 {
		return nil, errors.New("trial authentication requires users")
	}
	t := &trialAuth{protocol: s.Protocol}
	if s.Protocol == "shadowsocks" {
		t.method = cipherName(s.Params)
		size, err := secretSize(t.method)
		if err != nil || !strings.HasPrefix(t.method, "2022-blake3-") {
			return nil, errors.New("invalid trial configuration")
		}
		for _, login := range s.Accounts {
			raw, err := base64.StdEncoding.DecodeString(s.Users[login]["password"])
			if err != nil || len(raw) != size {
				return nil, errors.New("invalid trial secret")
			}
			t.keys = append(t.keys, trialKey{login: login, raw: append([]byte(nil), raw...)})
		}
		t.min = size + 11 + 16
	} else {
		for _, login := range s.Accounts {
			id, err := uuid.FromString(s.Users[login]["uuid"])
			if err != nil || id.Version() != 4 {
				return nil, errors.New("invalid trial secret")
			}
			cmd := singvmess.Key(id)
			authKey := singvmess.KDF(cmd[:], singvmess.KDFSaltConstAuthIDEncryptionKey)[:16]
			b, err := aes.NewCipher(authKey)
			if err != nil {
				return nil, errors.New("invalid trial secret")
			}
			t.keys = append(t.keys, trialKey{login: login, cmd: cmd, auth: b})
		}
		t.min = 42
	}
	return t, nil
}
func (t *trialAuth) Try(p []byte, now time.Time) (Verdict, []byte) {
	if len(p) < t.min {
		return NeedMore, nil
	}
	if t.protocol == "vmess" {
		for _, u := range t.keys {
			var id [16]byte
			u.auth.Decrypt(id[:], p[:16])
			stamp := int64(binary.BigEndian.Uint64(id[:8]))
			if crc32.ChecksumIEEE(id[:12]) != binary.BigEndian.Uint32(id[12:]) || math.Abs(float64(now.Unix()-stamp)) > 118 {
				continue
			}
			nonce := p[34:42]
			lengthKey := singvmess.KDF(u.cmd[:], singvmess.KDFSaltConstVMessHeaderPayloadLengthAEADKey, p[:16], nonce)[:16]
			lengthNonce := singvmess.KDF(u.cmd[:], singvmess.KDFSaltConstVMessHeaderPayloadLengthAEADIV, p[:16], nonce)[:12]
			block, err := aes.NewCipher(lengthKey)
			if err != nil {
				continue
			}
			aead, err := cipher.NewGCM(block)
			if err != nil {
				continue
			}
			if _, err = aead.Open(nil, lengthNonce, p[16:34], p[:16]); err != nil {
				continue
			}
			return Accept, append([]byte(nil), id[:]...)
		}
		return Reject, nil
	}
	size := len(t.keys[0].raw)
	for _, u := range t.keys {
		key := shadowaead_2022.SessionKey(u.raw, p[:size], size)
		var aead cipher.AEAD
		var err error
		switch t.method {
		case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm":
			var b cipher.Block
			b, err = aes.NewCipher(key)
			if err == nil {
				aead, err = cipher.NewGCM(b)
			}
		case "2022-blake3-chacha20-poly1305":
			aead, err = chacha20poly1305.New(key)
		}
		if err != nil {
			continue
		}
		plain, err := aead.Open(nil, make([]byte, aead.NonceSize()), p[size:size+27], nil)
		if err != nil || len(plain) != 11 || plain[0] != shadowaead_2022.HeaderTypeClient {
			continue
		}
		stamp := binary.BigEndian.Uint64(plain[1:9])
		if math.Abs(float64(now.Unix()-int64(stamp))) > 28 {
			continue
		}
		return Accept, append([]byte(nil), p[:size]...)
	}
	return Reject, nil
}
func ShareMode(s Spec) (tls, trial bool) {
	if proto, ok := Lookup(s.Protocol); ok && IsUDPBound(proto) {
		return false, false
	}
	if _, _, _, ok := TLSMatchKeys(s); ok {
		return true, false
	}
	if s.Protocol == "vmess" {
		return false, true
	}
	if s.Protocol == "shadowsocks" {
		switch cipherName(s.Params) {
		case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
			return false, true
		}
	}
	return false, false
}
func ValidateTrialSecrets(specs []Spec) error {
	seen := map[string]bool{}
	for _, s := range specs {
		_, trial := ShareMode(s)
		if !trial {
			continue
		}
		if s.Protocol == "vmess" && len(s.Accounts) > 1024 {
			return errors.New("shared VMess endpoint exceeds user limit")
		}
		if _, err := NewTrial(s); err != nil && len(s.Users) != 0 {
			return err
		}
		for _, login := range s.Accounts {
			key := ""
			if s.Protocol == "vmess" {
				id, err := uuid.FromString(s.Users[login]["uuid"])
				if err != nil {
					return errors.New("invalid trial secret")
				}
				key = "vmess:" + string(id[:])
			} else {
				b, err := base64.StdEncoding.DecodeString(s.Users[login]["password"])
				if err != nil {
					return errors.New("invalid trial secret")
				}
				key = "ss:" + string(b)
			}
			if seen[key] {
				return fmt.Errorf("shared endpoint has ambiguous trial credentials")
			}
			seen[key] = true
		}
	}
	return nil
}

var _ TrialAuthenticator = shadowsocks{}
