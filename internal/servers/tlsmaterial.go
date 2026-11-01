package servers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metacubex/mihomo/component/ca"
)

const (
	tlsCertKey           = "cert_pem"
	tlsKeyKey            = "key_pem"
	tlsCertFileKey       = "cert_file"
	tlsPrivateKeyFileKey = "key_file"
	tlsFingerprintKey    = "fingerprint"
)

// tlsMatchKeyValues normalizes configured TLS routing values without interpreting protocol parameters.
func tlsMatchKeyValues(sniValue, alpnValue string, explicit bool) ([]string, []string, bool) {
	sni := strings.Split(sniValue, ",")
	for i := range sni {
		sni[i] = strings.ToLower(strings.TrimSpace(sni[i]))
	}
	if len(sni) == 1 && sni[0] == "" {
		sni = []string{""}
	}
	alpn := strings.Split(alpnValue, ",")
	for i := range alpn {
		alpn[i] = strings.TrimSpace(alpn[i])
	}
	if len(alpn) == 1 && alpn[0] == "" {
		alpn = nil
	}
	// A key that no TLS client puts on the wire (an IP literal is never sent as
	// SNI) cannot route a link to its server: it is not an explicit key.
	for _, name := range sni {
		if name == "" || net.ParseIP(name) != nil {
			explicit = false
		}
	}
	return sni, alpn, explicit
}

// ValidateTLSParams validates shared TLS listener parameters without interpreting
// protocol-specific fields.
func ValidateTLSParams(params map[string]string) error {
	if params == nil {
		return nil
	}
	cert, certOK := params[tlsCertFileKey]
	key, keyOK := params[tlsPrivateKeyFileKey]
	if certOK != keyOK || (certOK && (cert == "" || key == "")) {
		return errors.New("cert_file and key_file must both be provided and non-empty")
	}
	if sni, ok := params["sni"]; ok && !validTLSHost(sni) {
		return errors.New("invalid sni")
	}
	if alpn, ok := params["alpn"]; ok {
		if alpn == "" {
			return errors.New("invalid alpn")
		}
		for _, token := range strings.Split(alpn, ",") {
			if len(token) < 1 || len(token) > 255 {
				return errors.New("invalid alpn")
			}
			for _, b := range []byte(token) {
				if b <= 0x20 || b >= 0x7f {
					return errors.New("invalid alpn")
				}
			}
		}
	}
	return nil
}

func validTLSHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// NewTLSMaterial returns an existing validated file pair or generates a
// self-signed P-256 server certificate.
func NewTLSMaterial(params map[string]string, host string) (map[string]string, error) {
	if err := ValidateTLSParams(params); err != nil {
		return nil, err
	}
	if certPath, provided := params[tlsCertFileKey]; provided {
		absCert, err := filepath.Abs(certPath)
		if err != nil {
			return nil, errors.New("cert_file path resolution failed")
		}
		absKey, err := filepath.Abs(params[tlsPrivateKeyFileKey])
		if err != nil {
			return nil, errors.New("key_file path resolution failed")
		}
		certBytes, err := os.ReadFile(absCert)
		if err != nil {
			return nil, errors.New("cert_file could not be read")
		}
		keyBytes, err := os.ReadFile(absKey)
		if err != nil {
			return nil, errors.New("key_file could not be read")
		}
		pair, err := tls.X509KeyPair(certBytes, keyBytes)
		if err != nil {
			return nil, errors.New("cert_file and key_file are not a valid matching pair")
		}
		leaf, err := parsePairLeaf(pair)
		if err != nil {
			return nil, errors.New("cert_file does not contain a valid leaf certificate")
		}
		return map[string]string{
			tlsCertFileKey:       absCert,
			tlsPrivateKeyFileKey: absKey,
			tlsFingerprintKey:    ca.CalculateFingerprint(leaf.Raw),
		}, nil
	}
	if host != "" && !validTLSHost(host) {
		return nil, errors.New("invalid host")
	}
	if host == "" {
		host = "localhost"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("generate TLS private key failed")
	}
	limit := new(big.Int).Lsh(big.NewInt(1), 159)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, errors.New("generate TLS serial failed")
	}
	serial.Add(serial, big.NewInt(1))
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	if host == "localhost" {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, errors.New("create TLS certificate failed")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, errors.New("encode TLS private key failed")
	}
	return map[string]string{
		tlsCertKey:        string(pemEncode("CERTIFICATE", der)),
		tlsKeyKey:         string(pemEncode("PRIVATE KEY", keyDER)),
		tlsFingerprintKey: ca.CalculateFingerprint(der),
	}, nil
}

