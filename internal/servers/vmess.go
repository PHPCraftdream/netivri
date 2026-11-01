package servers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/metacubex/mihomo/adapter/inbound"
	LC "github.com/metacubex/mihomo/listener/config"
	vmesslistener "github.com/metacubex/mihomo/listener/sing_vmess"
)

type vmess struct{}

func (vmess) NewTrial(spec Spec) (Trial, error) { return newTrial(spec) }

func init()                    { Register(vmess{}) }
func (vmess) Name() string     { return "vmess" }
func (vmess) SingleUser() bool { return false }
func (vmess) Validate(params map[string]string) error {
	for key := range params {
		if key != "cipher" {
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	switch vmessCipher(params) {
	case "auto", "aes-128-gcm", "chacha20-poly1305", "none":
		return nil
	default:
		return fmt.Errorf("invalid cipher")
	}
}
func vmessCipher(params map[string]string) string {
	if cipher := strings.ToLower(params["cipher"]); cipher != "" {
		return cipher
	}
	return "auto"
}
func (vmess) NewMaterial(map[string]string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (p vmess) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := p.Validate(params); err != nil {
		return nil, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("generate vmess secret failed")
	}
	return map[string]string{"uuid": id.String()}, nil
}
func canonicalVMessUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != 4 {
		return "", fmt.Errorf("invalid vmess secret")
	}
	return id.String(), nil
}
func (vmess) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "vmess" {
		return nil, fmt.Errorf("vmess protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, fmt.Errorf("vmess start requires tunnel and listen config")
	}
	users := make([]LC.VmessUser, 0, len(spec.Accounts))
	seen := make(map[string]struct{}, len(spec.Accounts))
	for _, login := range spec.Accounts {
		id, err := canonicalVMessUUID(spec.Users[login]["uuid"])
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate vmess secret")
		}
		seen[id] = struct{}{}
		users = append(users, LC.VmessUser{Username: login, UUID: id, AlterID: 0})
	}
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	var err error
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, safeVMessStartError{cause: err}
	}
	listener, err := vmesslistener.New(LC.VmessServer{Enable: true, Listen: address, Users: users}, prepared, env.Tunnel, inbound.WithInName(spec.Name))
	if err != nil {
		prepared.closeUnclaimed()
		return nil, safeVMessStartError{cause: err}
	}
	return listener, nil
}

type safeVMessStartError struct{ cause error }

func (e safeVMessStartError) Error() string        { return "vmess listener start failed" }
func (e safeVMessStartError) String() string       { return e.Error() }
func (e safeVMessStartError) GoString() string     { return e.Error() }
func (e safeVMessStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (vmess) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "vmess" {
		return "", fmt.Errorf("vmess protocol mismatch")
	}
	if !containsVMessAccount(spec.Accounts, login) || host == "" {
		return "", fmt.Errorf("configured account and host are required")
	}
	id, err := canonicalVMessUUID(spec.Users[login]["uuid"])
	if err != nil {
		return "", err
	}
	payload := struct {
		Version string `json:"v"`
		Name    string `json:"ps"`
		Address string `json:"add"`
		Port    string `json:"port"`
		ID      string `json:"id"`
		AlterID string `json:"aid"`
		Cipher  string `json:"scy"`
		Network string `json:"net"`
		Type    string `json:"type"`
		Host    string `json:"host"`
		Path    string `json:"path"`
		TLS     string `json:"tls"`
	}{
		Version: "2",
		Name:    spec.Name,
		Address: host,
		Port:    strconv.Itoa(spec.Port),
		ID:      id,
		AlterID: "0",
		Cipher:  vmessCipher(spec.Params),
		Network: "tcp",
		Type:    "none",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode vmess link failed")
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(data), nil
}
func (vmess) Mihomo(spec Spec, login, host string) (string, error) {
	if _, err := (vmess{}).Link(spec, login, host); err != nil {
		return "", err
	}
	id, _ := canonicalVMessUUID(spec.Users[login]["uuid"])
	return fmt.Sprintf("proxies:\n  - name: %q\n    type: vmess\n    server: %q\n    port: %d\n    uuid: %q\n    alterId: 0\n    cipher: %q\n    network: tcp\n    tls: false\n    udp: false\n", spec.Name, host, spec.Port, id, vmessCipher(spec.Params)), nil
}
func containsVMessAccount(accounts []string, login string) bool {
	for _, account := range accounts {
		if account == login {
			return true
		}
	}
	return false
}
