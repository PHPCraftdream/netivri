// Package servertest provides helpers for tests of server TLS material and listeners.
package servertest

import (
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
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

// freePortOSAttempts is how many times FreePort lets the OS pick the TCP port
// before it falls back to candidates from a fixed range.
const freePortOSAttempts = 20

// freePortAttempts bounds the whole search.
const freePortAttempts = 200

// freePortCandidate is the port to try on an attempt: 0 asks the OS for a TCP
// port, later attempts pick from a range that Windows rarely excludes for UDP.
func freePortCandidate(attempt int) int {
	if attempt < freePortOSAttempts {
		return 0
	}
	return 20000 + rand.IntN(20000)
}

// FreePort briefly binds loopback TCP and returns the port, provided the same
// number can also be bound as a UDP port: the TCP and UDP port spaces are
// separate (Windows excludes different ranges for each), and the port may serve
// either kind of server. The OS picks the port first; when its choices keep
// failing on the UDP side (a runner with large UDP excluded ranges), ports from a
// fixed range are tried instead. The port may be claimed by another process
// before the caller uses it.
func FreePort(t testing.TB) int {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < freePortAttempts; attempt++ {
		candidate := freePortCandidate(attempt)
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate)))
		if err != nil {
			if candidate == 0 {
				t.Fatalf("could not reserve a test port: %v", err)
			}
			lastErr = err
			continue
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal("could not release a test port")
		}
		packet, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			lastErr = err
			continue
		}
		_ = packet.Close()
		return port
	}
	t.Fatalf("could not reserve a test port free for TCP and UDP (last error: %v)", lastErr)
	return 0
}

// IsAddrInUse reports whether err means the listen address is already taken:
// the OS errors (also through wrapping), the Windows and Unix texts, and the
// core's PORT_IN_USE code, which protocols' redacted start errors may reduce to.
func IsAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	// 10048 is WSAEADDRINUSE and 10013 WSAEACCES (a port in a Windows excluded
	// range or held exclusively), both mean the port cannot be used right now.
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048)) || errors.Is(err, syscall.Errno(10013)) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"address already in use", "address in use", "wsaeaddrinuse", "only one usage of each socket address", "forbidden by its access permissions", "port_in_use"} {
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
