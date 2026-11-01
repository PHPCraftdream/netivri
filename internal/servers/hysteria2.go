package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/component/ca"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/sing"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/ntp"
	"github.com/metacubex/mihomo/transport/tuic/common"
	"github.com/metacubex/quic-go"
	hy2 "github.com/metacubex/sing-quic/hysteria2"
	"github.com/metacubex/tls"
)

type hysteria2 struct{}

func init()                        { Register(hysteria2{}) }
func (hysteria2) Name() string     { return "hysteria2" }
func (hysteria2) SingleUser() bool { return false }
func (hysteria2) UDPBound() bool   { return true }

func (hysteria2) Validate(p map[string]string) error {
	if err := ValidateTLSParams(p); err != nil {
		return err
	}
	allowed := map[string]bool{"sni": true, "public_host": true, "cert_file": true, "key_file": true, "obfs": true, "up": true, "down": true}
	for direction := range map[string]bool{"up": true, "down": true} {
		if value, ok := p[direction]; ok {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil || parsed == 0 || parsed > uint64(math.MaxInt64/1_000_000) {
				return fmt.Errorf("invalid %s bandwidth", direction)
			}
		}
	}
	for k := range p {
		if !allowed[k] {
			return fmt.Errorf("unknown parameter %q", k)
		}
	}
	if host, ok := p["public_host"]; ok && !validTLSHost(host) {
		return errors.New("invalid public_host")
	}
	if p["obfs"] != "" && p["obfs"] != "salamander" {
		return errors.New("unsupported obfs")
	}
	return nil
}

func (hysteria2) NewMaterial(p map[string]string) (map[string]string, error) {
	if err := (hysteria2{}).Validate(p); err != nil {
		return nil, err
	}
	host := p["public_host"]
	if host == "" {
		host = p["sni"]
	}
	m, err := NewTLSMaterial(p, host)
	if err != nil {
		return nil, err
	}
	if p["obfs"] == "salamander" {
		m["obfs_password"], err = randomHysteriaSecret()
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func randomHysteriaSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("generate Hysteria2 secret failed")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (hysteria2) NewSecret(_ string, p map[string]string) (map[string]string, error) {
	if err := (hysteria2{}).Validate(p); err != nil {
		return nil, err
	}
	secret, err := randomHysteriaSecret()
	if err != nil {
		return nil, err
	}
	return map[string]string{"password": secret}, nil
}

type hysteria2Closer struct {
	service *hy2.Service[string]
	pc      net.PacketConn
	once    sync.Once
	err     error
}

func (c *hysteria2Closer) Close() error {
	c.once.Do(func() {
		c.err = c.service.Close()
		if err := c.pc.Close(); c.err == nil && !errors.Is(err, net.ErrClosed) {
			c.err = err
		}
	})
	return c.err
}

func (hysteria2) Start(s Spec, env Env) (io.Closer, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Protocol != "hysteria2" {
		return nil, errors.New("hysteria2 protocol mismatch")
	}
	users := make(map[string]string, len(s.Accounts))
	seen := map[string]bool{}
	for _, login := range s.Accounts {
		password := s.Users[login]["password"]
		if password == "" {
			return nil, errors.New("hysteria2 secret missing")
		}
		if seen[password] {
			return nil, errors.New("hysteria2 secrets must be unique per account")
		}
		seen[password] = true
		users[login] = password
	}
	cert, key := TLSFiles(s.Material)
	if cert == "" || key == "" {
		return nil, errors.New("hysteria2 TLS material missing")
	}
	if s.Params["obfs"] == "salamander" && s.Material["obfs_password"] == "" {
		return nil, errors.New("hysteria2 obfs material missing")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("hysteria2 start requires tunnel and listen config")
	}
	addr := s.ListenAddress()
	pc, err := env.ListenConfig.ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return nil, hysteria2StartError{err}
	}
	closePacket := func() { _ = pc.Close() }

	tlsConfig := &tls.Config{Time: ntp.Now, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}}
	certLoader, err := ca.NewTLSKeyPairLoader(cert, key)
	if err != nil {
		closePacket()
		return nil, hysteria2StartError{err}
	}
	tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return certLoader()
	}
	handler, err := sing.NewListenerHandler(sing.ListenerConfig{
		Tunnel:    env.Tunnel,
		Type:      C.HYSTERIA2,
		Additions: []inbound.Addition{inbound.WithInName(s.Name)},
		MuxOption: sing.MuxOption{},
	})
	if err != nil {
		closePacket()
		return nil, hysteria2StartError{err}
	}
	salamanderPassword := ""
	if s.Params["obfs"] == "salamander" {
		salamanderPassword = s.Material["obfs_password"]
	}
	service, err := hy2.NewService[string](hy2.ServiceOptions{
		Context:            context.Background(),
		Logger:             log.SingLogger,
		SalamanderPassword: salamanderPassword,
		TLSConfig:          tlsConfig,
		QUICConfig:         &quic.Config{},
		UDPTimeout:         sing.UDPTimeout,
		UdpMTU:             1197,
		UDPDisabled:        false,
		Handler:            handler,
		SetBBRCongestion: func(conn *quic.Conn) {
			common.SetCongestionController(conn, "bbr", 0, "")
		},
	})
	if err != nil {
		closePacket()
		return nil, hysteria2StartError{err}
	}
	userNames := make([]string, 0, len(users))
	passwords := make([]string, 0, len(users))
	for login, password := range users {
		userNames = append(userNames, login)
		passwords = append(passwords, password)
	}
	service.UpdateUsers(userNames, passwords)
	if err := service.Start(pc); err != nil {
		_ = service.Close()
		closePacket()
		return nil, hysteria2StartError{err}
	}
	return &hysteria2Closer{service: service, pc: pc}, nil
}

