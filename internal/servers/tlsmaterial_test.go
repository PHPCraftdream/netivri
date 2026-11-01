package servers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/ca"
)

func TestGeneratedTLSMaterialAndSAN(t *testing.T) {
	cases := []struct {
		host string
		dns  []string
		ips  []string
	}{
		{"edge.example.test", []string{"edge.example.test"}, nil},
		{"127.0.0.1", nil, []string{"127.0.0.1"}},
		{"2001:db8::7", nil, []string{"2001:db8::7"}},
		{"", []string{"localhost"}, []string{"127.0.0.1"}},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		material, err := NewTLSMaterial(nil, tc.host)
		if err != nil {
			t.Fatal("generation failed")
		}
		certPEM, keyPEM := TLSFiles(material)
		pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			t.Fatal("generated pair invalid")
		}
		leaf, err := parsePairLeaf(pair)
		if err != nil {
			t.Fatal("generated leaf invalid")
		}
		pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			t.Fatal("certificate key is not P-256 ECDSA")
		}
		if !equalStrings(leaf.DNSNames, tc.dns) || len(leaf.IPAddresses) != len(tc.ips) {
			t.Fatal("SAN names differ")
		}
		for i, want := range tc.ips {
			if !leaf.IPAddresses[i].Equal(net.ParseIP(want)) {
				t.Fatal("SAN IP differs")
			}
		}
		if leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
			t.Fatal("certificate usages differ")
		}
		if leaf.SerialNumber.Sign() <= 0 || seen[leaf.SerialNumber.String()] {
			t.Fatal("certificate serial is not unique and positive")
		}
		seen[leaf.SerialNumber.String()] = true
		if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
			t.Fatal("certificate is not self-signed")
		}
		now := time.Now()
		if leaf.NotAfter.Before(now.AddDate(9, 11, 0)) || leaf.NotAfter.After(now.AddDate(10, 0, 1)) {
			t.Fatal("certificate validity is not approximately ten years")
		}
		if material[tlsFingerprintKey] != ca.CalculateFingerprint(leaf.Raw) || TLSFingerprint(material) != material[tlsFingerprintKey] {
			t.Fatal("certificate fingerprint differs")
		}
		expiry, err := TLSExpiry(material)
		if err != nil || !expiry.Equal(leaf.NotAfter) {
			t.Fatal("certificate expiry differs")
		}
	}
	if _, err := NewTLSMaterial(nil, "invalid/name"); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatal("invalid generation host did not produce named error")
	}
}

func TestTLSParamValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    map[string]string
		ok   bool
	}{
		{"absent", nil, true},
		{"paths", map[string]string{"cert_file": "c", "key_file": "k"}, true},
		{"cert only", map[string]string{"cert_file": "c"}, false},
		{"key only", map[string]string{"key_file": "k"}, false},
		{"empty cert", map[string]string{"cert_file": "", "key_file": "k"}, false},
		{"empty key", map[string]string{"cert_file": "c", "key_file": ""}, false},
		{"valid dns", map[string]string{"sni": "sub.example.test"}, true},
		{"valid ipv4", map[string]string{"sni": "127.0.0.1"}, true},
		{"valid ipv6", map[string]string{"sni": "2001:db8::7"}, true},
		{"empty sni", map[string]string{"sni": ""}, false},
		{"bad sni", map[string]string{"sni": "bad/name"}, false},
		{"valid alpn", map[string]string{"alpn": "h2,http/1.1"}, true},
		{"alpn max", map[string]string{"alpn": strings.Repeat("a", 255)}, true},
		{"alpn empty", map[string]string{"alpn": ""}, false},
		{"alpn empty token", map[string]string{"alpn": "h2,,http/1.1"}, false},
		{"alpn space", map[string]string{"alpn": "bad token"}, false},
		{"alpn control", map[string]string{"alpn": "a\x7fb"}, false},
		{"alpn too long", map[string]string{"alpn": strings.Repeat("a", 256)}, false},
		{"unknown ignored", map[string]string{"protocol-option": "anything"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTLSParams(tc.p)
			if (err == nil) != tc.ok {
				t.Fatal("unexpected parameter validation result")
			}
		})
	}
}

