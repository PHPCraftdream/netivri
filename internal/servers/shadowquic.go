package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/jls-quic-go"
	"github.com/metacubex/jls-tls"
	"github.com/metacubex/mihomo/adapter/inbound"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/sing"
	quictransport "github.com/metacubex/mihomo/transport/shadowquic"
	"github.com/metacubex/mihomo/transport/socks5"
	"github.com/metacubex/mihomo/transport/tuic"
)

type shadowquic struct{}

func init()                         { Register(shadowquic{}) }
func (shadowquic) Name() string     { return "shadowquic" }
func (shadowquic) SingleUser() bool { return false }
func (shadowquic) UDPBound() bool   { return true }

func (shadowquic) Validate(p map[string]string) error {
	allowed := map[string]bool{
		"sni": true, "public_host": true, "cert_file": true, "key_file": true,
		"alpn": true, "quic_versions": true, "max_idle_time": true,
		"max_datagram_frame_size": true, "recv_window_conn": true,
		"recv_window": true, "disable_mtu_discovery": true,
		"congestion_controller": true,
	}
	for k := range p {
		if !allowed[k] {
			return fmt.Errorf("unknown parameter %q", k)
		}
	}
	if err := ValidateTLSParams(p); err != nil {
		return err
	}
	if v, ok := p["public_host"]; ok && (v == "" || !validTLSHost(v)) {
		return errors.New("invalid public_host")
	}
	if v, ok := p["sni"]; ok && (v == "" || !validTLSHost(v)) {
		return errors.New("invalid sni")
	}
	if v, ok := p["max_idle_time"]; ok {
		if v == "" {
			return errors.New("invalid max_idle_time")
		}
		n, err := strconv.ParseUint(v, 10, 31)
		if err != nil || n == 0 {
			return errors.New("invalid max_idle_time")
		}
	}
	if v, ok := p["max_datagram_frame_size"]; ok {
		if v == "" {
			return errors.New("invalid max_datagram_frame_size")
		}
		n, err := strconv.ParseUint(v, 10, 31)
		if err != nil || n < 1024 {
			return errors.New("invalid max_datagram_frame_size")
		}
	}
	for _, key := range []string{"recv_window_conn", "recv_window"} {
		if v, ok := p[key]; ok {
			if v == "" {
				return fmt.Errorf("invalid %s", key)
			}
			n, err := strconv.ParseUint(v, 10, 31)
			if err != nil || n == 0 {
				return fmt.Errorf("invalid %s", key)
			}
		}
	}
	if v, ok := p["disable_mtu_discovery"]; ok && v != "true" && v != "false" {
		return errors.New("invalid disable_mtu_discovery")
	}
	if v, ok := p["congestion_controller"]; ok && v != "bbr" && v != "cubic" && v != "new_reno" {
		return errors.New("invalid congestion_controller")
	}
	if v, ok := p["alpn"]; ok {
		if v == "" {
			return errors.New("invalid alpn")
		}
		for _, token := range strings.Split(v, ",") {
			if token == "" || strings.TrimSpace(token) != token {
				return errors.New("invalid alpn")
			}
		}
	}
	if v, ok := p["quic_versions"]; ok {
		if v == "" {
			return errors.New("invalid quic_versions")
		}
		if _, err := quictransport.ParseQUICVersions(strings.Split(v, ",")); err != nil {
			return errors.New("invalid quic_versions")
		}
	}
	return nil
}
func (shadowquic) NewMaterial(p map[string]string) (map[string]string, error) {
	if err := (shadowquic{}).Validate(p); err != nil {
		return nil, err
	}
	host := p["public_host"]
	if host == "" {
		host = p["sni"]
	}
	if host == "" {
		host = "localhost"
	}
	return NewTLSMaterial(p, host)
}
func (shadowquic) NewSecret(_ string, p map[string]string) (map[string]string, error) {
	if err := (shadowquic{}).Validate(p); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, errors.New("generate ShadowQUIC secret failed")
	}
	return map[string]string{"password": base64.RawURLEncoding.EncodeToString(b)}, nil
}

