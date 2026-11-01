package servers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/component/ca"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/sing"
	"github.com/metacubex/mihomo/ntp"
	"github.com/metacubex/mihomo/transport/socks5"
	transporttuic "github.com/metacubex/mihomo/transport/tuic"
	"github.com/metacubex/quic-go"
	"github.com/metacubex/tls"
	"go.yaml.in/yaml/v3"
)

type tuicProtocol struct{}

func init() { Register(tuicProtocol{}) }

func (tuicProtocol) Name() string     { return "tuic" }
func (tuicProtocol) SingleUser() bool { return true }
func (tuicProtocol) UDPBound() bool   { return true }

func (tuicProtocol) Validate(params map[string]string) error {
	for key := range params {
		switch key {
		case "sni", "alpn", "congestion", "udp_relay_mode", "public_host", "cert_file", "key_file":
		default:
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	if err := ValidateTLSParams(params); err != nil {
		return err
	}
	if host := params["public_host"]; host != "" && !validTLSHost(host) {
		return errors.New("invalid public_host")
	}
	if value := params["congestion"]; value != "" && value != "bbr" && value != "cubic" && value != "new_reno" {
		return errors.New("invalid congestion")
	}
	if value := params["udp_relay_mode"]; value != "" && value != "native" && value != "quic" {
		return errors.New("invalid udp_relay_mode")
	}
	return nil
}

func (tuicProtocol) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (tuicProtocol{}).Validate(params); err != nil {
		return nil, err
	}
	host := params["public_host"]
	if host == "" {
		host = params["sni"]
	}
	return NewTLSMaterial(params, host)
}

func (tuicProtocol) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (tuicProtocol{}).Validate(params); err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, errors.New("generate tuic UUID failed")
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return nil, errors.New("generate tuic password failed")
	}
	return map[string]string{
		"uuid":     fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(id[:4]), hex.EncodeToString(id[4:6]), hex.EncodeToString(id[6:8]), hex.EncodeToString(id[8:10]), hex.EncodeToString(id[10:])),
		"password": hex.EncodeToString(password),
	}, nil
}

func (tuicProtocol) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "tuic" {
		return nil, errors.New("tuic protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("tuic start requires tunnel and listen config")
	}
	login := spec.Accounts[0]
	secret := spec.Users[login]
	if !validTUICSecret(secret) {
		return nil, errors.New("tuic user secret missing or invalid")
	}
	certificate, privateKey := TLSFiles(spec.Material)
	if certificate == "" || privateKey == "" || TLSFingerprint(spec.Material) == "" {
		return nil, errors.New("tuic TLS material missing or invalid")
	}
	address := spec.ListenAddress()
	pc, err := env.ListenConfig.ListenPacket(context.Background(), "udp", address)
	if err != nil {
		return nil, tuicStartError{cause: err}
	}
	alpn := spec.Params["alpn"]
	if alpn == "" {
		alpn = "h3"
	}
	congestion := spec.Params["congestion"]
	if congestion == "" {
		congestion = "bbr"
	}
	serverTLS := &tls.Config{
		Time:       ntp.Now,
		MinVersion: tls.VersionTLS13,
		NextProtos: strings.Split(alpn, ","),
	}
	certLoader, err := ca.NewTLSKeyPairLoader(certificate, privateKey)
	if err != nil {
		_ = pc.Close()
		return nil, tuicStartError{cause: err}
	}
	userID, err := uuid.FromString(secret["uuid"])
	if err != nil {
		_ = pc.Close()
		return nil, tuicStartError{cause: err}
	}
	serverTLS.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return certLoader()
	}
	quicConfig := &quic.Config{
		MaxIdleTimeout:                 15 * time.Second,
		MaxIncomingStreams:             (1 << 32) - 1,
		MaxIncomingUniStreams:          (1 << 32) - 1,
		EnableDatagrams:                true,
		Allow0RTT:                      true,
		DisablePathManager:             true,
		InitialStreamReceiveWindow:     transporttuic.DefaultStreamReceiveWindow / 10,
		MaxStreamReceiveWindow:         transporttuic.DefaultStreamReceiveWindow,
		InitialConnectionReceiveWindow: transporttuic.DefaultConnectionReceiveWindow / 10,
		MaxConnectionReceiveWindow:     transporttuic.DefaultConnectionReceiveWindow,
		MaxDatagramFrameSize:           1400,
	}
	listenerHandler, err := sing.NewListenerHandler(sing.ListenerConfig{
		Tunnel:    tuicScopedTunnel{Tunnel: env.Tunnel, login: login},
		Type:      C.TUIC,
		Additions: []inbound.Addition{inbound.WithInName(spec.Name), inbound.WithInUser(login)},
	})
	if err != nil {
		_ = pc.Close()
		return nil, tuicStartError{cause: err}
	}
	option := &transporttuic.ServerOption{
		TlsConfig:             serverTLS,
		QuicConfig:            quicConfig,
		Users:                 map[[16]byte]string{userID: secret["password"]},
		CongestionController:  congestion,
		AuthenticationTimeout: 5 * time.Second,
		MaxUdpRelayPacketSize: 1252,
		CWND:                  32,
		HandleTcpFn: func(conn net.Conn, addr socks5.Addr, additions ...inbound.Addition) error {
			listenerHandler.HandleSocket(addr, conn, additions...)
			return nil
		},
		HandleUdpFn: func(addr socks5.Addr, packet C.UDPPacket, additions ...inbound.Addition) error {
			wrapped, metadata := inbound.NewPacket(addr, packet, C.TUIC, append([]inbound.Addition{inbound.WithInName(spec.Name), inbound.WithInUser(login)}, additions...)...)
			metadata.InUser = login
			env.Tunnel.HandleUDPPacket(wrapped, metadata)
			return nil
		},
	}
	server, err := transporttuic.NewServer(option, pc)
	if err != nil {
		_ = pc.Close()
		return nil, tuicStartError{cause: err}
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	return &tuicCloser{server: server, packet: pc, serveDone: serveDone}, nil
}

