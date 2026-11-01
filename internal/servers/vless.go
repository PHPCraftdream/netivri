package servers

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/metacubex/mihomo/adapter/inbound"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/listener/reality"
	"github.com/metacubex/mihomo/listener/sing_vless"
	"go.yaml.in/yaml/v3"
)

const defaultRealityDest = "www.microsoft.com:443"
const defaultRealityFingerprint = "chrome"
const defaultRealityFlow = "xtls-rprx-vision"

type vlessReality struct{}

func init()                           { Register(vlessReality{}) }
func (vlessReality) Name() string     { return "vless-reality" }
func (vlessReality) SingleUser() bool { return false }

func realityDest(params map[string]string) string {
	if params["dest"] == "" {
		return defaultRealityDest
	}
	return params["dest"]
}
func realitySNI(params map[string]string) string {
	if params["sni"] != "" {
		return params["sni"]
	}
	host, _, err := net.SplitHostPort(realityDest(params))
	if err == nil {
		return host
	}
	return realityDest(params)
}
func validHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, " /\\?#@[]") {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func validDNSName(host string) bool {
	if !validHost(host) || net.ParseIP(host) != nil {
		return false
	}
	return true
}

func validHostPort(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || !validHost(host) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}
func (vlessReality) Validate(params map[string]string) error {
	for k := range params {
		if k != "dest" && k != "sni" && k != "flow" && k != "fingerprint" {
			return fmt.Errorf("unknown parameter %q", k)
		}
	}
	if params["dest"] != "" && !validHostPort(params["dest"]) {
		return errors.New("invalid dest parameter")
	}
	if params["sni"] != "" && !validDNSName(params["sni"]) {
		return errors.New("invalid sni parameter")
	}
	if params["sni"] == "" && net.ParseIP(strings.Trim(realitySNI(params), "[]")) != nil {
		return errors.New("Reality SNI is required for an IP destination")
	}
	if params["flow"] != "" && params["flow"] != defaultRealityFlow {
		return errors.New("invalid flow parameter")
	}
	if fp := params["fingerprint"]; fp != "" && !supportedFingerprint(fp) {
		return errors.New("invalid fingerprint parameter key")
	}
	return nil
}
func supportedFingerprint(fp string) bool {
	switch fp {
	case "chrome", "firefox", "safari", "ios", "edge", "random", "randomized":
		return true
	}
	return false
}
func (vlessReality) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (vlessReality{}).Validate(params); err != nil {
		return nil, err
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.New("generate VLESS Reality material failed")
	}
	ids := make([]string, 3)
	for i := range ids {
		b := make([]byte, 4)
		if _, err = rand.Read(b); err != nil {
			return nil, errors.New("generate VLESS Reality material failed")
		}
		ids[i] = hex.EncodeToString(b)
	}
	return map[string]string{
		"private_key": base64.RawURLEncoding.EncodeToString(private.Bytes()),
		"public_key":  base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		"short_ids":   strings.Join(ids, ","),
	}, nil
}
func (vlessReality) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (vlessReality{}).Validate(params); err != nil {
		return nil, err
	}
	u, err := uuid.NewRandom()
	if err != nil {
		return nil, errors.New("generate VLESS account secret failed")
	}
	return map[string]string{"uuid": u.String()}, nil
}
func parseUUID(value string) bool {
	u, err := uuid.Parse(value)
	return err == nil && u.String() == strings.ToLower(value)
}
func validMaterial(material SecretMap) bool {
	if len(material) != 3 {
		return false
	}
	priv, err := base64.RawURLEncoding.DecodeString(material["private_key"])
	if err != nil || len(priv) != 32 {
		return false
	}
	pub, err := base64.RawURLEncoding.DecodeString(material["public_key"])
	if err != nil || len(pub) != 32 {
		return false
	}
	key, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil || !bytes.Equal(key.PublicKey().Bytes(), pub) {
		return false
	}
	ids := strings.Split(material["short_ids"], ",")
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		raw, err := hex.DecodeString(id)
		if err != nil || len(raw) == 0 || len(raw) > 8 || len(id) < 2 || len(id) > 16 || len(id)%2 != 0 {
			return false
		}
	}
	return true
}

