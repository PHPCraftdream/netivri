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

	"github.com/metacubex/mihomo/listener/inbound"
)

type mieru struct{}

func init()                    { Register(mieru{}) }
func (mieru) Name() string     { return "mieru" }
func (mieru) SingleUser() bool { return false }

func (mieru) Validate(params map[string]string) error {
	for key := range params {
		if key != "transport" && key != "multiplexing" {
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	if transport := strings.ToUpper(params["transport"]); transport == "UDP" {
		return errors.New("transport: UDP transport is not supported")
	}
	if transport := strings.ToUpper(params["transport"]); transport != "" && transport != "TCP" {
		return errors.New("transport: must be TCP")
	}
	if mux := params["multiplexing"]; mux != "" {
		if _, ok := mieruMultiplexingLevels[mux]; !ok {
			return errors.New("invalid multiplexing level")
		}
	}
	return nil
}

var mieruMultiplexingLevels = map[string]struct{}{
	"MULTIPLEXING_DEFAULT": {},
	"MULTIPLEXING_OFF":     {},
	"MULTIPLEXING_LOW":     {},
	"MULTIPLEXING_MIDDLE":  {},
	"MULTIPLEXING_HIGH":    {},
}

func (mieru) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (mieru{}).Validate(params); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (p mieru) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := p.Validate(params); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, errors.New("generate mieru secret failed")
	}
	return map[string]string{"password": base64.RawURLEncoding.EncodeToString(secret)}, nil
}

func (mieru) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Protocol != "mieru" {
		return nil, errors.New("mieru protocol mismatch")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("mieru start requires tunnel and listen config")
	}
	users := make(map[string]string, len(spec.Accounts))
	for _, login := range spec.Accounts {
		password := spec.Users[login]["password"]
		if password == "" {
			return nil, errors.New("mieru secret missing")
		}
		users[login] = password
	}
	listen := spec.Bind
	if listen == "" {
		listen = "0.0.0.0"
	}
	if ip := net.ParseIP(listen); ip != nil {
		listen = ip.String()
	}
	address := net.JoinHostPort(listen, strconv.Itoa(spec.Port))
	prepared := &preparedListenConfig{address: address}
	var err error
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, mieruSafeStartError{cause: err}
	}
	ownedListener := prepared.tcp
	option := &inbound.MieruOption{
		BaseOption: inbound.BaseOption{
			NameStr:            spec.Name,
			Listen:             listen,
			Port:               fmt.Sprint(spec.Port),
			ListenConfigForAPI: prepared,
		},
		Transport: "TCP",
		Users:     users,
	}
	listener, err := inbound.NewMieru(option)
	if err != nil {
		prepared.closeUnclaimed()
		return nil, errors.New("mieru listener configuration failed")
	}
	if err := listener.Listen(env.Tunnel); err != nil {
		prepared.closeUnclaimed()
		_ = listener.Close()
		if ownedListener != nil {
			_ = ownedListener.Close()
		}
		return nil, mieruSafeStartError{cause: err}
	}
	return &mieruCloser{listener: listener, socket: ownedListener}, nil
}

type mieruCloser struct {
	listener io.Closer
	socket   net.Listener
	once     sync.Once
	err      error
}

func (c *mieruCloser) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		if c.listener != nil {
			if err := c.listener.Close(); err != nil {
				c.err = errors.New("mieru listener close failed")
			}
		}
		if c.socket != nil {
			if err := c.socket.Close(); err != nil && !errors.Is(err, net.ErrClosed) && c.err == nil {
				c.err = errors.New("mieru socket close failed")
			}
		}
	})
	return c.err
}

type mieruSafeStartError struct{ cause error }

func (e mieruSafeStartError) Error() string        { return "mieru listener start failed" }
func (e mieruSafeStartError) String() string       { return e.Error() }
func (e mieruSafeStartError) GoString() string     { return e.Error() }
func (e mieruSafeStartError) Is(target error) bool { return errors.Is(e.cause, target) }

func (mieru) Link(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "mieru" {
		return "", errors.New("mieru protocol mismatch")
	}
	if !mieruContainsAccount(spec.Accounts, login) || host == "" {
		return "", errors.New("configured account and host are required")
	}
	password := spec.Users[login]["password"]
	if password == "" {
		return "", errors.New("mieru secret missing")
	}
	query := url.Values{"port": {strconv.Itoa(spec.Port)}, "protocol": {"TCP"}}
	if mux := spec.Params["multiplexing"]; mux != "" {
		query.Set("multiplexing", mux)
	}
	uriHost := host
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		uriHost = "[" + ip.String() + "]"
	}
	return (&url.URL{Scheme: "mierus", User: url.UserPassword(login, password), Host: uriHost, RawQuery: query.Encode(), Fragment: spec.Name}).String(), nil
}
func (mieru) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.Protocol != "mieru" {
		return "", errors.New("mieru protocol mismatch")
	}
	if !mieruContainsAccount(spec.Accounts, login) || host == "" {
		return "", errors.New("configured account and host are required")
	}
	password := spec.Users[login]["password"]
	if password == "" {
		return "", errors.New("mieru secret missing")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n  - name: %q\n    type: mieru\n    server: %q\n    port: %d\n    transport: TCP\n    username: %q\n    password: %q\n    udp: false\n", spec.Name, host, spec.Port, login, password)
	if mux := spec.Params["multiplexing"]; mux != "" {
		fmt.Fprintf(&b, "    multiplexing: %q\n", mux)
	}
	return b.String(), nil
}
func mieruContainsAccount(accounts []string, login string) bool {
	for _, account := range accounts {
		if account == login {
			return true
		}
	}
	return false
}
