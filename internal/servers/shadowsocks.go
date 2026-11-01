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
	LC "github.com/metacubex/mihomo/listener/config"
	sslistener "github.com/metacubex/mihomo/listener/sing_shadowsocks"
)

const defaultCipher = "2022-blake3-aes-128-gcm"

type shadowsocks struct{}

func init()                          { Register(shadowsocks{}) }
func (shadowsocks) Name() string     { return "shadowsocks" }
func (shadowsocks) SingleUser() bool { return true }
func cipherName(p map[string]string) string {
	if c := p["cipher"]; c != "" {
		return strings.ToLower(c)
	}
	return defaultCipher
}
func secretSize(cipher string) (int, error) {
	switch strings.ToLower(cipher) {
	case "2022-blake3-aes-128-gcm", "aes-128-gcm":
		return 16, nil
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "aes-256-gcm", "chacha20-ietf-poly1305":
		return 32, nil
	default:
		return 0, fmt.Errorf("unsupported cipher")
	}
}
func (shadowsocks) Validate(params map[string]string) error {
	for k := range params {
		if k != "cipher" && k != "udp" {
			return fmt.Errorf("unknown parameter %q", k)
		}
	}
	if _, err := secretSize(cipherName(params)); err != nil {
		return fmt.Errorf("invalid cipher")
	}
	if v := params["udp"]; v != "" && v != "false" {
		if v == "true" {
			return fmt.Errorf("udp: UDP through account routes is not supported yet")
		}
		return fmt.Errorf("invalid udp parameter")
	}
	return nil
}
func mustSecretSize(cipher string) int { size, _ := secretSize(cipher); return size }

func (shadowsocks) NewMaterial(map[string]string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (shadowsocks) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (shadowsocks{}).Validate(params); err != nil {
		return nil, err
	}
	size, _ := secretSize(cipherName(params))
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate shadowsocks secret failed")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if strings.HasPrefix(cipherName(params), "2022-") {
		encoded = base64.StdEncoding.EncodeToString(raw)
	}
	return map[string]string{"password": encoded}, nil
}
func (shadowsocks) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "shadowsocks" {
		return nil, fmt.Errorf("shadowsocks protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, fmt.Errorf("shadowsocks start requires tunnel and listen config")
	}
	login := spec.Accounts[0]
	password := spec.Users[login]["password"]
	if password == "" {
		return nil, fmt.Errorf("shadowsocks secret missing")
	}
	cipher := cipherName(spec.Params)
	if strings.HasPrefix(cipher, "2022-") {
		key, err := base64.StdEncoding.DecodeString(password)
		if err != nil || len(key) != mustSecretSize(cipher) {
			return nil, fmt.Errorf("invalid shadowsocks secret")
		}
	}
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	var err error
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, safeStartError{cause: err}
	}
	config := LC.ShadowsocksServer{Enable: true, Listen: address, Cipher: cipher, Password: password, Udp: false}
	listener, err := sslistener.New(config, prepared, env.Tunnel, inbound.WithInName(spec.Name), inbound.WithInUser(login))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, safeStartError{cause: err}
	}
	return listener, nil
}

type safeStartError struct{ cause error }

func (e safeStartError) Error() string        { return "shadowsocks listener start failed" }
func (e safeStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e safeStartError) String() string       { return e.Error() }
func (e safeStartError) GoString() string     { return e.Error() }
func (shadowsocks) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "shadowsocks" {
		return "", fmt.Errorf("shadowsocks protocol mismatch")
	}
	if len(spec.Accounts) != 1 || spec.Accounts[0] != login {
		return "", fmt.Errorf("shadowsocks requires its configured account")
	}
	password := spec.Users[login]["password"]
	if password == "" || host == "" {
		return "", fmt.Errorf("host and secret are required")
	}
	userinfo := cipherName(spec.Params) + ":" + url.QueryEscape(password)
	if strings.HasPrefix(cipherName(spec.Params), "2022-") {
		return "ss://" + userinfo + "@" + net.JoinHostPort(host, strconv.Itoa(spec.Port)) + "#" + url.PathEscape(spec.Name), nil
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(cipherName(spec.Params) + ":" + password))
	return "ss://" + payload + "@" + net.JoinHostPort(host, strconv.Itoa(spec.Port)) + "#" + url.PathEscape(spec.Name), nil
}
func (shadowsocks) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "shadowsocks" {
		return "", fmt.Errorf("shadowsocks protocol mismatch")
	}
	if len(spec.Accounts) != 1 || spec.Accounts[0] != login {
		return "", fmt.Errorf("shadowsocks requires its configured account")
	}
	password := spec.Users[login]["password"]
	if password == "" || host == "" {
		return "", fmt.Errorf("host and secret are required")
	}
	return fmt.Sprintf("proxies:\n  - name: %q\n    type: ss\n    server: %q\n    port: %d\n    cipher: %q\n    password: %q\n    udp: false\n", spec.Name, host, spec.Port, cipherName(spec.Params), password), nil
}
