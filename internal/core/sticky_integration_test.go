//go:build integration

package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// TestStickyGroupAffinity verifies destination affinity end to end: one
// account's domain and its subdomain always leave through the same upstream
// node, and a second account binds independently. Failover is not exercised
// here (see the unit tests for down and rebind logic).
func TestStickyGroupAffinity(t *testing.T) {
	n0, n0Address := startFakeUpstream(t)
	n1, n1Address := startFakeUpstream(t)
	backend, _ := countEchoServer(t)

	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
			{User: "bob", Pass: bobPassword},
		}),
		RouteNodes: map[string][]map[string]any{
			"de": {nodeMap(n0Address), nodeMap(n1Address)},
		},
		AccountRoute: map[string]string{"alice": "de", "bob": "de"},
	})
	if err != nil {
		t.Fatalf("Start sticky instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	domain, subdomain := "sticky.example", "sub.sticky.example"
	for _, upstream := range []*fakeUpstream{n0, n1} {
		upstream.mapDomain(domain, backend)
		upstream.mapDomain(subdomain, backend)
	}

	// served returns the single upstream that recorded a connection, or nil
	// when the connection did not reach exactly one of them.
	served := func() *fakeUpstream {
		n0Seen, n1Seen := len(n0.recorded()), len(n1.recorded())
		switch {
		case n0Seen > 0 && n1Seen == 0:
			return n0
		case n1Seen > 0 && n0Seen == 0:
			return n1
		default:
			return nil
		}
	}
	clearRecords := func() {
		for _, upstream := range []*fakeUpstream{n0, n1} {
			upstream.mu.Lock()
			upstream.targets = nil
			upstream.mu.Unlock()
		}
	}

	upstreamTarget := net.JoinHostPort(domain, mustPort(t, backend))

	// Alice: the domain and a subdomain of it share one binding.
	dialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
	aliceFirst := served()
	if aliceFirst == nil {
		t.Fatal("alice's connection did not reach exactly one upstream")
	}
	if got := aliceFirst.recorded(); len(got) != 1 || got[0] != upstreamTarget {
		t.Fatalf("upstream saw %v, want [%s]", got, upstreamTarget)
	}
	clearRecords()

	dialAccountSOCKS5(t, instance.Addr(), subdomain, backend, "alice", alicePassword)
	if got := served(); got != aliceFirst {
		t.Fatal("the subdomain used a different upstream than the domain")
	}
	clearRecords()

	dialAccountSOCKS5(t, instance.Addr(), domain, backend, "alice", alicePassword)
	if got := served(); got != aliceFirst {
		t.Fatal("alice's binding moved although the node stayed healthy")
	}
	clearRecords()

	// Bob binds independently: his connections stay on one upstream, which
	// may be either of them.
	var bobFirst *fakeUpstream
	for i := 0; i < 3; i++ {
		dialAccountSOCKS5(t, instance.Addr(), domain, backend, "bob", bobPassword)
		bobServed := served()
		if bobServed == nil {
			t.Fatalf("bob's connection %d did not reach exactly one upstream", i)
		}
		clearRecords()
		if i > 0 && bobServed != bobFirst {
			t.Fatal("bob's binding moved between connections")
		}
		bobFirst = bobServed
	}

	stats := instance.AffinityStats()
	if stats.Bindings != 2 {
		t.Fatalf("AffinityStats().Bindings = %d, want 2", stats.Bindings)
	}
	if stats.Rebinds != 0 {
		t.Fatalf("AffinityStats().Rebinds = %d, want 0", stats.Rebinds)
	}
}

// tryDialAccountSOCKS5 performs a full authenticated SOCKS5 CONNECT with an
// echo round trip and returns the first error instead of failing the test.
// It is used where failures are expected (dead route nodes).
func tryDialAccountSOCKS5(t *testing.T, proxyAddress, domain, backend, login, password string) error {
	t.Helper()
	connection, err := openAccountSOCKS5(t, proxyAddress, domain, backend, login, password)
	if err != nil {
		return err
	}
	defer connection.Close()
	return accountEcho(connection)
}

// openAccountSOCKS5 authenticates as login and connects to domain (port of
// backend) through the proxy; the caller closes the connection.
func openAccountSOCKS5(t *testing.T, proxyAddress, domain, backend, login, password string) (net.Conn, error) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", proxyAddress, testTimeout)
	if err != nil {
		return nil, err
	}
	if err := socks5AccountHandshake(connection, domain, backend, login, password); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func socks5AccountHandshake(connection net.Conn, domain, backend, login, password string) error {
	if err := connection.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		return err
	}
	if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		return err
	}
	if greeting[0] != 5 || greeting[1] != 2 {
		return fmt.Errorf("socks5 greeting = %v, want username/password", greeting)
	}
	auth := []byte{1, byte(len(login))}
	auth = append(auth, login...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	if _, err := connection.Write(auth); err != nil {
		return err
	}
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(connection, authReply); err != nil {
		return err
	}
	if authReply[0] != 1 || authReply[1] != 0 {
		return fmt.Errorf("socks5 auth reply = %v, want success", authReply)
	}
	_, portText, err := net.SplitHostPort(backend)
	if err != nil {
		return fmt.Errorf("parse backend address: %w", err)
	}
	backendPort, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("parse backend port: %w", err)
	}
	request := []byte{5, 1, 0, 3, byte(len(domain))}
	request = append(request, domain...)
	request = binary.BigEndian.AppendUint16(request, uint16(backendPort))
	if _, err := connection.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(connection, reply); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 {
		return fmt.Errorf("socks5 connect reply = %v, want success", reply)
	}
	switch reply[3] {
	case 1:
		if _, err := readExact(connection, 6); err != nil {
			return err
		}
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return err
		}
		if _, err := readExact(connection, int(length[0])+2); err != nil {
			return err
		}
	case 4:
		if _, err := readExact(connection, 18); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown socks5 address type: %d", reply[3])
	}
	return nil
}

// accountEcho sends a payload through an open connection to an echo backend
// and checks it comes back: the tunnel must carry traffic before the
// connection counts as success.
func accountEcho(connection net.Conn) error {
	payload := []byte("sticky-probe")
	if _, err := connection.Write(payload); err != nil {
		return err
	}
	echoed, err := readExact(connection, len(payload))
	if err != nil {
		return err
	}
	if string(echoed) != string(payload) {
		return fmt.Errorf("echo round trip got %q", echoed)
	}
	return nil
}

// readExact reads exactly n bytes without failing the test.
func readExact(c net.Conn, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
