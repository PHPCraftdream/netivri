//go:build integration

package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
)

const multiportLinkClassifyDeadline = 4 * time.Second

type multiportLinkHelloConn struct{ bytes.Buffer }

func (c *multiportLinkHelloConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *multiportLinkHelloConn) Close() error                     { return nil }
func (c *multiportLinkHelloConn) LocalAddr() net.Addr              { return multiportLinkAddr("local") }
func (c *multiportLinkHelloConn) RemoteAddr() net.Addr             { return multiportLinkAddr("remote") }
func (c *multiportLinkHelloConn) SetDeadline(time.Time) error      { return nil }
func (c *multiportLinkHelloConn) SetReadDeadline(time.Time) error  { return nil }
func (c *multiportLinkHelloConn) SetWriteDeadline(time.Time) error { return nil }

type multiportLinkAddr string

func (a multiportLinkAddr) Network() string { return "test" }
func (a multiportLinkAddr) String() string  { return string(a) }

func multiportLinkHello(t *testing.T, sni string, alpn []string) []byte {
	t.Helper()
	conn := &multiportLinkHelloConn{}
	_ = tls.Client(conn, &tls.Config{ServerName: sni, NextProtos: alpn}).Handshake()
	if conn.Len() == 0 {
		t.Fatal("TLS client did not write a ClientHello")
	}
	hello := append([]byte(nil), conn.Bytes()...)
	parsed, err := servers.ParseClientHello(hello)
	if err != nil {
		t.Fatalf("could not parse generated ClientHello: %v", err)
	}
	if parsed.SNI != strings.ToLower(sni) {
		t.Fatalf("ClientHello SNI %q, want %q", parsed.SNI, strings.ToLower(sni))
	}
	if !reflect.DeepEqual(parsed.ALPN, alpn) {
		t.Fatalf("ClientHello ALPN %v, want %v", parsed.ALPN, alpn)
	}
	return hello
}

func multiportLinkSpec(t *testing.T, protocol, name, login, advertisedHost, sni, alpn string, port int) (servers.Spec, string, []string) {
	t.Helper()
	p, ok := servers.Lookup(protocol)
	if !ok {
		t.Fatalf("%s protocol unavailable", protocol)
	}
	params := map[string]string{"public_host": advertisedHost}
	if sni != "" {
		params["sni"] = sni
	}
	if alpn != "" {
		if protocol != "trojan" {
			t.Fatal("ALPN fixture is only valid for Trojan")
		}
		params["alpn"] = alpn
	}
	material, err := p.NewMaterial(params)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := p.NewSecret(login, params)
	if err != nil {
		t.Fatal(err)
	}
	spec := servers.Spec{Name: name, Protocol: protocol, Bind: "127.0.0.1", Port: port, Accounts: []string{login}, Params: params, Material: servers.SecretMap(material), Users: servers.UserSecrets{login: servers.SecretMap(secret)}}
	link, err := p.Link(spec, login, advertisedHost)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal("protocol link is invalid")
	}
	// Keep the parsed URL local: it contains credentials and is never logged.
	nameOnHello := parsed.Query().Get("sni")
	if nameOnHello == "" {
		nameOnHello = parsed.Hostname()
	}
	if nameOnHello == "" {
		t.Fatal("protocol link has neither SNI nor hostname")
	}
	var exportedALPN []string
	if value := parsed.Query().Get("alpn"); value != "" {
		exportedALPN = strings.Split(value, ",")
	}
	return spec, nameOnHello, exportedALPN
}

