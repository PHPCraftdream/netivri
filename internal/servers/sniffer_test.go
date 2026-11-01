package servers

import (
	"crypto/tls"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

type snifferHelloConn struct {
	net.Conn
	captured []byte
}

func (c *snifferHelloConn) Write(p []byte) (int, error) {
	c.captured = append(c.captured, p...)
	return len(p), nil
}
func (c *snifferHelloConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *snifferHelloConn) Close() error                     { return nil }
func (c *snifferHelloConn) LocalAddr() net.Addr              { return snifferDummyAddr("local") }
func (c *snifferHelloConn) RemoteAddr() net.Addr             { return snifferDummyAddr("remote") }
func (c *snifferHelloConn) SetDeadline(time.Time) error      { return nil }
func (c *snifferHelloConn) SetReadDeadline(time.Time) error  { return nil }
func (c *snifferHelloConn) SetWriteDeadline(time.Time) error { return nil }

type snifferDummyAddr string

func (a snifferDummyAddr) Network() string { return string(a) }
func (a snifferDummyAddr) String() string  { return string(a) }

func TestSnifferParseClientHelloTLSClient(t *testing.T) {
	conn := &snifferHelloConn{}
	client := tls.Client(conn, &tls.Config{ServerName: "Test.Example", NextProtos: []string{"h2", "http/1.1"}, InsecureSkipVerify: true})
	_ = client.Handshake()
	got, err := ParseClientHello(conn.captured)
	if err != nil {
		t.Fatal(err)
	}
	if got.SNI != "test.example" || len(got.ALPN) != 2 || got.ALPN[0] != "h2" {
		t.Fatalf("hello=%+v", got)
	}
}
func TestSnifferParseClientHelloAbsentSNIAndALPN(t *testing.T) {
	body := []byte{3, 3}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0, 2, 1, 0)
	handshake := []byte{1, 0, 0, byte(len(body))}
	handshake = append(handshake, body...)
	wire := []byte{22, 3, 3, byte(len(handshake) >> 8), byte(len(handshake))}
	wire = append(wire, handshake...)
	got, err := ParseClientHello(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.SNI != "" || len(got.ALPN) != 0 {
		t.Fatalf("hello=%+v", got)
	}
}
func TestSnifferParseClientHelloGREASEAndMultiRecord(t *testing.T) {
	body := []byte{3, 3}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0, 2, 1, 0, 0, 4, 0x0a, 0x0a, 0, 0)
	handshake := []byte{1, 0, 0, byte(len(body))}
	handshake = append(handshake, body...)
	split := len(handshake) / 2
	wire := []byte{22, 3, 3, byte(split >> 8), byte(split)}
	wire = append(wire, handshake[:split]...)
	wire = append(wire, 22, 3, 3, byte((len(handshake)-split)>>8), byte(len(handshake)-split))
	wire = append(wire, handshake[split:]...)
	if _, err := ParseClientHello(wire); err != nil {
		t.Fatal(err)
	}
}
func TestSnifferParseClientHelloIncompleteLimits(t *testing.T) {
	for _, input := range [][]byte{nil, {22, 3, 3, 0, 4, 1, 0}, {22, 3, 3, 0, 1, 1}} {
		if _, err := ParseClientHello(input); err == nil {
			t.Errorf("accepted %x", input)
		}
	}
	if _, err := ParseClientHello(make([]byte, MaxClientHelloBytes+1)); err == nil {
		t.Fatal("accepted oversized peek")
	}
}
func TestSnifferAmbiguous(t *testing.T) {
	if !SnifferAmbiguous([]string{"a.example"}, nil, []string{"A.EXAMPLE"}, []string{"h2"}) {
		t.Fatal("expected case-insensitive SNI overlap")
	}
	if SnifferAmbiguous([]string{"a.example"}, []string{"h2"}, []string{"A.EXAMPLE"}, []string{"H2"}) {
		t.Fatal("ALPN must be exact and case-sensitive")
	}
}

func TestValidateSharedPortDefaultDoesNotHideAmbiguousSNI(t *testing.T) {
	specs := []Spec{
		{Name: "default", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Default: true, Params: map[string]string{"sni": "a.example"}},
		{Name: "same", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Params: map[string]string{"sni": "A.EXAMPLE"}},
	}
	if err := ValidateSharedPort(specs); err == nil {
		t.Fatal("expected default and normal server with overlapping SNI to be rejected")
	}
}

func TestValidateSharedPortNoSNIRequiresDefault(t *testing.T) {
	standalone := Spec{Name: "standalone", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Params: map[string]string{}}
	if err := ValidateSharedPort([]Spec{standalone, {Name: "other", Bind: standalone.Bind, Port: standalone.Port, Protocol: "trojan", Params: map[string]string{"sni": "other.example"}}}); err == nil {
		t.Fatal("expected no-SNI shared server to require a default")
	}
	defaultSpec := standalone
	defaultSpec.Default = true
	if err := ValidateSharedPort([]Spec{defaultSpec, {Name: "other", Bind: standalone.Bind, Port: standalone.Port, Protocol: "trojan", Params: map[string]string{"sni": "other.example"}}}); err != nil {
		t.Fatalf("no-SNI default rejected: %v", err)
	}
}