func TestProvidedTLSPathsAndRefresh(t *testing.T) {
	material, err := NewTLSMaterial(nil, "provided.test")
	if err != nil {
		t.Fatal("generation failed")
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeSecretFixture(t, certPath, material[tlsCertKey])
	writeSecretFixture(t, keyPath, material[tlsKeyKey])
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("working directory unavailable")
	}
	relCert, relCertErr := filepath.Rel(cwd, certPath)
	relKey, relKeyErr := filepath.Rel(cwd, keyPath)
	inputCert, inputKey := certPath, keyPath
	if relCertErr == nil && relKeyErr == nil {
		inputCert, inputKey = relCert, relKey
	}
	provided, err := NewTLSMaterial(map[string]string{"cert_file": inputCert, "key_file": inputKey}, "ignored")
	if err != nil {
		t.Fatal("valid file pair rejected")
	}
	if provided["cert_file"] != certPath || provided["key_file"] != keyPath || provided[tlsCertKey] != "" || provided[tlsKeyKey] != "" {
		t.Fatal("provided pair was not retained as absolute paths only")
	}
	cert, key := TLSFiles(provided)
	if cert != certPath || key != keyPath {
		t.Fatal("TLSFiles did not return paths")
	}
	if TLSFingerprint(provided) != material[tlsFingerprintKey] {
		t.Fatal("provided pair fingerprint differs")
	}
	oldExpiry, err := TLSExpiry(provided)
	if err != nil {
		t.Fatal("provided pair expiry unavailable")
	}
	changed, err := makeMaterialWithExpiry(t, "provided.test", time.Now().AddDate(3, 0, 0))
	if err != nil {
		t.Fatal("replacement fixture generation failed")
	}
	writeSecretFixture(t, certPath, changed[tlsCertKey])
	writeSecretFixture(t, keyPath, changed[tlsKeyKey])
	if TLSFingerprint(provided) != changed[tlsFingerprintKey] {
		t.Fatal("fingerprint did not follow updated certificate file")
	}
	newExpiry, err := TLSExpiry(provided)
	if err != nil || !newExpiry.Before(oldExpiry) {
		t.Fatal("expiry did not follow updated certificate file")
	}
	if _, err := NewTLSMaterial(map[string]string{"cert_file": filepath.Join(dir, "missing-cert"), "key_file": keyPath}, "host"); err == nil || !strings.Contains(err.Error(), "cert_file") || strings.Contains(err.Error(), "missing-cert") {
		t.Fatal("certificate path error missing key or disclosed path")
	}
	if _, err := NewTLSMaterial(map[string]string{"cert_file": certPath, "key_file": filepath.Join(dir, "missing-key")}, "host"); err == nil || !strings.Contains(err.Error(), "key_file") || strings.Contains(err.Error(), "missing-key") {
		t.Fatal("key path error missing key or disclosed path")
	}
	other, err := NewTLSMaterial(nil, "other.test")
	if err != nil {
		t.Fatal("mismatch fixture generation failed")
	}
	writeSecretFixture(t, keyPath, other[tlsKeyKey])
	if _, err := NewTLSMaterial(map[string]string{"cert_file": certPath, "key_file": keyPath}, "host"); err == nil || !strings.Contains(err.Error(), "cert_file") || !strings.Contains(err.Error(), "key_file") {
		t.Fatal("mismatched pair error did not name fields")
	}
	writeSecretFixture(t, keyPath, "invalid key material")
	if _, err := NewTLSMaterial(map[string]string{"cert_file": certPath, "key_file": keyPath}, "host"); err == nil || !strings.Contains(err.Error(), "cert_file") || !strings.Contains(err.Error(), "key_file") || strings.Contains(err.Error(), "invalid key material") {
		t.Fatal("malformed pair error did not name fields or disclosed content")
	}
	writeSecretFixture(t, keyPath, material[tlsKeyKey])
	writeSecretFixture(t, certPath, "invalid cert material")
	if _, err := NewTLSMaterial(map[string]string{"cert_file": certPath, "key_file": keyPath}, "host"); err == nil || !strings.Contains(err.Error(), "cert_file") || !strings.Contains(err.Error(), "key_file") || strings.Contains(err.Error(), "invalid cert material") {
		t.Fatal("malformed certificate error did not name fields or disclosed content")
	}
}