func TestMultiportClassifierUsesProtocolLinkNames(t *testing.T) {
	for _, tc := range []struct{ name, protocol, login, host, sni, alpn string }{
		{name: "trojan-explicit-sni-alpn", protocol: "trojan", login: "alice", host: "advertised.example", sni: "explicit.example", alpn: "h2,http/1.1"},
		{name: "trojan-host-fallback", protocol: "trojan", login: "alice", host: "advertised.example"},
		{name: "anytls-explicit-sni", protocol: "anytls", login: "bob", host: "advertised.example", sni: "any.example"},
		{name: "anytls-host-fallback", protocol: "anytls", login: "bob", host: "any.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			b := newSocketBroker()
			t.Cleanup(b.close)
			stage := b.stage()
			var acceptWG sync.WaitGroup
			_ = listener.Close()
			a, selectedName, selectedALPN := multiportLinkSpec(t, tc.protocol, "link-member", tc.login, tc.host, tc.sni, tc.alpn, port)
			a.Default = tc.sni == ""
			expectedName := tc.sni
			if expectedName == "" {
				expectedName = tc.host
			}
			if selectedName != expectedName {
				t.Fatalf("exported link SNI %q, want %q", selectedName, expectedName)
			}
			if !reflect.DeepEqual(selectedALPN, func() []string {
				if tc.alpn == "" {
					return nil
				}
				return strings.Split(tc.alpn, ",")
			}()) {
				t.Fatalf("exported link ALPN %v, want %q", selectedALPN, tc.alpn)
			}
			otherProtocol, otherLogin := "anytls", "bob"
			if tc.protocol == "anytls" {
				otherProtocol, otherLogin = "trojan", "alice"
			}
			other, _, _ := multiportLinkSpec(t, otherProtocol, "other-member", otherLogin, "unrelated.example", "unrelated.example", "", port)
			if err := servers.ValidateSharedPort([]servers.Spec{a, other}); err != nil {
				t.Fatalf("shared-port validation failed: %v", err)
			}
			stage.setServers([]servers.Spec{a, other})
			address := a.ListenAddress()
			leaseA, err := stage.forServer(a).Listen(context.Background(), "tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			leaseB, err := stage.forServer(other).Listen(context.Background(), "tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			if err := stage.commit(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = leaseA.Close()
				_ = leaseB.Close()
				b.close()
				wait := make(chan struct{})
				go func() { acceptWG.Wait(); close(wait) }()
				select {
				case <-wait:
				case <-time.After(multiportLinkClassifyDeadline):
					t.Error("timed out waiting for accept goroutines to stop")
				}
			})
			payload := append(multiportLinkHello(t, selectedName, selectedALPN), []byte("replay-tail")...)
			client, err := net.DialTimeout("tcp", address, multiportLinkClassifyDeadline)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.SetDeadline(time.Now().Add(multiportLinkClassifyDeadline)); err != nil {
				t.Fatal("could not set client deadline")
			}
			if _, err := client.Write(payload); err != nil {
				t.Fatal(err)
			}
			accepted := make(chan struct {
				name string
				conn net.Conn
			}, 2)
			for _, lease := range []*socketLease{leaseA.(*socketLease), leaseB.(*socketLease)} {
				acceptWG.Add(1)
				go func(l *socketLease) {
					defer acceptWG.Done()
					c, e := l.Accept()
					if e == nil {
						accepted <- struct {
							name string
							conn net.Conn
						}{l.name, c}
					}
				}(lease)
			}
			select {
			case got := <-accepted:
				defer got.conn.Close()
				if got.name != "link-member" {
					t.Fatalf("selected member %q, want link-member", got.name)
				}
				if err := got.conn.SetDeadline(time.Now().Add(multiportLinkClassifyDeadline)); err != nil {
					t.Fatal("could not set accepted-connection deadline")
				}
				gotBytes := make([]byte, len(payload))
				if _, err := io.ReadFull(got.conn, gotBytes); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(gotBytes, payload) {
					t.Fatal("classified connection replay did not preserve the exact ClientHello and trailing bytes")
				}
			case <-time.After(multiportLinkClassifyDeadline):
				t.Fatal("timed out waiting for link-derived classification")
			}
			if members, unclassified, _ := b.endpointInfo(address); unclassified != 0 || len(members) != 2 {
				t.Fatalf("endpoint members=%v unclassified=%d, want both members and classified connection", members, unclassified)
			}
		})
	}
}
