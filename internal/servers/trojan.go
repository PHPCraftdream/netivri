package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	LC "github.com/metacubex/mihomo/listener/config"
	trojanlistener "github.com/metacubex/mihomo/listener/trojan"
)

type trojanAcceptListener struct {
	net.Listener
	done chan struct{}
	once sync.Once
}

func (l *trojanAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.done
		return nil, err
	}
	return conn, nil
}
func (l *trojanAcceptListener) Close() error {
	var err error
	l.once.Do(func() { close(l.done); err = l.Listener.Close() })
	return err
}

type trojanCloser struct {
	listener io.Closer
	accept   *trojanAcceptListener
	once     sync.Once
	err      error
}

func (c *trojanCloser) Close() error {
	c.once.Do(func() {
		c.err = c.listener.Close()
		_ = c.accept.Close()
	})
	return c.err
}

type trojanStartError struct{ cause error }

func (e trojanStartError) Error() string        { return "trojan listener start failed" }
func (e trojanStartError) String() string       { return e.Error() }
func (e trojanStartError) GoString() string     { return e.Error() }
func (e trojanStartError) Is(target error) bool { return errors.Is(e.cause, target) }

type trojanServer struct{}

func (trojanServer) MatchKeys(params map[string]string) ([]string, []string, bool) {
	return tlsMatchKeyValues(params["sni"], params["alpn"], params["sni"] != "")
}

func init() { Register(trojanServer{}) }

func (trojanServer) Name() string     { return "trojan" }
func (trojanServer) SingleUser() bool { return false }

func (trojanServer) Validate(params map[string]string) error {
	for key := range params {
		switch key {
		case "public_host", "sni", "alpn", "cert_file", "key_file":
		default:
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	if host := params["public_host"]; host != "" && !validTLSHost(host) {
		return fmt.Errorf("invalid public_host")
	}
	return ValidateTLSParams(params)
}
func (trojanServer) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (trojanServer{}).Validate(params); err != nil {
		return nil, err
	}
	host := params["public_host"]
	if host == "" && validTLSHost(params["sni"]) {
		host = params["sni"]
	}
	return NewTLSMaterial(params, host)
}
func (trojanServer) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (trojanServer{}).Validate(params); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate Trojan secret failed")
	}
	return map[string]string{"password": base64.RawURLEncoding.EncodeToString(raw)}, nil
}
func (trojanServer) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "trojan" {
		return nil, fmt.Errorf("trojan protocol mismatch")
	}
	users := make([]LC.TrojanUser, 0, len(spec.Accounts))
	passwords := make(map[string]struct{}, len(spec.Accounts))
	for _, login := range spec.Accounts {
		password := spec.Users[login]["password"]
		if password == "" {
			return nil, fmt.Errorf("trojan secret missing")
		}
		if _, exists := passwords[password]; exists {
			return nil, errors.New("trojan secrets must be unique per account")
		}
		passwords[password] = struct{}{}
		users = append(users, LC.TrojanUser{Username: login, Password: password})
	}
	address := spec.ListenAddress()
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, fmt.Errorf("trojan start requires tunnel and listen config")
	}
	certificate, privateKey := TLSFiles(spec.Material)
	if certificate == "" || privateKey == "" {
		return nil, fmt.Errorf("trojan TLS material missing")
	}
	prepared := &preparedListenConfig{address: address}
	listener, err := env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, trojanStartError{cause: err}
	}
	acceptListener := &trojanAcceptListener{Listener: listener, done: make(chan struct{})}
	prepared.tcp = acceptListener
	running, err := trojanlistener.New(LC.TrojanServer{Enable: true, Listen: address, Users: users, Certificate: certificate, PrivateKey: privateKey}, prepared, env.Tunnel, inbound.WithInName(spec.Name))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, trojanStartError{cause: err}
	}
	return &trojanCloser{listener: running, accept: acceptListener}, nil
}
func (trojanServer) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "trojan" {
		return "", fmt.Errorf("trojan protocol mismatch")
	}
	if !trojanHasAccount(spec, login) || host == "" {
		return "", fmt.Errorf("host and configured account are required")
	}
	password := spec.Users[login]["password"]
	if password == "" {
		return "", fmt.Errorf("trojan secret missing")
	}
	query := url.Values{"security": {"tls"}, "sni": {trojanSNI(spec.Params, host)}, "type": {"tcp"}, "allowInsecure": {"0"}}
	if alpn := spec.Params["alpn"]; alpn != "" {
		query.Set("alpn", alpn)
	}
	pin := TLSFingerprint(spec.Material)
	if pin == "" {
		return "", errors.New("trojan TLS fingerprint unavailable")
	}
	query.Set("pcs", pin)
	return (&url.URL{Scheme: "trojan", User: url.User(password), Host: net.JoinHostPort(host, strconv.Itoa(spec.Port)), RawQuery: query.Encode(), Fragment: spec.Name}).String(), nil
}
func (p trojanServer) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "trojan" {
		return "", fmt.Errorf("trojan protocol mismatch")
	}
	if !trojanHasAccount(spec, login) || host == "" {
		return "", fmt.Errorf("host and configured account are required")
	}
	password := spec.Users[login]["password"]
	if password == "" {
		return "", fmt.Errorf("trojan secret missing")
	}
	if TLSFingerprint(spec.Material) == "" {
		return "", errors.New("trojan TLS fingerprint unavailable")
	}
	return fmt.Sprintf("proxies:\n  - name: %q\n    type: trojan\n    server: %q\n    port: %d\n    password: %q\n    sni: %q\n    fingerprint: %q\n    skip-cert-verify: false\n    udp: false\n%s", spec.Name, host, spec.Port, password, trojanSNI(spec.Params, host), TLSFingerprint(spec.Material), trojanALPN(spec.Params)), nil
}
func trojanHasAccount(spec Spec, login string) bool {
	for _, account := range spec.Accounts {
		if account == login {
			return true
		}
	}
	return false
}
func trojanALPN(params map[string]string) string {
	if params["alpn"] == "" {
		return ""
	}
	tokens := strings.Split(params["alpn"], ",")
	for i := range tokens {
		tokens[i] = strconv.Quote(tokens[i])
	}
	return fmt.Sprintf("    alpn: [%s]\n", strings.Join(tokens, ", "))
}

func trojanSNI(params map[string]string, host string) string {
	if sni := params["sni"]; sni != "" {
		return sni
	}
	return host
}