func TestTLSExpiryAndFingerprintMalformed(t *testing.T) {
	valid, err := makeMaterialWithExpiry(t, "leaf.test", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal("leaf fixture generation failed")
	}
	delete(valid, tlsKeyKey)
	if _, err := TLSExpiry(valid); err != nil || TLSFingerprint(valid) == "" {
		t.Fatal("certificate-only PEM did not provide expiry and fingerprint")
	}
	for _, material := range []map[string]string{{}, {"cert_pem": "invalid"}, {"cert_pem": "invalid", "key_pem": "bad"}} {
		if _, err := TLSExpiry(material); err == nil || !strings.Contains(err.Error(), "cert_pem") {
			t.Fatal("malformed material did not produce named error")
		}
		if TLSFingerprint(material) != "" {
			t.Fatal("invalid material returned fingerprint")
		}
	}
	validBlock, _ := pem.Decode([]byte(valid[tlsCertKey]))
	malformedFirst := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad DER")})) + string(valid[tlsCertKey])
	if _, err := TLSExpiry(map[string]string{"cert_pem": malformedFirst}); err == nil || !strings.Contains(err.Error(), "cert_pem") || TLSFingerprint(map[string]string{"cert_pem": malformedFirst}) != "" {
		t.Fatal("malformed first certificate block was not rejected")
	}
	withPreamble := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("ignored")})) + string(pem.EncodeToMemory(validBlock))
	if _, err := TLSExpiry(map[string]string{"cert_pem": withPreamble}); err != nil || TLSFingerprint(map[string]string{"cert_pem": withPreamble}) == "" {
		t.Fatal("non-certificate PEM block prevented valid leaf parsing")
	}
	if TLSFingerprint(map[string]string{"cert_file": filepath.Join(t.TempDir(), "absent"), tlsFingerprintKey: strings.Repeat("a", 64)}) != "" {
		t.Fatal("unreadable file returned cached fingerprint")
	}
	if _, err := RegenerateIfExpiring(map[string]string{"cert_pem": "invalid", "key_pem": "bad"}, "host", time.Hour); err == nil {
		t.Fatal("rotation accepted malformed material")
	}
}

func TestTLSRotation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		delta  time.Duration
		within time.Duration
		rotate bool
	}{
		{"outside", 48 * time.Hour, 24 * time.Hour, false},
		{"inside", 12 * time.Hour, 24 * time.Hour, true},
		{"expired at zero", -time.Hour, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			material, err := makeMaterialWithExpiry(t, "rotate.test", time.Now().Add(tc.delta))
			if err != nil {
				t.Fatal("controlled fixture generation failed")
			}
			material["unrelated"] = "kept"
			before := cloneMap(material)
			got, err := RegenerateIfExpiring(material, "rotate.test", tc.within)
			if err != nil {
				t.Fatal("rotation evaluation failed")
			}
			if (got[tlsFingerprintKey] != material[tlsFingerprintKey]) != tc.rotate || got["unrelated"] != "kept" || !equalMap(material, before) {
				t.Fatal("rotation result or input preservation differs")
			}
		})
	}
	if _, err := RegenerateIfExpiring(nil, "host", -time.Second); err == nil {
		t.Fatal("negative renewal window accepted")
	}
	filePair, err := NewTLSMaterial(nil, "file.test")
	if err != nil {
		t.Fatal("file fixture generation failed")
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeSecretFixture(t, certPath, filePair[tlsCertKey])
	writeSecretFixture(t, keyPath, filePair[tlsKeyKey])
	provided, err := NewTLSMaterial(map[string]string{"cert_file": certPath, "key_file": keyPath}, "host")
	if err != nil {
		t.Fatal("file pair rejected")
	}
	unchanged, err := RegenerateIfExpiring(provided, "host", 20*365*24*time.Hour)
	if err != nil || !equalMap(unchanged, provided) {
		t.Fatal("user-provided pair was replaced")
	}
}

