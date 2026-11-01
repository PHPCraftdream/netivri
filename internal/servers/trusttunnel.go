package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/metacubex/mihomo/adapter/inbound"
	LC "github.com/metacubex/mihomo/listener/config"
	trusttunnellistener "github.com/metacubex/mihomo/listener/trusttunnel"
)

const trusttunnelSecretBytes = 32

type trusttunnelProtocol struct{}

func (trusttunnelProtocol) MatchKeys(params map[string]string) ([]string, []string, bool) {
	sni, _, explicit := tlsMatchKeyValues(params["sni"], "", params["sni"] != "")
	if params["sni"] == "" {
		return nil, []string{"h2"}, true
	}
	return sni, []string{"h2"}, explicit
}

func init()                                  { Register(trusttunnelProtocol{}) }
func (trusttunnelProtocol) Name() string     { return "trusttunnel" }
func (trusttunnelProtocol) SingleUser() bool { return false }

func (trusttunnelProtocol) Validate(params map[string]string) error {
	for key := range params {
		switch key {
		case "sni", "alpn", "public_host", "cert_file", "key_file", "network", "udp", "quic":
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
	if network := params["network"]; network != "" && network != "tcp" {
		return errors.New("network must be tcp")
	}
	if alpn := params["alpn"]; alpn != "" && alpn != "h2" {
		return errors.New("alpn must be h2")
	}
	if udp := params["udp"]; udp != "" && udp != "false" {
		if udp == "true" {
			return errors.New("udp is unsupported for TrustTunnel servers")
		}
		return errors.New("invalid udp parameter")
	}
	if quic := params["quic"]; quic != "" && quic != "false" {
		if quic == "true" {
			return errors.New("quic is unsupported for TrustTunnel servers")
		}
		return errors.New("invalid quic parameter")
	}
	return nil
}

func (trusttunnelProtocol) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (trusttunnelProtocol{}).Validate(params); err != nil {
		return nil, err
	}
	host := params["public_host"]
	if host == "" {
		host = params["sni"]
	}
	return NewTLSMaterial(params, host)
}

func (trusttunnelProtocol) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (trusttunnelProtocol{}).Validate(params); err != nil {
		return nil, fmt.Errorf("invalid TrustTunnel parameters: %w", err)
	}
	raw := make([]byte, trusttunnelSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, errors.New("generate TrustTunnel secret failed")
	}
	return map[string]string{"password": base64.RawURLEncoding.EncodeToString(raw)}, nil
}

func (trusttunnelProtocol) Start(spec Spec, env Env) (io.Closer, error) {
	if len(spec.Accounts) == 0 {
		return nil, errors.New("TrustTunnel accounts are required")
	}
	if err := (trusttunnelProtocol{}).Validate(spec.Params); err != nil {
		return nil, fmt.Errorf("invalid TrustTunnel parameters: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "trusttunnel" {
		return nil, errors.New("TrustTunnel protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("TrustTunnel start requires tunnel and listen config")
	}
	certificate, privateKey := TLSFiles(spec.Material)
	if certificate == "" || privateKey == "" {
		return nil, errors.New("TrustTunnel TLS certificate and private key are required")
	}
	users := make(map[string]string, len(spec.Accounts))
	for _, login := range spec.Accounts {
		users[login] = spec.Users[login]["password"]
		if !trusttunnelSecretValid(users[login]) {
			return nil, errors.New("invalid TrustTunnel account secret")
		}
	}
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	var err error
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, trusttunnelSafeStartError{cause: err}
	}
	config := LC.TrustTunnelServer{
		Enable:      true,
		Listen:      address,
		Users:       users,
		Certificate: certificate,
		PrivateKey:  privateKey,
		Network:     []string{"tcp"},
	}
	listener, err := trusttunnellistener.New(config, prepared, env.Tunnel, inbound.WithInName(spec.Name))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, trusttunnelSafeStartError{cause: err}
	}
	return listener, nil
}

type trusttunnelSafeStartError struct{ cause error }

func (e trusttunnelSafeStartError) Error() string        { return "TrustTunnel listener start failed" }
func (e trusttunnelSafeStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e trusttunnelSafeStartError) String() string       { return e.Error() }
func (e trusttunnelSafeStartError) GoString() string     { return e.Error() }

func trusttunnelSecretValid(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == trusttunnelSecretBytes
}

func trusttunnelContainsAccount(accounts []string, login string) bool {
	for _, account := range accounts {
		if account == login {
			return true
		}
	}
	return false
}

func (trusttunnelProtocol) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if err := (trusttunnelProtocol{}).Validate(spec.Params); err != nil {
		return "", fmt.Errorf("invalid TrustTunnel parameters: %w", err)
	}
	if spec.Protocol != "trusttunnel" {
		return "", errors.New("TrustTunnel protocol mismatch")
	}
	if host == "" {
		host = spec.Params["public_host"]
	}
	if host == "" || !validTLSHost(host) {
		return "", errors.New("a valid public host is required")
	}
	if !trusttunnelContainsAccount(spec.Accounts, login) || !trusttunnelSecretValid(spec.Users[login]["password"]) {
		return "", errors.New("configured account and valid secret are required")
	}
	return "", errors.New("TrustTunnel does not define a standard URI; use the Mihomo snippet")
}

func (trusttunnelProtocol) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if err := (trusttunnelProtocol{}).Validate(spec.Params); err != nil {
		return "", fmt.Errorf("invalid TrustTunnel parameters: %w", err)
	}
	if spec.Protocol != "trusttunnel" {
		return "", errors.New("TrustTunnel protocol mismatch")
	}
	if host == "" {
		host = spec.Params["public_host"]
	}
	if host == "" || !validTLSHost(host) {
		return "", errors.New("a valid public host is required")
	}
	password := spec.Users[login]["password"]
	if !trusttunnelContainsAccount(spec.Accounts, login) || !trusttunnelSecretValid(password) {
		return "", errors.New("configured account and valid secret are required")
	}
	fingerprint := TLSFingerprint(spec.Material)
	if fingerprint == "" {
		return "", errors.New("valid TLS material is required")
	}
	sni := spec.Params["sni"]
	if sni == "" {
		sni = host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n  - name: %q\n    type: trusttunnel\n    server: %q\n    port: %d\n    username: %q\n    password: %q\n    sni: %q\n    fingerprint: %q\n    udp: false\n    quic: false\n", spec.Name, host, spec.Port, login, password, sni, fingerprint)
	return b.String(), nil
}
