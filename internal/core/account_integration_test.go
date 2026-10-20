//go:build integration

package core

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	mihomoAuth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

const (
	alicePassword = "alice-test-password"
	bobPassword   = "bob-test-password"
	thirdPassword = "third-test-password"
)

func TestAccountCountryIsolation(t *testing.T) {
	de, deAddress := startFakeUpstream(t)
	nl, nlAddress := startFakeUpstream(t)
	defaultUpstream, defaultAddress := startFakeUpstream(t)
	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
			{User: "bob", Pass: bobPassword},
			{User: "third", Pass: thirdPassword},
		}),
		Node: nodeMap(defaultAddress),
		Countries: map[string][]map[string]any{
			"DE": {nodeMap(deAddress)},
			"NL": {nodeMap(nlAddress)},
		},
		AccountCountry: map[string]string{"alice": "DE", "bob": "NL"},
	})
	if err != nil {
		t.Fatalf("Start account instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if instance.Route() != "node" {
		t.Fatalf("Route() = %q, want node", instance.Route())
	}
	if got := instance.AccountRoutes(); got["alice"] != "DE" || got["bob"] != "NL" {
		t.Fatalf("AccountRoutes() = %v", got)
	}

	backend, _ := countEchoServer(t)
	backendDomain := "account-target.invalid"
	de.mapDomain(backendDomain, backend)
	nl.mapDomain(backendDomain, backend)
	defaultUpstream.mapDomain(backendDomain, backend)
	upstreamTarget := net.JoinHostPort(backendDomain, mustPort(t, backend))

	for _, test := range []struct {
		login    string
		password string
		upstream *fakeUpstream
		others   []*fakeUpstream
	}{
		{login: "alice", password: alicePassword, upstream: de, others: []*fakeUpstream{nl, defaultUpstream}},
		{login: "bob", password: bobPassword, upstream: nl, others: []*fakeUpstream{de, defaultUpstream}},
		{login: "third", password: thirdPassword, upstream: defaultUpstream, others: []*fakeUpstream{de, nl}},
	} {
		t.Run(test.login+"-socks5", func(t *testing.T) {
			dialAccountSOCKS5(t, instance.Addr(), backendDomain, backend, test.login, test.password)
			assertAccountUpstreams(t, test.upstream, test.others, upstreamTarget)
		})
		t.Run(test.login+"-http-connect", func(t *testing.T) {
			dialAccountHTTPConnect(t, instance.Addr(), backendDomain, backend, test.login, test.password)
			assertAccountUpstreams(t, test.upstream, test.others, upstreamTarget)
		})
	}

	t.Run("wrong-socks-auth", func(t *testing.T) {
		connection := dialProxy(t, instance.Addr())
		_ = connection.SetDeadline(time.Now().Add(testTimeout))
		if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
			t.Fatal(err)
		}
		if response := readFull(t, connection, 2); response[1] != 2 {
			t.Fatalf("SOCKS greeting = %v, want username/password", response)
		}
		if response := socks5SendAuth(t, connection, "alice", "wrong"); response[1] == 0 {
			t.Fatal("wrong SOCKS credentials accepted")
		}
		_ = connection.Close()
		assertAccountUpstreams(t, nil, []*fakeUpstream{de, nl, defaultUpstream}, upstreamTarget)
	})

	t.Run("wrong-http-auth", func(t *testing.T) {
		connection := dialProxy(t, instance.Addr())
		_ = connection.SetDeadline(time.Now().Add(testTimeout))
		credential := base64.StdEncoding.EncodeToString([]byte("alice:wrong"))
		request := fmt.Sprintf(
			"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
			backend, backend, credential,
		)
		if _, err := io.WriteString(connection, request); err != nil {
			t.Fatal(err)
		}
		response, err := bufio.NewReader(connection).ReadString('\n')
		if err != nil {
			t.Fatalf("read HTTP authentication response: %v", err)
		}
		if !strings.Contains(response, " 407 ") {
			t.Fatalf("HTTP response = %q, want 407", response)
		}
		_ = connection.Close()
		assertAccountUpstreams(t, nil, []*fakeUpstream{de, nl, defaultUpstream}, upstreamTarget)
	})
}

func dialAccountSOCKS5(t *testing.T, proxyAddress, domain, backend, login, password string) {
	t.Helper()
	connection := dialProxy(t, proxyAddress)
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(testTimeout))
	if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
		t.Fatalf("SOCKS greeting: %v", err)
	}
	if response := readFull(t, connection, 2); response[1] != 2 {
		t.Fatalf("SOCKS greeting response = %v", response)
	}
	if response := socks5SendAuth(t, connection, login, password); response[1] != 0 {
		t.Fatalf("SOCKS authentication response = %v", response)
	}
	writeSOCKSDomainConnect(t, connection, domain, backend)
	echoRoundTrip(t, connection, connection)
	_ = connection.Close()
}

func writeSOCKSDomainConnect(t *testing.T, connection net.Conn, domain, backend string) {
	t.Helper()
	_, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatalf("parse backend address: %v", err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}
	request := []byte{5, 1, 0, 3, byte(len(domain))}
	request = append(request, domain...)
	request = binary.BigEndian.AppendUint16(request, uint16(backendPort))
	if _, err := connection.Write(request); err != nil {
		t.Fatalf("SOCKS CONNECT request: %v", err)
	}
	response := readFull(t, connection, 4)
	if response[0] != 5 || response[1] != 0 {
		t.Fatalf("SOCKS CONNECT response = %v, want success", response)
	}
	switch response[3] {
	case 1:
		readFull(t, connection, 6)
	case 3:
		length := int(readFull(t, connection, 1)[0])
		readFull(t, connection, length+2)
	case 4:
		readFull(t, connection, 18)
	default:
		t.Fatalf("unknown SOCKS address type: %d", response[3])
	}
}

