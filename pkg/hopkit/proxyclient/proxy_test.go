package proxyclient

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

const (
	testUser     = "private-user"
	testPassword = "private-password"
	testTarget   = "unresolved-target.invalid:443"
)

func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 20*time.Second)
}

type closeTrackingConn struct {
	net.Conn
	closed bool
}

func (c *closeTrackingConn) Close() error {
	c.closed = true
	return c.Conn.Close()
}

type readCounterConn struct {
	net.Conn
	readBytes int
}

func (c *readCounterConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.readBytes += n
	return n, err
}

func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	deadline := time.Now().Add(15 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)
	return client, server
}

func rawPipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func waitErr(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatal("proxy peer routine failed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("proxy peer routine did not finish")
	}
}

func peerResult() chan error { return make(chan error, 1) }

func peerDone(ch chan error, err error) { ch <- err }

func checkWire(t *testing.T, got, want []byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("wire length mismatch: got %d bytes, want %d", len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("wire differs at byte %d", i)
			return
		}
	}
}

func readExact(conn net.Conn, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(conn, b)
	return b, err
}

func checkProxyError(t *testing.T, err error, stage Stage, code int) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected proxy error")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatal("error did not have proxy error type")
	}
	if pe.Stage != stage || pe.Code != code {
		t.Fatal("proxy error stage or code mismatch")
	}
	formatted := err.Error()
	for _, secret := range []string{testUser, testPassword, "unresolved-target"} {
		if strings.Contains(formatted, secret) {
			t.Fatal("proxy error exposed sensitive input")
		}
	}
	return pe
}

func TestParseAndString(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Proxy
		text string
	}{
		{"socks default", "socks5://proxy.example", Proxy{Scheme: "socks5", Host: "proxy.example", Port: 1080}, "socks5://proxy.example:1080"},
		{"http default", "http://proxy.example", Proxy{Scheme: "http", Host: "proxy.example", Port: 80}, "http://proxy.example:80"},
		{"https default", "https://proxy.example", Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, "https://proxy.example:443"},
		{"custom port", "socks5://proxy.example:12345", Proxy{Scheme: "socks5", Host: "proxy.example", Port: 12345}, "socks5://proxy.example:12345"},
		{"IPv4", "http://192.0.2.8:8080", Proxy{Scheme: "http", Host: "192.0.2.8", Port: 8080}, "http://192.0.2.8:8080"},
		{"IPv6", "https://[2001:db8::8]:8443", Proxy{Scheme: "https", Host: "2001:db8::8", Port: 8443}, "https://[2001:db8::8]:8443"},
		{"userinfo escapes", "socks5://private%2Duser:private%2Dpassword@proxy.example:999", Proxy{Scheme: "socks5", Host: "proxy.example", Port: 999, Username: testUser, Password: testPassword}, "socks5://private-user:***@proxy.example:999"},
		{"password literal stars", "http://name:***@proxy.example", Proxy{Scheme: "http", Host: "proxy.example", Port: 80, Username: "name", Password: "***"}, "http://name:***@proxy.example:80"},
		{"username only", "http://name@proxy.example", Proxy{Scheme: "http", Host: "proxy.example", Port: 80, Username: "name"}, "http://name@proxy.example:80"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err != nil {
				t.Fatal("valid proxy URL was rejected")
			}
			if got != tt.want {
				t.Fatal("parsed proxy fields did not match")
			}
			if got.String() != tt.text {
				t.Fatal("proxy URL formatting did not match")
			}
			for _, secret := range []string{testPassword} {
				if strings.Contains(got.String(), secret) {
					t.Fatal("formatted proxy URL exposed password")
				}
			}
		})
	}
}

func TestProxyStringMasksEveryPasswordExceptLiteralStars(t *testing.T) {
	cases := []struct {
		proxy Proxy
		want  string
	}{
		{Proxy{Scheme: "http", Host: "proxy", Port: 80, Username: testUser, Password: testPassword}, "http://private-user:***@proxy:80"},
		{Proxy{Scheme: "http", Host: "proxy", Port: 80, Username: "name", Password: "***"}, "http://name:***@proxy:80"},
		{Proxy{Scheme: "http", Host: "proxy", Port: 80, Password: testPassword}, "http://:***@proxy:80"},
	}
	for _, tc := range cases {
		if got := tc.proxy.String(); got != tc.want {
			t.Fatal("proxy URL password masking mismatch")
		}
		if strings.Contains(tc.proxy.String(), testPassword) {
			t.Fatal("proxy URL exposed password")
		}
	}
}
func TestParseRejectsMalformedURLsWithoutSecrets(t *testing.T) {
	bad := []string{
		"socks5://private-user:private-password@proxy.example/",
		"socks5://private-user:private-password@proxy.example/path",
		"http://private-user:private-password@proxy.example?",
		"http://private-user:private-password@proxy.example?x=1",
		"https://private-user:private-password@proxy.example#",
		"https://private-user:private-password@proxy.example#fragment",
		"ftp://private-user:private-password@proxy.example",
		"socks5:///",
		"http://",
		"https://private-user:private-password@",
		"http://proxy.example:",
		"http://proxy.example:0",
		"https://proxy.example:65536",
		"http://proxy.example:nope",
		"http://proxy.example:80abc",
		"http://[2001:db8::1",
		"http://proxy.example:80:81",
		"://proxy.example",
	}
	for _, raw := range bad {
		t.Run("invalid", func(t *testing.T) {
			_, err := Parse(raw)
			_ = checkProxyError(t, err, StageOther, 0)
		})
	}
}

func TestHandshakeValidationLeavesInputOpen(t *testing.T) {
	clientRaw, server := pipePair(t)
	client := &closeTrackingConn{Conn: clientRaw}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, "bad-target")
	if time.Since(started) >= 2*time.Second {
		t.Fatal("validation failure was not prompt")
	}
	_ = checkProxyError(t, err, StageOther, 0)
	if client.closed {
		t.Fatal("Handshake closed its input on validation failure")
	}
	read := make(chan error, 1)
	go func() { _, err := readExact(server, 1); read <- err }()
	if _, err := client.Write([]byte("x")); err != nil {
		t.Fatal("input connection did not remain open")
	}
	select {
	case err := <-read:
		if err != nil {
			t.Fatal("input connection did not remain open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input connection did not respond")
	}
}

func TestHandshakeNilAndCancelledInputs(t *testing.T) {
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Handshake(ctx, nil, Proxy{}, testTarget)
	_ = checkProxyError(t, err, StageOther, 0)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	client, _ := pipePair(t)
	_, err = Handshake(cancelled, client, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
	_ = checkProxyError(t, err, StageConnect, 0)
}
