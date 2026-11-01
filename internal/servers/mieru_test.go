package servers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/yaml"
	"github.com/metacubex/mihomo/constant"
)

func TestMieruRegistrationAndValidation(t *testing.T) {
	p, ok := Lookup("mieru")
	if !ok || p.Name() != "mieru" || p.SingleUser() {
		t.Fatal("Mieru registry behavior mismatch")
	}
	for _, params := range []map[string]string{
		nil,
		{},
		{"transport": "tcp"},
		{"transport": "TcP", "multiplexing": "MULTIPLEXING_LOW"},
		{"multiplexing": "MULTIPLEXING_DEFAULT"},
		{"multiplexing": "MULTIPLEXING_OFF"},
		{"multiplexing": "MULTIPLEXING_MIDDLE"},
		{"multiplexing": "MULTIPLEXING_HIGH"},
	} {
		if err := p.Validate(params); err != nil {
			t.Fatalf("valid params rejected: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		params map[string]string
		key    string
	}{
		{"UDP", map[string]string{"transport": "UDP"}, "transport"},
		{"bad transport", map[string]string{"transport": "bogus-secret-marker"}, "transport"},
		{"unknown key", map[string]string{"private-key": "secret-marker"}, "private-key"},
		{"bad multiplexing", map[string]string{"multiplexing": "secret-marker"}, "multiplexing"},
		{"lowercase multiplexing", map[string]string{"multiplexing": "multiplexing_low"}, "multiplexing"},
		{"mixedcase multiplexing", map[string]string{"multiplexing": "Multiplexing_LOW"}, "multiplexing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Validate(tc.params)
			if err == nil || !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("invalid parameters were not rejected safely")
			}
		})
	}
}

