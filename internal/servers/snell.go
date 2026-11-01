package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	C "github.com/metacubex/mihomo/constant"
	LC "github.com/metacubex/mihomo/listener/config"
	snelllistener "github.com/metacubex/mihomo/listener/snell"
)

type snell struct{}

func init()                    { Register(snell{}) }
func (snell) Name() string     { return "snell" }
func (snell) SingleUser() bool { return true }
func (snell) Validate(params map[string]string) error {
	for k := range params {
		if k != "version" && k != "obfs" && k != "obfs_host" {
			return fmt.Errorf("unknown parameter %q", k)
		}
	}
	if v := params["version"]; v != "" && v != "2" && v != "3" {
		return errors.New("unsupported version parameter")
	}
	if v := params["obfs"]; v != "" && v != "none" && v != "http" && v != "tls" {
		return errors.New("unsupported obfs parameter")
	}
	if params["obfs_host"] != "" {
		if params["obfs"] != "http" && params["obfs"] != "tls" {
			return errors.New("obfs_host requires http or tls obfs")
		}
		if !validTLSHost(params["obfs_host"]) {
			return errors.New("invalid obfs_host parameter")
		}
	}
	return nil
}
func (p snell) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := p.Validate(params); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (p snell) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := p.Validate(params); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, errors.New("generate Snell secret failed")
	}
	return map[string]string{"psk": base64.RawURLEncoding.EncodeToString(raw)}, nil
}
func snellPSKValid(key string) bool {
	b, err := base64.RawURLEncoding.DecodeString(key)
	return err == nil && len(b) >= 24 && base64.RawURLEncoding.EncodeToString(b) == key
}

func (p snell) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != p.Name() {
		return nil, errors.New("Snell protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("Snell start requires tunnel and listen config")
	}
	login := spec.Accounts[0]
	key := spec.Users[login]["psk"]
	if !snellPSKValid(key) {
		return nil, errors.New("invalid Snell secret")
	}
	version := 2
	if spec.Params["version"] == "3" {
		version = 3
	}
	mode := spec.Params["obfs"]
	if mode == "none" {
		mode = ""
	}
	address := spec.ListenAddress()
	tcp, err := env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, snellStartError{cause: err}
	}
	cfg := LC.SnellServer{Listen: "", Psk: key, Version: version, UDP: false, ObfsMode: mode, ObfsHost: spec.Params["obfs_host"]}
	listener, err := snelllistener.New(cfg, env.ListenConfig, env.Tunnel)
	if err != nil {
		_ = tcp.Close()
		return nil, snellStartError{cause: err}
	}
	handler, ok := listener.(*snelllistener.Listener)
	if !ok {
		_ = tcp.Close()
		return nil, errors.New("unexpected Snell listener implementation")
	}
	r := &snellRunning{listener: handler, tunnel: env.Tunnel, connListener: tcp, login: login, additions: []inbound.Addition{inbound.WithInName(spec.Name), inbound.WithInUser(login)}, done: make(chan struct{})}
	go r.accept()
	return r, nil
}

type snellRunning struct {
	listener     *snelllistener.Listener
	tunnel       C.Tunnel
	connListener net.Listener
	login        string
	additions    []inbound.Addition
	once         sync.Once
	done         chan struct{}
}

func (r *snellRunning) accept() {
	defer close(r.done)
	for {
		c, err := r.connListener.Accept()
		if err != nil {
			return
		}
		go r.listener.HandleConn(c, snellPinnedTunnel{inner: r.tunnel, user: r.login}, r.additions...)
	}
}
func (r *snellRunning) Close() error {
	var err error
	r.once.Do(func() {
		err = r.connListener.Close()
		if r.done != nil {
			<-r.done
		}
	})
	return err
}

type snellPinnedTunnel struct {
	inner C.Tunnel
	user  string
}

func (t snellPinnedTunnel) HandleTCPConn(c net.Conn, m *C.Metadata) {
	m.InUser = t.user
	t.inner.HandleTCPConn(c, m)
}
func (t snellPinnedTunnel) HandleUDPPacket(p C.UDPPacket, m *C.Metadata) { p.Drop() }
func (t snellPinnedTunnel) NatTable() C.NatTable                         { return t.inner.NatTable() }

type snellStartError struct{ cause error }

func (e snellStartError) Error() string        { return "Snell listener start failed" }
func (e snellStartError) String() string       { return e.Error() }
func (e snellStartError) GoString() string     { return e.Error() }
func (e snellStartError) Is(target error) bool { return errors.Is(e.cause, target) }

func (snell) Link(Spec, string, string) (string, error) {
	return "", errors.New("Snell has no standard share URI; use the Mihomo YAML snippet")
}
func (p snell) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != p.Name() {
		return "", errors.New("Snell protocol mismatch")
	}
	if len(spec.Accounts) != 1 || spec.Accounts[0] != login {
		return "", errors.New("Snell requires its configured account")
	}
	key := spec.Users[login]["psk"]
	if !snellPSKValid(key) || host == "" {
		return "", errors.New("host and valid Snell secret are required")
	}
	version := spec.Params["version"]
	if version == "" {
		version = "2"
	}
	mode := spec.Params["obfs"]
	hostname := spec.Params["obfs_host"]
	var obfs string
	if mode == "http" || mode == "tls" {
		if hostname == "" {
			hostname = "bing.com"
		}
		obfs = fmt.Sprintf("\n    obfs-opts:\n      mode: %q\n      host: %q", mode, hostname)
	}
	return fmt.Sprintf("proxies:\n  - name: %q\n    type: snell\n    server: %q\n    port: %d\n    psk: %q\n    version: %s\n    udp: false%s\n", spec.Name, host, spec.Port, key, version, obfs), nil
}