func (vlessReality) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "vless-reality" {
		return nil, errors.New("VLESS Reality protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("VLESS Reality start requires tunnel and listen config")
	}
	if !validMaterial(spec.Material) {
		return nil, errors.New("invalid VLESS Reality material")
	}
	users := make([]LC.VlessUser, 0, len(spec.Accounts))
	seen := make(map[string]bool)
	for _, login := range spec.Accounts {
		id := spec.Users[login]["uuid"]
		if !parseUUID(id) || seen[strings.ToLower(id)] {
			return nil, errors.New("invalid or duplicate VLESS account UUID")
		}
		seen[strings.ToLower(id)] = true
		users = append(users, LC.VlessUser{Username: login, UUID: id, Flow: defaultRealityFlow})
	}
	ids := strings.Split(spec.Material["short_ids"], ",")
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	listener, err := env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, safeVlessStartError{err}
	}
	prepared.tcp = listener
	config := LC.VlessServer{
		Enable:     true,
		Listen:     address,
		Users:      users,
		Decryption: "none",
		RealityConfig: reality.Config{
			Dest:        realityDest(spec.Params),
			PrivateKey:  spec.Material["private_key"],
			ShortID:     ids,
			ServerNames: []string{realitySNI(spec.Params)},
		},
	}
	started, err := sing_vless.New(config, prepared, env.Tunnel, inbound.WithInName(spec.Name))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, safeVlessStartError{err}
	}
	return started, nil
}

type safeVlessStartError struct{ cause error }

func (e safeVlessStartError) Error() string        { return "VLESS Reality listener start failed" }
func (e safeVlessStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e safeVlessStartError) String() string       { return e.Error() }
func (e safeVlessStartError) GoString() string     { return e.Error() }

func (vlessReality) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "vless-reality" || !containsAccount(spec.Accounts, login) {
		return "", errors.New("VLESS Reality account is not configured")
	}
	id := spec.Users[login]["uuid"]
	if !parseUUID(id) || !validMaterial(spec.Material) || !validHost(host) {
		return "", errors.New("invalid VLESS Reality link inputs")
	}
	ids := strings.Split(spec.Material["short_ids"], ",")
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("flow", defaultRealityFlow)
	q.Set("security", "reality")
	q.Set("sni", realitySNI(spec.Params))
	q.Set("fp", fingerprint(spec.Params))
	q.Set("pbk", spec.Material["public_key"])
	q.Set("sid", ids[0])
	q.Set("type", "tcp")
	u := url.URL{Scheme: "vless", User: url.User(id), Host: net.JoinHostPort(host, strconv.Itoa(spec.Port)), RawQuery: q.Encode(), Fragment: spec.Name}
	return u.String(), nil
}

func containsAccount(accounts []string, login string) bool {
	for _, a := range accounts {
		if a == login {
			return true
		}
	}
	return false
}

func fingerprint(params map[string]string) string {
	if params["fingerprint"] != "" {
		return params["fingerprint"]
	}
	return defaultRealityFingerprint
}

func (vlessReality) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "vless-reality" || !containsAccount(spec.Accounts, login) {
		return "", errors.New("VLESS Reality account is not configured")
	}
	id := spec.Users[login]["uuid"]
	if !parseUUID(id) || !validMaterial(spec.Material) || !validHost(host) {
		return "", errors.New("invalid VLESS Reality link inputs")
	}
	ids := strings.Split(spec.Material["short_ids"], ",")
	proxy := map[string]any{
		"name": spec.Name, "type": "vless", "server": host, "port": spec.Port,
		"uuid": id, "flow": defaultRealityFlow, "tls": true, "udp": false,
		"servername": realitySNI(spec.Params), "client-fingerprint": fingerprint(spec.Params),
		"network": "tcp", "encryption": "none",
		"reality-opts": map[string]string{
			"public-key": spec.Material["public_key"],
			"short-id":   ids[0],
		},
	}
	data, err := yaml.Marshal(map[string]any{"proxies": []any{proxy}})
	if err != nil {
		return "", errors.New("encode VLESS Reality configuration failed")
	}
	return string(data), nil
}
