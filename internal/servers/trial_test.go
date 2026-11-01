package servers

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/sing-shadowsocks/shadowaead_2022"
	"golang.org/x/crypto/chacha20poly1305"
)

type trialMemConn struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *trialMemConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *trialMemConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}
func (c *trialMemConn) Close() error                     { return nil }
func (c *trialMemConn) LocalAddr() net.Addr              { return trialMemAddr("local") }
func (c *trialMemConn) RemoteAddr() net.Addr             { return trialMemAddr("remote") }
func (c *trialMemConn) SetDeadline(time.Time) error      { return nil }
func (c *trialMemConn) SetReadDeadline(time.Time) error  { return nil }
func (c *trialMemConn) SetWriteDeadline(time.Time) error { return nil }
func (c *trialMemConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.b.Bytes()...)
}

type trialMemAddr string

func (a trialMemAddr) Network() string { return "memory" }
func (a trialMemAddr) String() string  { return string(a) }

func TestTrialVMessFromMihomoAdapter(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	adapter, err := outbound.NewVmess(outbound.VmessOption{Name: "fixture", Server: "127.0.0.1", Port: 443, UUID: id, AlterID: 0, Cipher: "aes-128-gcm", Network: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	conn := &trialMemConn{}
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr("192.0.2.8"), DstPort: 443}
	wrapped, err := adapter.StreamConnContext(context.Background(), conn, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrapped.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = wrapped.Close()
	flight := conn.bytes()
	trial, err := NewTrial(Spec{Protocol: "vmess", Accounts: []string{"u"}, Users: UserSecrets{"u": {"uuid": id}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(flight) < 42 {
		t.Fatalf("adapter flight length %d", len(flight))
	}
	key := trial.(*trialAuth).keys[0]
	var authID [16]byte
	key.auth.Decrypt(authID[:], flight[:16])
	now := time.Unix(int64(binary.BigEndian.Uint64(authID[:8])), 0)
	if v, _ := trial.Try(flight, now); v != Accept {
		t.Fatalf("Mihomo first flight: %v", v)
	}
	if v, _ := trial.Try(flight, now); v != Accept {
		t.Fatalf("idempotent retry: %v", v)
	}
	wrong, _ := NewTrial(Spec{Protocol: "vmess", Accounts: []string{"x"}, Users: UserSecrets{"x": {"uuid": "550e8400-e29b-41d4-a716-446655440001"}}})
	if v, _ := wrong.Try(flight, now); v != Reject {
		t.Fatalf("wrong UUID: %v", v)
	}
	bad := append([]byte(nil), flight...)
	bad[16] ^= 0x80
	if v, _ := trial.Try(bad, now); v != Reject {
		t.Fatalf("corrupt AEAD length: %v", v)
	}
	if v, _ := trial.Try(flight, now.Add(119*time.Second)); v != Reject {
		t.Fatalf("skew: %v", v)
	}
	for n := 0; n < 42; n++ {
		if v, _ := trial.Try(flight[:n], now); v != NeedMore {
			t.Fatalf("short prefix %d: %v", n, v)
		}
	}
	if v, _ := trial.Try(bytes.Repeat([]byte{0x77}, 42), now); v != Reject {
		t.Fatal("random prefix accepted")
	}
	tls := append([]byte{0x16, 3, 3}, bytes.Repeat([]byte{0}, 39)...)
	if v, _ := trial.Try(tls, now); v != Reject {
		t.Fatal("TLS prefix accepted")
	}
}

func mihomoSSFirstFlight(t *testing.T, method, password string) []byte {
	t.Helper()
	adapter, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "fixture", Server: "127.0.0.1", Port: 443, Password: password, Cipher: method})
	if err != nil {
		t.Fatal(err)
	}
	conn := &trialMemConn{}
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr("192.0.2.8"), DstPort: 443}
	wrapped, err := adapter.StreamConnContext(context.Background(), conn, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrapped.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = wrapped.Close()
	return conn.bytes()
}

func TestTrialShadowsocksAll2022Methods(t *testing.T) {
	for _, tc := range []struct {
		method string
		size   int
	}{{"2022-blake3-aes-128-gcm", 16}, {"2022-blake3-aes-256-gcm", 32}, {"2022-blake3-chacha20-poly1305", 32}} {
		t.Run(tc.method, func(t *testing.T) {
			psk := bytes.Repeat([]byte{0x42}, tc.size)
			password := base64.StdEncoding.EncodeToString(psk)
			flight := mihomoSSFirstFlight(t, tc.method, password)
			trial, err := NewTrial(Spec{Protocol: "shadowsocks", Params: map[string]string{"cipher": tc.method}, Accounts: []string{"u"}, Users: UserSecrets{"u": {"password": password}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(flight) < trial.MinBytes() {
				t.Fatalf("adapter flight length %d, want at least authenticated prefix %d", len(flight), trial.MinBytes())
			}
			if len(flight) <= trial.MinBytes() {
				t.Fatalf("adapter flight length %d, want suffix beyond authenticated prefix %d", len(flight), trial.MinBytes())
			}
			salt := flight[:tc.size]
			session := shadowaead_2022.SessionKey(psk, salt, tc.size)
			plain, err := func() ([]byte, error) {
				var a cipher.AEAD
				switch tc.method {
				case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm":
					b, e := aes.NewCipher(session)
					if e != nil {
						return nil, e
					}
					a, e = cipher.NewGCM(b)
					if e != nil {
						return nil, e
					}
				case "2022-blake3-chacha20-poly1305":
					a, err = chacha20poly1305.New(session)
					if err != nil {
						return nil, err
					}
				}
				return a.Open(nil, make([]byte, a.NonceSize()), flight[tc.size:tc.size+27], nil)
			}()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(int64(binary.BigEndian.Uint64(plain[1:9])), 0)
			if v, _ := trial.Try(flight, now); v != Accept {
				t.Fatalf("Mihomo flight: %v", v)
			}
			if v, _ := trial.Try(flight, now); v != Accept {
				t.Fatalf("idempotent retry: %v", v)
			}
			wrongKey := bytes.Repeat([]byte{0x43}, tc.size)
			wrong, _ := NewTrial(Spec{Protocol: "shadowsocks", Params: map[string]string{"cipher": tc.method}, Accounts: []string{"x"}, Users: UserSecrets{"x": {"password": base64.StdEncoding.EncodeToString(wrongKey)}}})
			if v, _ := wrong.Try(flight, now); v != Reject {
				t.Fatalf("wrong PSK: %v", v)
			}
			// The trial authenticates only MinBytes; later server parsing rejects suffix corruption.
			badPrefix := append([]byte(nil), flight...)
			badPrefix[0] ^= 1 // salt
			if v, _ := trial.Try(badPrefix, now); v != Reject {
				t.Fatalf("corrupt salt: %v", v)
			}
			badPrefix = append([]byte(nil), flight...)
			badPrefix[tc.size] ^= 1 // fixed authenticated ciphertext
			if v, _ := trial.Try(badPrefix, now); v != Reject {
				t.Fatalf("corrupt fixed ciphertext: %v", v)
			}
			badSuffix := append([]byte(nil), flight...)
			badSuffix[len(badSuffix)-1] ^= 1
			if v, _ := trial.Try(badSuffix, now); v != Accept {
				t.Fatalf("corrupt suffix beyond authenticated prefix: %v", v)
			}
			if v, _ := trial.Try(flight, now.Add(29*time.Second)); v != Reject {
				t.Fatalf("skew: %v", v)
			}
			for n := 0; n < trial.MinBytes(); n++ {
				if v, _ := trial.Try(flight[:n], now); v != NeedMore {
					t.Fatalf("short prefix %d: %v", n, v)
				}
			}
			if v, _ := trial.Try(bytes.Repeat([]byte{0x7a}, trial.MinBytes()), now); v != Reject {
				t.Fatal("random prefix accepted")
			}
			tls := append([]byte{0x16, 3, 3}, bytes.Repeat([]byte{0}, trial.MinBytes()-3)...)
			if v, _ := trial.Try(tls, now); v != Reject {
				t.Fatal("TLS prefix accepted")
			}
			if tc.method == "2022-blake3-aes-128-gcm" {
				tlsSalt := append([]byte{0x16, 3, 3}, bytes.Repeat([]byte{0x11}, tc.size-3)...)
				tlsSession := shadowaead_2022.SessionKey(psk, tlsSalt, tc.size)
				block, e := aes.NewCipher(tlsSession)
				if e != nil {
					t.Fatal(e)
				}
				tlsAEAD, e := cipher.NewGCM(block)
				if e != nil {
					t.Fatal(e)
				}
				fixed := make([]byte, 11)
				fixed[0] = shadowaead_2022.HeaderTypeClient
				binary.BigEndian.PutUint64(fixed[1:9], uint64(now.Unix()))
				collisionFlight := append(append([]byte(nil), tlsSalt...), tlsAEAD.Seal(nil, make([]byte, tlsAEAD.NonceSize()), fixed, nil)...)
				if v, _ := trial.Try(collisionFlight, now); v != Accept {
					t.Fatalf("valid TLS-prefix salt: %v", v)
				}
			}
		})
	}
}