func TestMieruSecretsMaterialAndRedaction(t *testing.T) {
	p, _ := Lookup("mieru")
	first, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	second, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal("secret generation failed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first["password"])
	if err != nil || len(decoded) < 24 || first["password"] == second["password"] {
		t.Fatal("generated password entropy or uniqueness mismatch")
	}
	material, err := p.NewMaterial(nil)
	if err != nil || len(material) != 0 {
		t.Fatal("Mieru unexpectedly requires material")
	}
	material, err = p.NewMaterial(map[string]string{"transport": "UDP"})
	if err == nil || material != nil {
		t.Fatal("material generation accepted invalid protocol parameters")
	}
	spec := Spec{Name: "edge", Protocol: "mieru", Port: 1234, Accounts: []string{"alice"}, Users: UserSecrets{"alice": {"password": "DO_NOT_LEAK"}}}
	if strings.Contains(fmt.Sprintf("%+v %#v", spec, spec), "DO_NOT_LEAK") {
		t.Fatal("Spec formatting leaked secret")
	}
}

func TestMieruMihomoSnippetAndImporter(t *testing.T) {
	p, _ := Lookup("mieru")
	spec := Spec{Name: "edge", Protocol: "mieru", Port: 1234, Accounts: []string{"alice", "bob"}, Params: map[string]string{"transport": "tcp", "multiplexing": "MULTIPLEXING_LOW"}, Users: UserSecrets{"alice": {"password": "pw"}, "bob": {"password": "pw2"}}}
	link, err := p.Link(spec, "alice", "example.org")
	if err != nil {
		t.Fatal("Mieru URI generation failed")
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "mierus" || u.Host != "example.org" || u.User.Username() != "alice" {
		t.Fatal("Mieru URI shape mismatch")
	}
	password, ok := u.User.Password()
	if !ok || password != "pw" || u.Query().Get("port") != "1234" || u.Query().Get("protocol") != "TCP" || u.Query().Get("multiplexing") != "MULTIPLEXING_LOW" || u.Fragment != "edge" || strings.Count(u.RawQuery, "port=") != 1 || strings.Count(u.RawQuery, "protocol=") != 1 {
		t.Fatal("Mieru URI fields mismatch")
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 {
		t.Fatal("Mieru URI conversion failed")
	}
	proxy := converted[0]
	if proxy["type"] != "mieru" || proxy["server"] != "example.org" || proxy["port"] != 1234 || proxy["transport"] != "TCP" || proxy["username"] != "alice" || proxy["password"] != "pw" || proxy["multiplexing"] != "MULTIPLEXING_LOW" || proxy["udp"] != true {
		t.Fatal("Mieru URI conversion fields mismatch")
	}
	parsed, err := subscription.ParseDetailed([]byte(link))
	if err != nil || len(parsed.Nodes) != 1 {
		t.Fatal("Mieru subscription parsing failed")
	}
	proxy = parsed.Nodes[0].Raw
	if parsed.Nodes[0].Type != "mieru" || parsed.Nodes[0].Server != "example.org" || parsed.Nodes[0].Port != 1234 || proxy["transport"] != "TCP" || proxy["username"] != "alice" || proxy["password"] != "pw" || proxy["multiplexing"] != "MULTIPLEXING_LOW" || proxy["udp"] != true {
		t.Fatal("Mieru subscription fields mismatch")
	}
	snippet, err := p.(MihomoLinker).Mihomo(spec, "alice", "example.org")
	if err != nil {
		t.Fatal("Mihomo snippet generation failed")
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &cfg); err != nil || len(cfg.Proxies) != 1 {
		t.Fatal("snippet is not valid proxy YAML")
	}
	if cfg.Proxies[0]["udp"] != false {
		t.Fatal("authoritative YAML udp setting mismatch")
	}
	for k, want := range map[string]any{"name": "edge", "type": "mieru", "server": "example.org", "transport": "TCP", "username": "alice", "password": "pw", "port": 1234, "multiplexing": "MULTIPLEXING_LOW", "udp": false} {
		if cfg.Proxies[0][k] != want {
			t.Fatalf("snippet field mismatch: %s", k)
		}
	}
	parsedProxy, err := adapter.ParseProxy(cfg.Proxies[0])
	if err != nil {
		t.Fatal("Mihomo importer rejected Mieru proxy")
	}
	_ = parsedProxy.Close()
}

func TestMieruLinkEncodesCredentialsAndRejectsInvalidInput(t *testing.T) {
	p, _ := Lookup("mieru")
	login := "alice@realm"
	secret := "space +%@:secret"
	spec := Spec{Name: "edge", Protocol: "mieru", Port: 4321, Accounts: []string{login}, Params: map[string]string{"multiplexing": "MULTIPLEXING_OFF"}, Users: UserSecrets{login: {"password": secret}}}
	link, err := p.Link(spec, login, "example.org")
	if err != nil {
		t.Fatal("Mieru link generation failed")
	}
	if strings.Contains(link, login) || strings.Contains(link, secret) || !strings.Contains(link, "alice%40realm") || !strings.Contains(link, "space%20+%25%40%3Asecret") {
		t.Fatal("credentials were not percent-escaped")
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal("Mieru link parse failed")
	}
	gotLogin, gotPassword := u.User.Username(), ""
	gotPassword, ok := u.User.Password()
	if gotLogin != login || !ok || gotPassword != secret {
		t.Fatal("credentials did not round-trip")
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 || converted[0]["username"] != login || converted[0]["password"] != secret {
		t.Fatal("escaped credentials did not survive URI conversion")
	}
	for _, tc := range []struct {
		spec        Spec
		login, host string
	}{
		{spec, "missing", "example.org"},
		{spec, login, ""},
		{func() Spec { s := spec.Clone(); s.Protocol = "other"; return s }(), login, "example.org"},
		{func() Spec { s := spec.Clone(); s.Users[login]["password"] = ""; return s }(), login, "example.org"},
		{func() Spec { s := spec.Clone(); s.Params["multiplexing"] = "multiplexing_off"; return s }(), login, "example.org"},
		{func() Spec { s := spec.Clone(); s.Params["multiplexing"] = "Multiplexing_OFF"; return s }(), login, "example.org"},
	} {
		if _, err := p.Link(tc.spec, tc.login, tc.host); err == nil {
			t.Fatal("invalid Mieru link input accepted")
		}
		if _, err := p.(MihomoLinker).Mihomo(tc.spec, tc.login, tc.host); err == nil {
			t.Fatal("invalid Mieru Mihomo input accepted")
		}
	}
}

func TestMieruLinkIPv6Host(t *testing.T) {
	p, _ := Lookup("mieru")
	spec := Spec{Name: "edge", Protocol: "mieru", Port: 4321, Accounts: []string{"alice"}, Users: UserSecrets{"alice": {"password": "pw"}}}
	link, err := p.Link(spec, "alice", "2001:db8::7")
	if err != nil {
		t.Fatal("IPv6 Mieru link generation failed")
	}
	u, err := url.Parse(link)
	if err != nil || u.Hostname() != "2001:db8::7" {
		t.Fatal("IPv6 Mieru host did not round-trip")
	}
	converted, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil || len(converted) != 1 || converted[0]["server"] != "2001:db8::7" {
		t.Fatal("IPv6 Mieru host conversion mismatch")
	}
}

func TestMieruSnippetAccountsAndHostEncoding(t *testing.T) {
	p, _ := Lookup("mieru")
	password := "quotes:\" slash:/ newline:\n"
	for _, host := range []string{"example.org", "2001:db8::7"} {
		spec := Spec{Name: "edge", Protocol: "mieru", Port: 4321, Accounts: []string{"alice", "bob"}, Users: UserSecrets{"alice": {"password": password}, "bob": {"password": "other"}}}
		snippet, err := p.(MihomoLinker).Mihomo(spec, "alice", host)
		if err != nil {
			t.Fatal("Mihomo snippet generation failed")
		}
		var cfg struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &cfg); err != nil || len(cfg.Proxies) != 1 {
			t.Fatal("Mihomo YAML could not be decoded")
		}
		if cfg.Proxies[0]["server"] != host || cfg.Proxies[0]["password"] != password {
			t.Fatal("host or quoted password did not round-trip")
		}
	}
}

func TestMieruMultiplexingCanonicalLinks(t *testing.T) {
	p, _ := Lookup("mieru")
	for _, level := range []string{"MULTIPLEXING_DEFAULT", "MULTIPLEXING_OFF", "MULTIPLEXING_LOW", "MULTIPLEXING_MIDDLE", "MULTIPLEXING_HIGH"} {
		spec := Spec{Name: "edge", Protocol: "mieru", Port: 1234, Accounts: []string{"alice"}, Params: map[string]string{"multiplexing": level}, Users: UserSecrets{"alice": {"password": "pw"}}}
		link, err := p.Link(spec, "alice", "example.org")
		if err != nil {
			t.Fatalf("canonical level rejected: %s", level)
		}
		u, err := url.Parse(link)
		if err != nil || u.Query().Get("multiplexing") != level {
			t.Fatalf("URI multiplexing mismatch for %s", level)
		}
		snippet, err := p.(MihomoLinker).Mihomo(spec, "alice", "example.org")
		if err != nil {
			t.Fatalf("Mihomo level rejected: %s", level)
		}
		var cfg struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(snippet), &cfg); err != nil || len(cfg.Proxies) != 1 || cfg.Proxies[0]["multiplexing"] != level {
			t.Fatalf("YAML multiplexing mismatch for %s", level)
		}
	}
}
func TestMieruStartRejectsMissingEnvironmentAndSecrets(t *testing.T) {
	p, _ := Lookup("mieru")
	spec := Spec{Name: "edge", Protocol: "mieru", Port: 1234, Accounts: []string{"alice"}, Users: UserSecrets{"alice": {"password": "secret-marker"}}}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, spec), "secret-marker") {
			t.Fatal("Spec formatting leaked secret")
		}
	}
	if _, err := p.Start(spec, Env{}); err == nil {
		t.Fatal("missing environment accepted")
	}
	spec.Users = nil
	listen := &mieruTrackingListenConfig{}
	if _, err := p.Start(spec, Env{Tunnel: mieruNoopTunnel{}, ListenConfig: listen}); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatal("missing secret was not rejected")
	}
	if listen.calls != 0 {
		t.Fatal("missing secret attempted to open a listener")
	}
}

