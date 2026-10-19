package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// setFastCost lowers the argon2 cost parameters for the test's
// duration and restores them via cleanup. Only
// TestHashRoundTripRealParams runs with the real parameters.
func setFastCost(t *testing.T) {
	t.Helper()
	om, ot, op := argonMemory, argonTime, argonThreads
	argonMemory, argonTime, argonThreads = 8, 1, 1
	t.Cleanup(func() { argonMemory, argonTime, argonThreads = om, ot, op })
}

func b64n(n int) string { return base64.RawStdEncoding.EncodeToString(make([]byte, n)) }

func validHashFor(t *testing.T, password string) string {
	t.Helper()
	h, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

// hashWithKeyLen builds a real hash with a non-default key length
// under the currently set cost parameters.
func hashWithKeyLen(t *testing.T, password string, n int) string {
	t.Helper()
	salt := make([]byte, saltLen)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, uint32(n))
	return hashString(argonMemory, argonTime, argonThreads, salt, key)
}

func TestHashRoundTripRealParams(t *testing.T) {
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(h, hashPrefix+verField+"$") {
		t.Fatalf("hash prefix = %q", h[:20])
	}
	if !strings.Contains(h, "$"+costString(argonMemory, argonTime, argonThreads)+"$") {
		t.Fatalf("hash does not carry the default cost parameters")
	}
	// $argon2id$v=19$<params>$<salt>$<key> -> 6 fields.
	fields := strings.Split(h, "$")
	if len(fields) != 6 {
		t.Fatalf("field count = %d", len(fields))
	}
	if fields[0] != "" || fields[1] != "argon2id" || fields[2] != "v=19" {
		t.Fatalf("header fields = %q", fields[:3])
	}
	if ok, err := VerifyHash(h, "correct horse"); err != nil || !ok {
		t.Fatalf("VerifyHash(correct) = %v, %v", ok, err)
	}
	if ok, err := VerifyHash(h, "wrong"); err != nil || ok {
		t.Fatalf("VerifyHash(wrong) = %v, %v", ok, err)
	}
}

func TestHashFormat(t *testing.T) {
	setFastCost(t)
	h, err := Hash("secret")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	fields := strings.Split(h, "$")
	if len(fields) != 6 {
		t.Fatalf("field count = %d, want 6", len(fields))
	}
	if fields[0] != "" || fields[1] != "argon2id" || fields[2] != "v=19" {
		t.Fatalf("header fields = %q", fields[:3])
	}
	if fields[3] != costString(argonMemory, argonTime, argonThreads) {
		t.Fatalf("cost field = %q", fields[3])
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(fields[4])
	key, err2 := base64.RawStdEncoding.DecodeString(fields[5])
	if err1 != nil || err2 != nil {
		t.Fatalf("salt/key not raw base64: %v %v", err1, err2)
	}
	if len(salt) != saltLen || len(key) != keyLen {
		t.Fatalf("salt/key len = %d/%d, want %d/%d", len(salt), len(key), saltLen, keyLen)
	}
}

func TestHashTwoHashesDiffer(t *testing.T) {
	setFastCost(t)
	h1 := validHashFor(t, "same")
	h2 := validHashFor(t, "same")
	if h1 == h2 {
		t.Fatal("two hashes of one password are identical (salt not random?)")
	}
}

func TestHashEmptyPassword(t *testing.T) {
	if _, err := Hash(""); err == nil {
		t.Fatal(`Hash("") accepted`)
	}
}

func TestVerifyHashTable(t *testing.T) {
	setFastCost(t)
	good := validHashFor(t, "hunter2")

	cases := []struct {
		name    string
		hash    string
		pass    string
		wantOK  bool
		wantErr bool
	}{
		{"valid", good, "hunter2", true, false},
		{"wrong password", good, "hunter3", false, false},
		{"wrong prefix", strings.Replace(good, "$argon2id$", "$argon2i$", 1), "hunter2", false, true},
		{"no prefix at all", "argon2id" + good, "hunter2", false, true},
		{"double prefix", "$argon2id$" + good, "hunter2", false, true},
		{"missing key field", good[:strings.LastIndex(good, "$")+1], "hunter2", false, true},
		{"extra field", good + "$extra", "hunter2", false, true},
		{"v=16", strings.Replace(good, "v=19", "v=16", 1), "hunter2", false, true},
		{"params out of order", strings.Replace(good, "m=8,t=1,p=1", "t=1,m=8,p=1", 1), "hunter2", false, true},
		{"extra param", strings.Replace(good, "m=8,t=1,p=1", "m=8,t=1,p=1,x=9", 1), "hunter2", false, true},
		{"m over cap", strings.Replace(good, "m=8,", "m=999999,", 1), "hunter2", false, true},
		{"t over cap", strings.Replace(good, "t=1,", "t=99,", 1), "hunter2", false, true},
		{"p over cap", strings.Replace(good, ",p=1", ",p=99", 1), "hunter2", false, true},
		{"salt too short", good[:strings.LastIndex(good[:strings.LastIndex(good, "$")], "$")+1] + b64n(minSaltLen-1) + "$" + b64n(keyLen), "hunter2", false, true},
		{"salt too long", good[:strings.LastIndex(good[:strings.LastIndex(good, "$")], "$")+1] + b64n(maxSaltLen+1) + "$" + b64n(keyLen), "hunter2", false, true},
		{"key too short", good[:strings.LastIndex(good, "$")+1] + b64n(minKeyLen-1), "hunter2", false, true},
		{"key too long", good[:strings.LastIndex(good, "$")+1] + b64n(maxKeyLen+1), "hunter2", false, true},
		{"bad base64 salt", good[:strings.LastIndex(good[:strings.LastIndex(good, "$")], "$")+1] + "!!!" + "$" + b64n(keyLen), "hunter2", false, true},
		{"oversized hash string", strings.Repeat("a", 600), "hunter2", false, true},
		{"16-byte key round trip", hashWithKeyLen(t, "hunter2", 16), "hunter2", true, false},
		{"16-byte key wrong pass", hashWithKeyLen(t, "hunter2", 16), "hunter3", false, false},
		{"empty password", good, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyHash(tc.hash, tc.pass)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if strings.Contains(err.Error(), tc.hash) {
					t.Errorf("error echoes the hash: %v", err)
				}
				if tc.pass != "" && strings.Contains(err.Error(), tc.pass) {
					t.Errorf("error echoes the password: %v", err)
				}
			}
		})
	}
}

func TestVerifyHashNoSecretInErrors(t *testing.T) {
	setFastCost(t)
	password := "s3cret-hunter2-xyz"
	hash := validHashFor(t, password)
	malformed := []string{"", "$argon2id$", hash + "junk", strings.Replace(hash, "argon2id", "argon2x", 1)}
	for _, h := range malformed {
		for _, p := range []string{password, "other"} {
			_, err := VerifyHash(h, p)
			if err == nil {
				continue
			}
			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), hash) {
				t.Fatalf("error %q leaks a secret", err)
			}
		}
	}
}
