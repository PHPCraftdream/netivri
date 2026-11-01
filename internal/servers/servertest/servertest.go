// Package servertest provides helpers for tests of server TLS material and listeners.
package servertest

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/component/ca"
)

// TrustForTest adds the material's certificate to Mihomo's global test CA pool.
// It restores the pool when the test finishes; do not use concurrently with other
// tests that mutate that global pool.
func TrustForTest(t testing.TB, material map[string]string) {
	t.Helper()
	t.Cleanup(ca.ResetCertificate)
	certificate := material["cert_pem"]
	if certificate == "" && material["cert_file"] != "" {
		data, err := os.ReadFile(material["cert_file"])
		if err != nil {
			t.Fatal("could not read test TLS certificate")
		}
		certificate = string(data)
	}
	if certificate == "" {
		t.Fatal("test TLS certificate is unavailable")
	}
	if err := ca.AddCertificate(certificate); err != nil {
		t.Fatal("could not trust test TLS certificate")
	}
}

// FreePort briefly binds loopback TCP port 0 and returns the assigned port.
// The port may be claimed by another process before the caller uses it.
func FreePort(t testing.TB) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not reserve a test port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal("could not release a test port")
	}
	return port
}

// IsAddrInUse reports whether err means the listen address is already taken:
// the OS errors (also through wrapping), the Windows and Unix texts, and the
// core's PORT_IN_USE code, which protocols' redacted start errors may reduce to.
func IsAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048)) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"address already in use", "address in use", "wsaeaddrinuse", "only one usage of each socket address", "port_in_use"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// StartOnFreePort retries a listener start up to five times when the selected
// port is already in use. Other errors fail with the error text, which the
// servers' start errors keep free of secrets.
func StartOnFreePort(t testing.TB, start func(port int) error) int {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		port := FreePort(t)
		err := start(port)
		if err == nil {
			return port
		}
		if !IsAddrInUse(err) {
			t.Fatalf("could not start test listener: %v", err)
		}
	}
	t.Fatal("could not start test listener on an available port")
	return 0
}