type tuicScopedTunnel struct {
	C.Tunnel
	login string
}

func (t tuicScopedTunnel) HandleTCPConn(conn net.Conn, m *C.Metadata) {
	if m != nil {
		m.InUser = t.login
	}
	t.Tunnel.HandleTCPConn(conn, m)
}
func (t tuicScopedTunnel) HandleUDPPacket(p C.UDPPacket, m *C.Metadata) {
	if m != nil {
		m.InUser = t.login
	}
	t.Tunnel.HandleUDPPacket(p, m)
}

type tuicStartError struct{ cause error }

func (e tuicStartError) Error() string        { return "tuic listener start failed" }
func (e tuicStartError) String() string       { return e.Error() }
func (e tuicStartError) GoString() string     { return e.Error() }
func (e tuicStartError) Is(target error) bool { return errors.Is(e.cause, target) }

type tuicCloser struct {
	server    interface{ Close() error }
	packet    net.PacketConn
	serveDone chan error
	once      sync.Once
	err       error
}

func (c *tuicCloser) Close() error {
	c.once.Do(func() {
		c.err = c.server.Close()
		if c.err != nil && !errors.Is(c.err, net.ErrClosed) && !errors.Is(c.err, quic.ErrServerClosed) {
			c.err = tuicStartError{cause: c.err}
		} else {
			c.err = nil
		}
		if err := c.packet.Close(); c.err == nil && err != nil && !errors.Is(err, net.ErrClosed) {
			c.err = tuicStartError{cause: err}
		}
		if serveErr := <-c.serveDone; c.err == nil && serveErr != nil && !errors.Is(serveErr, net.ErrClosed) && !errors.Is(serveErr, quic.ErrServerClosed) {
			c.err = tuicStartError{cause: serveErr}
		}
	})
	return c.err
}

func validTUICSecret(secret map[string]string) bool {
	if secret["uuid"] == "" || secret["password"] == "" {
		return false
	}
	value := secret["uuid"]
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	id, err := hex.DecodeString(compact)
	return err == nil && len(id) == 16 && id[6]>>4 == 4 && id[8]>>6 == 2
}

func (tuicProtocol) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "tuic" {
		return "", errors.New("tuic protocol mismatch")
	}
	if len(spec.Accounts) != 1 || spec.Accounts[0] != login {
		return "", errors.New("tuic requires its configured account")
	}
	if host == "" {
		return "", errors.New("host is required")
	}
	secret := spec.Users[login]
	if !validTUICSecret(secret) {
		return "", errors.New("tuic user secret missing or invalid")
	}
	query := url.Values{}
	sni := spec.Params["sni"]
	if sni == "" {
		sni = host
	}
	query.Set("sni", sni)
	alpn := spec.Params["alpn"]
	if alpn == "" {
		alpn = "h3"
	}
	query.Set("alpn", alpn)
	congestion := spec.Params["congestion"]
	if congestion == "" {
		congestion = "bbr"
	}
	query.Set("congestion_control", congestion)
	mode := spec.Params["udp_relay_mode"]
	if mode == "" {
		mode = "native"
	}
	query.Set("udp_relay_mode", mode)
	query.Set("allow_insecure", "0")
	if pin := TLSFingerprint(spec.Material); pin == "" {
		return "", errors.New("tuic TLS fingerprint unavailable")
	}
	return (&url.URL{Scheme: "tuic", User: url.UserPassword(secret["uuid"], secret["password"]), Host: net.JoinHostPort(host, strconv.Itoa(spec.Port)), RawQuery: query.Encode(), Fragment: spec.Name}).String(), nil
}

func (tuicProtocol) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "tuic" {
		return "", errors.New("tuic protocol mismatch")
	}
	if len(spec.Accounts) != 1 || spec.Accounts[0] != login {
		return "", errors.New("tuic requires its configured account")
	}
	if host == "" {
		return "", errors.New("host is required")
	}
	secret := spec.Users[login]
	if !validTUICSecret(secret) {
		return "", errors.New("tuic user secret missing or invalid")
	}
	alpn := spec.Params["alpn"]
	if alpn == "" {
		alpn = "h3"
	}
	congestion := spec.Params["congestion"]
	if congestion == "" {
		congestion = "bbr"
	}
	sni := spec.Params["sni"]
	if sni == "" {
		sni = host
	}
	mode := spec.Params["udp_relay_mode"]
	if mode == "" {
		mode = "native"
	}
	pin := TLSFingerprint(spec.Material)
	if pin == "" {
		return "", errors.New("tuic TLS fingerprint unavailable")
	}
	proxy := map[string]any{
		"name": spec.Name, "type": "tuic", "server": host, "port": spec.Port,
		"uuid": secret["uuid"], "password": secret["password"], "sni": sni,
		"alpn": strings.Split(alpn, ","), "congestion-controller": congestion,
		"udp-relay-mode": mode, "udp": false, "skip-cert-verify": false,
	}
	proxy["fingerprint"] = pin
	data, err := yaml.Marshal(map[string]any{"proxies": []any{proxy}})
	if err != nil {
		return "", errors.New("encode tuic Mihomo snippet failed")
	}
	return string(data), nil
}

var _ Protocol = tuicProtocol{}
var _ MihomoLinker = tuicProtocol{}
var _ UDPBound = tuicProtocol{}