// pemEncode keeps PEM encoding centralized for generated certificate material.
func pemEncode(kind string, data []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data})
}

func parsePairLeaf(pair tls.Certificate) (*x509.Certificate, error) {
	if len(pair.Certificate) == 0 {
		return nil, errors.New("missing leaf")
	}
	if pair.Leaf != nil {
		return pair.Leaf, nil
	}
	return x509.ParseCertificate(pair.Certificate[0])
}

func readTLSLeaf(material map[string]string) (*x509.Certificate, error) {
	var certPEM []byte
	field := tlsCertKey
	if material[tlsCertKey] != "" {
		certPEM = []byte(material[tlsCertKey])
	} else if material[tlsCertFileKey] != "" {
		field = tlsCertFileKey
		var err error
		certPEM, err = os.ReadFile(material[tlsCertFileKey])
		if err != nil {
			return nil, errors.New("cert_file could not be read")
		}
	} else {
		return nil, errors.New("cert_pem or cert_file is required")
	}
	leaf, err := parseCertificateLeaf(certPEM)
	if err != nil {
		return nil, errors.New(field + " does not contain a valid certificate")
	}
	return leaf, nil
}

func parseCertificateLeaf(certPEM []byte) (*x509.Certificate, error) {
	remainder := certPEM
	for len(remainder) > 0 {
		block, rest := pem.Decode(remainder)
		if block == nil {
			break
		}
		remainder = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		return cert, nil
	}
	return nil, errors.New("invalid certificate")
}

// TLSFiles returns values in the PEM-or-path form consumed by listeners.
func TLSFiles(material map[string]string) (certificate, privateKey string) {
	if material[tlsCertKey] != "" {
		return material[tlsCertKey], material[tlsKeyKey]
	}
	return material[tlsCertFileKey], material[tlsPrivateKeyFileKey]
}

// TLSExpiry returns the expiry of the current leaf certificate.
func TLSExpiry(material map[string]string) (time.Time, error) {
	leaf, err := readTLSLeaf(material)
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}

// TLSFingerprint calculates the current leaf's SHA-256 DER fingerprint.
func TLSFingerprint(material map[string]string) string {
	leaf, err := readTLSLeaf(material)
	if err != nil {
		return ""
	}
	return ca.CalculateFingerprint(leaf.Raw)
}

// RegenerateIfExpiring renews generated material at or inside the requested
// threshold. User-provided file pairs are never replaced.
func RegenerateIfExpiring(material map[string]string, host string, within time.Duration) (map[string]string, error) {
	if within < 0 {
		return nil, errors.New("invalid TLS renewal window")
	}
	if material[tlsCertFileKey] != "" || material[tlsPrivateKeyFileKey] != "" {
		return cloneMap(material), nil
	}
	expiry, err := TLSExpiry(material)
	if err != nil {
		return nil, err
	}
	if time.Until(expiry) > within {
		return cloneMap(material), nil
	}
	fresh, err := NewTLSMaterial(nil, host)
	if err != nil {
		return nil, err
	}
	for k, v := range material {
		if k != tlsCertKey && k != tlsKeyKey && k != tlsFingerprintKey {
			fresh[k] = v
		}
	}
	return fresh, nil
}
