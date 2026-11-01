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

	"github.com/metacubex/mihomo/adapter/inbound"
	anytlslistener "github.com/metacubex/mihomo/listener/anytls"
	LC "github.com/metacubex/mihomo/listener/config"
)

const anyTLSMinPasswordBytes = 24

type anyTLS struct{}

func init() { Register(anyTLS{}) }

func (anyTLS) Name() string     { return "anytls" }
func (anyTLS) SingleUser() bool { return false }

func (anyTLS) Validate(params map[string]string) error {
	for key := range params {
		switch key {
		case "sni", "padding_scheme", "public_host", "cert_file", "key_file":
		default:
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	if err := ValidateTLSParams(params); err != nil {
		return err
	}
	if publicHost, ok := params["public_host"]; ok && publicHost != "" && !validTLSHost(publicHost) {
		return errors.New("invalid public_host")
	}
	if value, ok := params["padding_scheme"]; ok && value != "" && !validAnyTLSPadding(value) {
		return errors.New("invalid padding_scheme")
	}
	return nil
}

func validAnyTLSPadding(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	seenStop := false
	seenKeys := make(map[string]struct{})
	for _, line := range strings.Split(value, "\n") {
		if line == "" || strings.ContainsAny(line, "\r\x00\t") {
			return false
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return false
		}
		key, contents := parts[0], parts[1]
		if _, exists := seenKeys[key]; exists {
			return false
		}
		seenKeys[key] = struct{}{}
		if key == "stop" {
			if seenStop {
				return false
			}
			seenStop = true
			stop, err := strconv.ParseUint(contents, 10, 32)
			if err != nil || stop > 1000 || strings.TrimLeft(contents, "0123456789") != "" {
				return false
			}
			continue
		}
		if len(key) > 3 || strings.TrimLeft(key, "0123456789") != "" {
			return false
		}
		packet, err := strconv.ParseUint(key, 10, 32)
		if err != nil || packet > 999 || strconv.FormatUint(packet, 10) != key {
			return false
		}
		for index, item := range strings.Split(contents, ",") {
			if item == "c" {
				if packet == 0 && index == 0 {
					return false
				}
				continue
			}
			rangeParts := strings.Split(item, "-")
			if len(rangeParts) != 2 {
				return false
			}
			for _, part := range rangeParts {
				n, err := strconv.ParseUint(part, 10, 16)
				if err != nil || n == 0 || strings.TrimLeft(part, "0123456789") != "" {
					return false
				}
			}
		}
	}
	return seenStop
}

func (anyTLS) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (anyTLS{}).Validate(params); err != nil {
		return nil, err
	}
	host := params["public_host"]
	if host == "" && validTLSHost(params["sni"]) {
		host = params["sni"]
	}
	return NewTLSMaterial(params, host)
}

func (anyTLS) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (anyTLS{}).Validate(params); err != nil {
		return nil, err
	}
	raw := make([]byte, anyTLSMinPasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, errors.New("generate AnyTLS secret failed")
	}
	return map[string]string{"password": base64.RawURLEncoding.EncodeToString(raw)}, nil
}

func (anyTLS) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "anytls" {
		return nil, errors.New("AnyTLS protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("AnyTLS start requires tunnel and listen config")
	}
	users := make(map[string]string, len(spec.Accounts))
	passwords := make(map[string]struct{}, len(spec.Accounts))
	for _, login := range spec.Accounts {
		password := spec.Users[login]["password"]
		if !validAnyTLSSecret(password) {
			return nil, errors.New("invalid AnyTLS secret")
		}
		if _, ok := passwords[password]; ok {
			return nil, errors.New("duplicate AnyTLS secret")
		}
		passwords[password] = struct{}{}
		users[login] = password
	}
	certificate, privateKey := TLSFiles(spec.Material)
	if certificate == "" || privateKey == "" {
		return nil, errors.New("AnyTLS TLS certificate and private key are required")
	}
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	var err error
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, safeAnyTLSStartError{cause: err}
	}
	config := LC.AnyTLSServer{
		Enable: true, Listen: address, Users: users,
		Certificate: certificate, PrivateKey: privateKey,
		PaddingScheme: spec.Params["padding_scheme"],
	}
	listener, err := anytlslistener.New(config, prepared, env.Tunnel, inbound.WithInName(spec.Name))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, safeAnyTLSStartError{cause: err}
	}
	return listener, nil
}

type safeAnyTLSStartError struct{ cause error }

func (e safeAnyTLSStartError) Error() string        { return "AnyTLS listener start failed" }
func (e safeAnyTLSStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e safeAnyTLSStartError) String() string       { return e.Error() }
func (e safeAnyTLSStartError) GoString() string     { return e.Error() }

func validAnyTLSSecret(password string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(password)
	return err == nil && len(decoded) >= anyTLSMinPasswordBytes
}

func (anyTLS) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "anytls" {
		return "", errors.New("AnyTLS protocol mismatch")
	}
	password := spec.Users[login]["password"]
	if !containsAccount(spec.Accounts, login) || !validAnyTLSSecret(password) || host == "" {
		return "", errors.New("host, configured account and valid secret are required")
	}
	fingerprint := TLSFingerprint(spec.Material)
	if fingerprint == "" {
		return "", errors.New("valid TLS material is required")
	}
	query := url.Values{}
	if sni := spec.Params["sni"]; sni != "" {
		query.Set("sni", sni)
	}
	query.Set("insecure", "0")
	query.Set("hpkp", fingerprint)
	return "anytls://" + url.User(password).String() + "@" + net.JoinHostPort(host, strconv.Itoa(spec.Port)) + "?" + query.Encode() + "#" + url.PathEscape(spec.Name), nil
}

func containsAccount(accounts []string, login string) bool {
	for _, account := range accounts {
		if account == login {
			return true
		}
	}
	return false
}

func (anyTLS) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "anytls" {
		return "", errors.New("AnyTLS protocol mismatch")
	}
	password := spec.Users[login]["password"]
	if !containsAccount(spec.Accounts, login) || !validAnyTLSSecret(password) || host == "" {
		return "", errors.New("host, configured account and valid secret are required")
	}
	fingerprint := TLSFingerprint(spec.Material)
	if fingerprint == "" {
		return "", errors.New("valid TLS material is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n  - name: %q\n    type: anytls\n    server: %q\n    port: %d\n    password: %q\n    sni: %q\n    fingerprint: %q\n    client-fingerprint: chrome\n    idle-session-check-interval: 30\n    skip-cert-verify: false\n    udp: false\n", spec.Name, host, spec.Port, password, spec.Params["sni"], fingerprint)
	return b.String(), nil
}