func dialAccountHTTPConnect(t *testing.T, proxyAddress, domain, backend, login, password string) {
	t.Helper()
	connection := dialProxy(t, proxyAddress)
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(testTimeout))
	credential := base64.StdEncoding.EncodeToString([]byte(login + ":" + password))
	target := net.JoinHostPort(domain, mustPort(t, backend))
	request := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		target, target, credential,
	)
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatalf("HTTP CONNECT request: %v", err)
	}
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil || status != "HTTP/1.1 200 Connection established\r\n" {
		t.Fatalf("HTTP CONNECT response = %q, err = %v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("HTTP CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	echoRoundTrip(t, connection, reader)
	_ = connection.Close()
}

func mustPort(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse address: %v", err)
	}
	return port
}

func assertAccountUpstreams(t *testing.T, selected *fakeUpstream, others []*fakeUpstream, backend string) {
	t.Helper()
	upstreams := append([]*fakeUpstream(nil), others...)
	if selected != nil {
		upstreams = append(upstreams, selected)
	}
	if selected != nil {
		assertUpstreamSaw(t, selected, []string{backend})
	}
	for _, upstream := range others {
		assertUpstreamSaw(t, upstream, nil)
	}
	for _, upstream := range upstreams {
		upstream.mu.Lock()
		upstream.targets = nil
		upstream.mu.Unlock()
	}
}

func TestAccountCountryDeadNodeFailsClosed(t *testing.T) {
	deadDE, deadDEAddress := startFakeUpstream(t)
	liveNL, liveNLAddress := startFakeUpstream(t)
	liveDefault, liveDefaultAddress := startFakeUpstream(t)
	backend, targetCount := countEchoServer(t)

	// The dead address is reserved last: every live listener already holds its
	// own port, so the closed one cannot be handed to any of them.
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead upstream address: %v", err)
	}
	deadAddress := deadListener.Addr().String()
	if err := deadListener.Close(); err != nil {
		t.Fatalf("close reserved upstream: %v", err)
	}

	instance, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t),
		Authenticator: mihomoAuth.NewAuthenticator([]mihomoAuth.AuthUser{
			{User: "alice", Pass: alicePassword},
			{User: "bob", Pass: bobPassword},
		}),
		Node: nodeMap(liveDefaultAddress),
		Countries: map[string][]map[string]any{
			"DE": {deadNodeMap(deadAddress), nodeMap(deadDEAddress)},
			"NL": {nodeMap(liveNLAddress)},
		},
		AccountCountry: map[string]string{"alice": "DE", "bob": "NL"},
	})
	if err != nil {
		t.Fatalf("Start fail-closed instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	for _, protocol := range []string{"socks5", "http-connect"} {
		t.Run(protocol, func(t *testing.T) {
			connection := dialProxy(t, instance.Addr())
			if err := connection.SetDeadline(time.Now().Add(testTimeout)); err != nil {
				t.Fatalf("set connection deadline: %v", err)
			}
			if protocol == "socks5" {
				if _, err := connection.Write([]byte{5, 1, 2}); err != nil {
					t.Fatal(err)
				}
				if response := readFull(t, connection, 2); response[1] != 2 {
					t.Fatalf("SOCKS greeting response = %v", response)
				}
				if response := socks5SendAuth(t, connection, "alice", alicePassword); response[1] != 0 {
					t.Fatalf("SOCKS authentication response = %v", response)
				}
				if _, err := connection.Write(socks5ConnectRequestBytes(backend)); err != nil {
					t.Fatalf("SOCKS CONNECT request: %v", err)
				}
				readFull(t, connection, 10)
			} else {
				credential := base64.StdEncoding.EncodeToString([]byte("alice:" + alicePassword))
				target := net.JoinHostPort("127.0.0.1", mustPort(t, backend))
				request := fmt.Sprintf(
					"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
					target, target, credential,
				)
				if _, err := io.WriteString(connection, request); err != nil {
					t.Fatalf("HTTP CONNECT request: %v", err)
				}
				reader := bufio.NewReader(connection)
				status, err := reader.ReadString('\n')
				if err != nil || status != "HTTP/1.1 200 Connection established\r\n" {
					t.Fatalf("HTTP CONNECT response = %q, err = %v", status, err)
				}
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("HTTP CONNECT headers: %v", err)
					}
					if line == "\r\n" {
						break
					}
				}
			}

			payload := []byte("must-not-fail-open")
			_, _ = connection.Write(payload)
			_ = connection.SetReadDeadline(time.Now().Add(testTimeout))
			_, readErr := io.ReadFull(connection, make([]byte, len(payload)))
			if readErr == nil {
				t.Fatal("payload reached target despite dead selected node")
			}
			if timeoutErr, ok := readErr.(net.Error); ok && timeoutErr.Timeout() {
				t.Fatalf("dead selected route neither failed nor closed: %v", readErr)
			}
			_ = connection.Close()
			if got := targetCount.Load(); got != 0 {
				t.Fatalf("target accepted %d connections, want 0", got)
			}
			for _, upstream := range []*fakeUpstream{liveNL, liveDefault, deadDE} {
				assertUpstreamSaw(t, upstream, nil)
			}
		})
	}
}

func socks5ConnectRequestBytes(target string) []byte {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		panic(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		panic("expected IPv4 target")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return []byte{5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
}

func deadNodeMap(address string) map[string]any {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return map[string]any{"type": "socks5", "server": host, "port": port}
}
