// Package auth implements proxy password authentication for the mixed
// listener: argon2id hashing, strict PHC verification and the "init"
// placeholder flow that persists the first real credential. Pure Go,
// no network; persistence is an injected callback (task #12 wires it).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// InitPlaceholder fills auth.login / auth.password until the first
// client provides the real credentials (docs/config.md).
const InitPlaceholder = "init"

var b64 = base64.RawStdEncoding // PHC strings use unpadded base64

const (
	hashPrefix = "$argon2id$"
	saltLen    = 16
	keyLen     = 32
	algField   = "argon2id"
	verField   = "v=19" // argon2 v1.3, the only version x/crypto emits

	// Caps on cost parameters parsed from a hash: a hostile config
	// file must not be able to trigger a huge allocation through
	// VerifyHash.
	maxMemoryKiB = 262144 // 256 MiB in KiB
	maxTime      = 10
	maxThreads   = 16
	minSaltLen   = 8
	maxSaltLen   = 64
	minKeyLen    = 16
	maxKeyLen    = 64
	minMemoryKiB = 8 // argon2's own minimum

	// maxHashLen bounds the whole PHC string before any split or
	// decode work: the longest valid hash is well under 200 bytes.
	maxHashLen = 512

	// Encoded (unpadded base64) lengths for the byte caps above:
	// n bytes take ceil(n*8/6) characters.
	minSaltEnc = 11 // 8 bytes
	maxSaltEnc = 86 // 64 bytes
	minKeyEnc  = 22 // 16 bytes
	maxKeyEnc  = 86 // 64 bytes
)

// Default cost parameters (19 MiB, 2 passes, 1 lane). Package variables
// so tests can lower them for speed.
var (
	argonMemory  uint32 = 19456
	argonTime    uint32 = 2
	argonThreads uint8  = 1
)

type phcHash struct {
	m, t uint32
	p    uint8
	salt []byte
	key  []byte
}

// parseHash is a strict PHC parser: exact field layout
// $argon2id$v=19$m=<KiB>,t=<iters>,p=<lanes>$<salt>$<key>, no extra or
// missing fields, values within the caps above. Encoded sizes are
// checked before anything is decoded.
func parseHash(hash string) (phcHash, error) {
	if len(hash) == 0 || len(hash) > maxHashLen {
		return phcHash{}, errors.New("auth: malformed argon2id PHC string")
	}
	// $argon2id$v=19$m=..,t=..,p=..$<salt>$<key> -> 6 fields.
	fields := strings.Split(hash, "$")
	if len(fields) != 6 || fields[0] != "" {
		return phcHash{}, errors.New("auth: malformed argon2id PHC string")
	}
	if fields[1] != algField {
		return phcHash{}, errors.New("auth: unsupported PHC algorithm (want argon2id)")
	}
	if fields[2] != verField {
		return phcHash{}, errors.New("auth: unsupported argon2 version (want v=19)")
	}
	params := strings.Split(fields[3], ",")
	if len(params) != 3 ||
		!strings.HasPrefix(params[0], "m=") ||
		!strings.HasPrefix(params[1], "t=") ||
		!strings.HasPrefix(params[2], "p=") {
		return phcHash{}, errors.New("auth: malformed cost parameters (want m=<KiB>,t=<iters>,p=<lanes>)")
	}
	m64, err := strconv.ParseUint(params[0][2:], 10, 32)
	if err != nil {
		return phcHash{}, errors.New("auth: invalid m cost parameter")
	}
	t64, err := strconv.ParseUint(params[1][2:], 10, 32)
	if err != nil {
		return phcHash{}, errors.New("auth: invalid t cost parameter")
	}
	p64, err := strconv.ParseUint(params[2][2:], 10, 8)
	if err != nil {
		return phcHash{}, errors.New("auth: invalid p cost parameter")
	}
	if m64 < minMemoryKiB || m64 > maxMemoryKiB {
		return phcHash{}, errors.New("auth: m cost parameter out of range")
	}
	if t64 == 0 || t64 > maxTime {
		return phcHash{}, errors.New("auth: t cost parameter out of range")
	}
	if p64 == 0 || p64 > maxThreads {
		return phcHash{}, errors.New("auth: p cost parameter out of range")
	}
	// Reject oversized input before decoding anything.
	if len(fields[4]) < minSaltEnc || len(fields[4]) > maxSaltEnc {
		return phcHash{}, errors.New("auth: salt length out of range")
	}
	if len(fields[5]) < minKeyEnc || len(fields[5]) > maxKeyEnc {
		return phcHash{}, errors.New("auth: key length out of range")
	}
	salt, err := b64.DecodeString(fields[4])
	if err != nil {
		return phcHash{}, errors.New("auth: salt is not valid base64")
	}
	key, err := b64.DecodeString(fields[5])
	if err != nil {
		return phcHash{}, errors.New("auth: key is not valid base64")
	}
	if len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return phcHash{}, errors.New("auth: salt length out of range")
	}
	if len(key) < minKeyLen || len(key) > maxKeyLen {
		return phcHash{}, errors.New("auth: key length out of range")
	}
	return phcHash{m: uint32(m64), t: uint32(t64), p: uint8(p64), salt: salt, key: key}, nil
}

func costString(m uint32, t uint32, p uint8) string {
	return fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p)
}

// Hash derives an argon2id PHC string for password:
// $argon2id$v=19$m=<KiB>,t=<iters>,p=<lanes>$<salt>$<key> with a fresh
// 16-byte random salt and a 32-byte key, unpadded base64.
func Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: reading salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLen)
	return hashString(argonMemory, argonTime, argonThreads, salt, key), nil
}

func hashString(m uint32, t uint32, p uint8, salt, key []byte) string {
	return hashPrefix + verField + "$" + costString(m, t, p) + "$" +
		b64.EncodeToString(salt) + "$" + b64.EncodeToString(key)
}

// VerifyHash reports whether password matches hash. Errors are
// returned for malformed hashes and never echo the hash or password.
func VerifyHash(hash, password string) (bool, error) {
	if password == "" {
		return false, errors.New("auth: password must not be empty")
	}
	h, err := parseHash(hash)
	if err != nil {
		return false, err
	}
	key := argon2.IDKey([]byte(password), h.salt, h.t, h.m, h.p, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(key, h.key) == 1, nil
}

// ctEqual compares strings in constant time; different lengths always
// differ (subtle.ConstantTimeCompare handles that case too).
func ctEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