func TestValidateSharedPortTrustTunnelHostSNIIsMatchable(t *testing.T) {
	trust := Spec{Name: "trust", Bind: "127.0.0.1", Port: 443, Protocol: "trusttunnel", Params: map[string]string{}}
	other := Spec{Name: "other", Bind: trust.Bind, Port: trust.Port, Protocol: "trojan", Params: map[string]string{"sni": "host.example"}}
	if err := ValidateSharedPort([]Spec{trust, other}); err == nil {
		t.Fatal("expected TrustTunnel host SNI to conflict with explicit host")
	}
}

type snifferCustomProtocol struct{ trojanServer }

func (snifferCustomProtocol) Name() string { return "sniffer-custom-matchkeys-test" }
func (snifferCustomProtocol) MatchKeys(params map[string]string) ([]string, []string, bool) {
	return []string{params["custom_host"]}, []string{params["custom_application"]}, true
}

func TestSnifferCustomMatchKeysCapability(t *testing.T) {
	Register(snifferCustomProtocol{})
	spec := Spec{Protocol: "sniffer-custom-matchkeys-test", Params: map[string]string{"custom_host": "custom.example", "custom_application": "custom/1"}}
	sni, alpn, explicit, capable := TLSMatchKeys(spec)
	if !capable || !explicit || !reflect.DeepEqual(sni, []string{"custom.example"}) || !reflect.DeepEqual(alpn, []string{"custom/1"}) {
		t.Fatalf("TLSMatchKeys() = (%v, %v, %t, %t)", sni, alpn, explicit, capable)
	}
	if !MatchClientHello(spec, ClientHello{SNI: "custom.example", ALPN: []string{"custom/1"}}) {
		t.Fatal("custom ClientHello did not match")
	}
	if MatchClientHello(spec, ClientHello{SNI: "wrong.example", ALPN: []string{"custom/1"}}) {
		t.Fatal("matched wrong custom host")
	}

	first := Spec{Protocol: spec.Protocol, Bind: "127.0.0.1", Port: 443, Params: map[string]string{"custom_host": "one.example", "custom_application": "one/1"}}
	second := Spec{Protocol: spec.Protocol, Bind: "127.0.0.1", Port: 443, Params: map[string]string{"custom_host": "two.example", "custom_application": "two/1"}}
	if err := ValidateSharedPort([]Spec{first, second}); err != nil {
		t.Fatalf("distinct custom keys rejected: %v", err)
	}
	second.Params["custom_host"] = first.Params["custom_host"]
	second.Params["custom_application"] = first.Params["custom_application"]
	err := ValidateSharedPort([]Spec{first, second})
	cliErr, isCLIError := err.(*cli.Error)
	if !isCLIError || cliErr.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("ValidateSharedPort() error = %v, want SERVER_PORT_AMBIGUOUS cli.Error", err)
	}
}

func TestTrustTunnelNoSNIRequiresH2(t *testing.T) {
	spec := Spec{Protocol: "trusttunnel", Params: map[string]string{}}
	if !MatchClientHello(spec, ClientHello{SNI: "host.example", ALPN: []string{"h2"}}) {
		t.Fatal("TrustTunnel no-SNI spec did not match host SNI with h2")
	}
	if MatchClientHello(spec, ClientHello{SNI: "host.example", ALPN: []string{"http/1.1"}}) {
		t.Fatal("TrustTunnel no-SNI spec matched unsupported ALPN")
	}
}

func TestTLSMatchKeysDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		spec     Spec
		sni      []string
		alpn     []string
		explicit bool
	}{
		{name: "trojan", spec: Spec{Protocol: "trojan", Params: map[string]string{"sni": "trojan.example", "alpn": "h2,http/1.1"}}, sni: []string{"trojan.example"}, alpn: []string{"h2", "http/1.1"}, explicit: true},
		{name: "anytls", spec: Spec{Protocol: "anytls", Params: map[string]string{"sni": "anytls.example"}}, sni: []string{"anytls.example"}, alpn: nil, explicit: true},
		{name: "trusttunnel", spec: Spec{Protocol: "trusttunnel", Params: map[string]string{"sni": "trust.example"}}, sni: []string{"trust.example"}, alpn: []string{"h2"}, explicit: true},
		{name: "vless-reality explicit", spec: Spec{Protocol: "vless-reality", Params: map[string]string{"sni": "EXAMPLE.COM", "dest": "fallback.example:443"}}, sni: []string{"example.com"}, explicit: true},
		{name: "vless-reality destination", spec: Spec{Protocol: "vless-reality", Params: map[string]string{"dest": "fallback.example:443"}}, sni: []string{"fallback.example"}, explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotSNI, gotALPN, explicit, capable := TLSMatchKeys(tc.spec)
			if !capable || explicit != tc.explicit || !reflect.DeepEqual(gotSNI, tc.sni) || !reflect.DeepEqual(gotALPN, tc.alpn) {
				t.Fatalf("TLSMatchKeys() = (%v, %v, %t, %t), want (%v, %v, %t, true)", gotSNI, gotALPN, explicit, capable, tc.sni, tc.alpn, tc.explicit)
			}
		})
	}
}