func TestTLSHandshakeOverPipe(t *testing.T) {
	material, err := NewTLSMaterial(nil, "localhost")
	if err != nil {
		t.Fatal("generation failed")
	}
	certPEM, keyPEM := TLSFiles(material)
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal("generated pair invalid")
	}
	if _, err := parsePairLeaf(cert); err != nil {
		t.Fatal("generated leaf invalid")
	}
	ca.ResetCertificate()
	t.Cleanup(ca.ResetCertificate)
	if err := ca.AddCertificate(string(certPEM)); err != nil {
		t.Fatal("test certificate was not added to CA pool")
	}
	pool := ca.GetCertPool().Clone()
	runPipeHandshake(t, cert, &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}, true)
	verifier, err := ca.NewFingerprintVerifier(material[tlsFingerprintKey], nil)
	if err != nil {
		t.Fatal("pin verifier setup failed")
	}
	runPipeHandshake(t, cert, &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error { return verifier(s.PeerCertificates, "") }}, true)
	wrongVerifier, err := ca.NewFingerprintVerifier(strings.Repeat("0", 64), nil)
	if err != nil {
		t.Fatal("wrong pin verifier setup failed")
	}
	runPipeHandshake(t, cert, &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error { return wrongVerifier(s.PeerCertificates, "") }}, false)
	runPipeHandshake(t, cert, &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost"}, false)
	runPipeHandshake(t, cert, &tls.Config{RootCAs: pool, ServerName: "wrong.test"}, false)
}

func runPipeHandshake(t *testing.T, cert tls.Certificate, clientConfig *tls.Config, wantSuccess bool) {
	t.Helper()
	serverRaw, clientRaw := net.Pipe()
	deadline := time.Now().Add(3 * time.Second)
	_ = serverRaw.SetDeadline(deadline)
	_ = clientRaw.SetDeadline(deadline)
	server := tls.Server(serverRaw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	client := tls.Client(clientRaw, clientConfig)
	type result struct {
		client bool
		err    error
	}
	results := make(chan result, 2)
	go func() { results <- result{err: server.Handshake()} }()
	go func() { results <- result{client: true, err: client.Handshake()} }()
	var clientErr, serverErr error
	for range 2 {
		got := <-results
		if got.client {
			clientErr = got.err
		} else {
			serverErr = got.err
		}
	}
	_ = serverRaw.Close()
	_ = clientRaw.Close()
	if wantSuccess && (clientErr != nil || serverErr != nil) {
		t.Fatal("expected TLS handshake to succeed")
	}
	if !wantSuccess && clientErr == nil {
		t.Fatal("expected TLS client to reject handshake")
	}
}

func makeMaterialWithExpiry(t *testing.T, host string, expiry time.Time) (map[string]string, error) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 159))
	if err != nil {
		return nil, err
	}
	serial.Add(serial, big.NewInt(1))
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              expiry,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		tlsCertKey:        string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		tlsKeyKey:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		tlsFingerprintKey: ca.CalculateFingerprint(der),
	}, nil
}

func writeSecretFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal("could not write TLS fixture")
	}
}
