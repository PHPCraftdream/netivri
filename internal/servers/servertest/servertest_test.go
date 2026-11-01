package servertest_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/component/ca"
)

func TestTrustForTestPEMAndFile(t *testing.T) {
	for _, fileBacked := range []bool{false, true} {
		name := "pem"
		if fileBacked {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			material, err := servers.NewTLSMaterial(nil, "trust.example.test")
			if err != nil {
				t.Fatal("could not generate TLS fixture")
			}
			if fileBacked {
				dir := t.TempDir()
				cert, key := servers.TLSFiles(material)
				certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
				if err := os.WriteFile(certPath, []byte(cert), 0600); err != nil {
					t.Fatal("could not write TLS fixture")
				}
				if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
					t.Fatal("could not write TLS fixture")
				}
				material = map[string]string{"cert_file": certPath, "key_file": keyPath}
			}
			certPEM := material["cert_pem"]
			if certPEM == "" {
				data, err := os.ReadFile(material["cert_file"])
				if err != nil {
					t.Fatal("could not read TLS fixture")
				}
				certPEM = string(data)
			}
			block, _ := pem.Decode([]byte(certPEM))
			if block == nil {
				t.Fatal("TLS certificate fixture invalid")
			}
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal("TLS certificate fixture invalid")
			}
			t.Run("trust", func(t *testing.T) {
				servertest.TrustForTest(t, material)
				trusted := ca.GetCertPool().Clone()
				if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "trust.example.test", Roots: trusted}); err != nil {
					t.Fatal("certificate was not trusted")
				}
			})
			current := ca.GetCertPool().Clone()
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "trust.example.test", Roots: current}); err == nil {
				t.Fatal("test certificate remained trusted after cleanup")
			}
		})
	}
}