func TestMieruSafeStartErrorRedacts(t *testing.T) {
	sentinel := errors.New("bind failure")
	err := mieruSafeStartError{cause: fmt.Errorf("password=secret-marker: %w", sentinel)}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, err), "secret-marker") {
			t.Fatal("startup error leaked cause")
		}
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("startup error did not preserve classification")
	}
}

type mieruTrackingListenConfig struct{ calls int }

func (c *mieruTrackingListenConfig) Listen(context.Context, string, string) (net.Listener, error) {
	c.calls++
	return nil, errors.New("unexpected listener call")
}
func (c *mieruTrackingListenConfig) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	c.calls++
	return nil, errors.New("unexpected packet listener call")
}

func TestMieruCloserClosesOwnedSocketOnce(t *testing.T) {
	socket := &mieruTestListener{}
	closer := &mieruCloser{socket: socket}
	if err := closer.Close(); err != nil {
		t.Fatal("close failed")
	}
	if err := closer.Close(); err != nil || socket.closes != 1 {
		t.Fatal("owned socket was not closed exactly once")
	}
}

type mieruTestListener struct{ closes int }

func (l *mieruTestListener) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (l *mieruTestListener) Close() error              { l.closes++; return nil }
func (l *mieruTestListener) Addr() net.Addr            { return &net.TCPAddr{} }

type mieruNoopTunnel struct{}

func (mieruNoopTunnel) HandleTCPConn(net.Conn, *constant.Metadata)             {}
func (mieruNoopTunnel) HandleUDPPacket(constant.UDPPacket, *constant.Metadata) {}
func (mieruNoopTunnel) NatTable() constant.NatTable                            { return nil }