type hysteria2StartError struct{ cause error }

func (e hysteria2StartError) Error() string        { return "hysteria2 listener start failed" }
func (e hysteria2StartError) String() string       { return e.Error() }
func (e hysteria2StartError) GoString() string     { return e.Error() }
func (e hysteria2StartError) Is(target error) bool { return errors.Is(e.cause, target) }

func hysteria2SpecSecret(s Spec, login, host string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Protocol != "hysteria2" || host == "" {
		return "", errors.New("host and Hysteria2 protocol are required")
	}
	found := false
	for _, account := range s.Accounts {
		if account == login {
			found = true
		}
	}
	if !found {
		return "", errors.New("Hysteria2 account not configured")
	}
	if TLSFingerprint(s.Material) == "" {
		return "", errors.New("TLS certificate material is required")
	}
	if s.Params["obfs"] == "salamander" && s.Material["obfs_password"] == "" {
		return "", errors.New("obfs material is required")
	}
	password := s.Users[login]["password"]
	if password == "" {
		return "", errors.New("account password is required")
	}
	return password, nil

}

func (hysteria2) Link(s Spec, login, host string) (string, error) {
	password, err := hysteria2SpecSecret(s, login, host)
	if err != nil {
		return "", err
	}
	if strings.Trim(password, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-.") != "" {
		return "", errors.New("password contains characters unsupported by Mihomo Hysteria2 URI importer")
	}
	sni := s.Params["sni"]
	if sni == "" {
		sni = host
	}
	u := url.URL{Scheme: "hysteria2", User: url.User(password), Host: net.JoinHostPort(host, strconv.Itoa(s.Port)), Fragment: s.Name}
	q := u.Query()
	q.Set("sni", sni)
	q.Set("insecure", "0")
	q.Set("pinSHA256", TLSFingerprint(s.Material))
	for _, direction := range []string{"up", "down"} {
		if value := s.Params[direction]; value != "" {
			q.Set(direction, value)
		}
	}
	if s.Params["obfs"] != "" {
		q.Set("obfs", s.Params["obfs"])
		q.Set("obfs-password", s.Material["obfs_password"])
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (hysteria2) Mihomo(s Spec, login, host string) (string, error) {
	password, err := hysteria2SpecSecret(s, login, host)
	if err != nil {
		return "", err
	}
	sni := s.Params["sni"]
	if sni == "" {
		sni = host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n  - name: %q\n    type: hysteria2\n    server: %q\n    port: %d\n    password: %q\n    sni: %q\n    skip-cert-verify: false\n    udp: false\n", s.Name, host, s.Port, password, sni)
	fmt.Fprintf(&b, "    fingerprint: %q\n", TLSFingerprint(s.Material))
	for _, direction := range []string{"up", "down"} {
		if value := s.Params[direction]; value != "" {
			fmt.Fprintf(&b, "    %s: %q\n", direction, value)
		}
	}
	if s.Params["obfs"] != "" {
		fmt.Fprintf(&b, "    obfs: %q\n    obfs-password: %q\n", s.Params["obfs"], s.Material["obfs_password"])
	}
	return b.String(), nil
}

var _ Protocol = hysteria2{}
var _ MihomoLinker = hysteria2{}
var _ UDPBound = hysteria2{}