type shadowquicCloser struct {
	server *quictransport.Server
	pc     net.PacketConn
	once   sync.Once
	err    error
}

func (c *shadowquicCloser) Close() error {
	c.once.Do(func() {
		c.err = c.server.Close()
		if err := c.pc.Close(); c.err == nil && !errors.Is(err, net.ErrClosed) {
			c.err = err
		}
	})
	if c.err != nil {
		return errors.New("ShadowQUIC listener close failed")
	}
	return nil
}

type shadowquicStartError struct{ cause error }

func (e shadowquicStartError) Error() string        { return "ShadowQUIC listener start failed" }
func (e shadowquicStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e shadowquicStartError) String() string       { return e.Error() }
func (e shadowquicStartError) GoString() string     { return e.Error() }

func (shadowquic) Start(s Spec, env Env) (io.Closer, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Protocol != "shadowquic" {
		return nil, errors.New("ShadowQUIC protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("ShadowQUIC start requires tunnel and listen config")
	}
	users := make([]tls.JLSUser, 0, len(s.Accounts))
	secrets := make(map[string]bool, len(s.Accounts))
	for _, login := range s.Accounts {
		password := s.Users[login]["password"]
		raw, err := base64.RawURLEncoding.DecodeString(password)
		if err != nil || len(raw) != 32 {
			return nil, errors.New("invalid ShadowQUIC secret")
		}
		if secrets[password] {
			return nil, errors.New("duplicate ShadowQUIC secret")
		}
		secrets[password] = true
		users = append(users, tls.JLSUser{Username: login, Password: password})
	}
	cert, key := TLSFiles(s.Material)
	if cert == "" || key == "" {
		return nil, errors.New("ShadowQUIC TLS material missing")
	}
	if !strings.HasPrefix(cert, "-----BEGIN ") {
		certBytes, err := os.ReadFile(cert)
		if err != nil {
			return nil, errors.New("ShadowQUIC TLS material invalid")
		}
		cert = string(certBytes)
	}
	if !strings.HasPrefix(key, "-----BEGIN ") {
		keyBytes, err := os.ReadFile(key)
		if err != nil {
			return nil, errors.New("ShadowQUIC TLS material invalid")
		}
		key = string(keyBytes)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		return nil, errors.New("ShadowQUIC TLS material invalid")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: shadowquicALPN(s.Params["alpn"]),
		JLSConfig: &tls.JLSConfig{
			Enable:     true,
			Users:      users,
			ServerName: shadowquicServerName(s.Params),
		},
		Certificates: []tls.Certificate{pair},
	}
	versions := quictransport.DefaultQUICVersions()
	if v := s.Params["quic_versions"]; v != "" {
		versions, err = quictransport.ParseQUICVersions(strings.Split(v, ","))
		if err != nil {
			return nil, errors.New("invalid ShadowQUIC QUIC versions")
		}
	}
	idle := time.Duration(shadowquicInt(s.Params, "max_idle_time", 30000)) * time.Millisecond
	qc := &quic.Config{
		Versions:                versions,
		MaxIdleTimeout:          idle,
		MaxIncomingStreams:      (1 << 32) - 1,
		MaxIncomingUniStreams:   (1 << 32) - 1,
		EnableDatagrams:         true,
		MaxDatagramFrameSize:    int64(shadowquicInt(s.Params, "max_datagram_frame_size", 1400)),
		DisablePathMTUDiscovery: s.Params["disable_mtu_discovery"] == "true",
	}
	if n := shadowquicInt(s.Params, "recv_window_conn", 0); n == 0 {
		qc.InitialStreamReceiveWindow = tuic.DefaultStreamReceiveWindow / 10
		qc.MaxStreamReceiveWindow = tuic.DefaultStreamReceiveWindow
	} else {
		qc.InitialStreamReceiveWindow = uint64(n)
		qc.MaxStreamReceiveWindow = uint64(n)
	}
	if n := shadowquicInt(s.Params, "recv_window", 0); n == 0 {
		qc.InitialConnectionReceiveWindow = tuic.DefaultConnectionReceiveWindow / 10
		qc.MaxConnectionReceiveWindow = tuic.DefaultConnectionReceiveWindow
	} else {
		qc.InitialConnectionReceiveWindow = uint64(n)
		qc.MaxConnectionReceiveWindow = uint64(n)
	}
	pc, err := env.ListenConfig.ListenPacket(context.Background(), "udp", s.ListenAddress())
	if err != nil {
		return nil, shadowquicStartError{err}
	}
	handler, err := sing.NewListenerHandler(sing.ListenerConfig{Tunnel: env.Tunnel, Type: C.SHADOWQUIC, Additions: []inbound.Addition{inbound.WithInName(s.Name)}})
	if err != nil {
		_ = pc.Close()
		return nil, shadowquicStartError{err}
	}
	congestionController := s.Params["congestion_controller"]
	if congestionController == "" {
		congestionController = "bbr"
	}
	option := &quictransport.ServerOption{
		TLSConfig:            tlsConfig,
		QUICConfig:           qc,
		CongestionController: congestionController,
		HandleTcpFn: func(conn net.Conn, addr socks5.Addr, additions ...inbound.Addition) error {
			go handler.HandleSocket(addr, conn, additions...)
			return nil
		},
	}
	server, err := quictransport.NewServer(option, pc)
	if err != nil {
		_ = pc.Close()
		return nil, shadowquicStartError{err}
	}
	go func() { _ = server.Serve() }()
	return &shadowquicCloser{server: server, pc: pc}, nil
}
func shadowquicALPN(v string) []string {
	if v == "" {
		return []string{"h3"}
	}
	return strings.Split(v, ",")
}
func shadowquicInt(p map[string]string, k string, d int) int {
	if p[k] == "" {
		return d
	}
	n, _ := strconv.Atoi(p[k])
	return n
}
func shadowquicServerName(params map[string]string) string {
	if sni := params["sni"]; sni != "" {
		return sni
	}
	return "localhost"
}

func (shadowquic) Link(s Spec, login, host string) (string, error) {
	return "", errors.New("ShadowQUIC has no standard URI; use the Mihomo snippet")
}
func (shadowquic) Mihomo(s Spec, login, host string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Protocol != "shadowquic" || host == "" {
		return "", errors.New("host and ShadowQUIC protocol are required")
	}
	if !containsAccount(s.Accounts, login) || s.Users[login]["password"] == "" {
		return "", errors.New("configured account and secret are required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.Users[login]["password"])
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid ShadowQUIC secret")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n  - name: %q\n    type: shadowquic\n    server: %q\n    port: %d\n    username: %q\n    password: %q\n    sni: %q\n", s.Name, host, s.Port, login, s.Users[login]["password"], shadowquicServerName(s.Params))
	if s.Params["alpn"] != "" {
		fmt.Fprintf(&b, "    alpn:\n")
		for _, token := range strings.Split(s.Params["alpn"], ",") {
			fmt.Fprintf(&b, "      - %q\n", token)
		}
	}
	if s.Params["quic_versions"] != "" {
		fmt.Fprintf(&b, "    quic-versions:\n")
		for _, version := range strings.Split(s.Params["quic_versions"], ",") {
			fmt.Fprintf(&b, "      - %q\n", version)
		}
	}
	if s.Params["congestion_controller"] != "" {
		fmt.Fprintf(&b, "    congestion-controller: %q\n", s.Params["congestion_controller"])
	}
	fmt.Fprintf(&b, "    udp: false\n")
	return b.String(), nil
}

var _ Protocol = shadowquic{}
var _ MihomoLinker = shadowquic{}
var _ UDPBound = shadowquic{}
