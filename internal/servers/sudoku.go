package servers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	LC "github.com/metacubex/mihomo/listener/config"
	sudokulistener "github.com/metacubex/mihomo/listener/sudoku"
	"github.com/metacubex/mihomo/transport/sudoku"
	"go.yaml.in/yaml/v3"
)

type sudokuServer struct{}

func init()                           { Register(sudokuServer{}) }
func (sudokuServer) Name() string     { return "sudoku" }
func (sudokuServer) SingleUser() bool { return true }

func (sudokuServer) Validate(params map[string]string) error {
	for key := range params {
		switch key {
		case "table-type", "padding-min", "padding-max", "enable-pure-downlink", "disable-http-mask", "http-mask-mode":
		default:
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	if table := params["table-type"]; table != "" {
		if _, err := sudoku.NormalizeTableType(table); err != nil {
			return errors.New("invalid table-type")
		}
	}
	min, max := 10, 30
	if value, ok := params["padding-min"]; ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 100 {
			return errors.New("invalid padding-min")
		}
		min = n
	}
	if value, ok := params["padding-max"]; ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 100 {
			return errors.New("invalid padding-max")
		}
		max = n
	}
	if min > max {
		return errors.New("invalid padding-min: exceeds effective padding-max")
	}
	for _, key := range []string{"enable-pure-downlink", "disable-http-mask"} {
		if v, ok := params[key]; ok && v != "true" && v != "false" {
			return fmt.Errorf("invalid %s", key)
		}
	}
	if mode := params["http-mask-mode"]; mode != "" && mode != "legacy" && mode != "stream" && mode != "poll" && mode != "auto" {
		return errors.New("invalid http-mask-mode")
	}
	return nil
}

func (sudokuServer) NewMaterial(params map[string]string) (map[string]string, error) {
	if err := (sudokuServer{}).Validate(params); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func (sudokuServer) NewSecret(_ string, params map[string]string) (map[string]string, error) {
	if err := (sudokuServer{}).Validate(params); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("generate sudoku secret failed")
	}
	return map[string]string{"key": base64.RawURLEncoding.EncodeToString(key)}, nil
}

func (sudokuServer) Start(spec Spec, env Env) (io.Closer, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sudoku server spec: %w", err)
	}
	if spec.Protocol != "sudoku" {
		return nil, errors.New("sudoku protocol mismatch")
	}
	key := spec.Users[spec.Accounts[0]]["key"]
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid sudoku secret")
	}
	if env.Tunnel == nil || env.ListenConfig == nil {
		return nil, errors.New("sudoku start requires tunnel and listen config")
	}
	address := spec.ListenAddress()
	prepared := &preparedListenConfig{address: address}
	prepared.tcp, err = env.ListenConfig.Listen(context.Background(), "tcp", address)
	if err != nil {
		return nil, sudokuSafeStartError{cause: err}
	}
	listener, err := prepared.Listen(context.Background(), "tcp", address)
	if err != nil {
		prepared.closeUnclaimed()
		return nil, sudokuSafeStartError{cause: err}
	}
	conf := LC.SudokuServer{Enable: true, Listen: address, Key: key}
	if v := spec.Params["table-type"]; v != "" {
		conf.TableType = v
	}
	if v, ok := spec.Params["padding-min"]; ok {
		n, _ := strconv.Atoi(v)
		conf.PaddingMin = &n
	}
	if v, ok := spec.Params["padding-max"]; ok {
		n, _ := strconv.Atoi(v)
		conf.PaddingMax = &n
	}
	if v, ok := spec.Params["enable-pure-downlink"]; ok {
		b := v == "true"
		conf.EnablePureDownlink = &b
	}
	conf.DisableHTTPMask = spec.Params["disable-http-mask"] == "true"
	conf.HTTPMaskMode = spec.Params["http-mask-mode"]
	gate := &sudokuListenGate{Listener: listener, ready: make(chan struct{})}
	started, err := sudokulistener.New(conf, sudokuGateConfig{gate}, env.Tunnel, inbound.WithInName(spec.Name), inbound.WithInUser(spec.Accounts[0]))
	if err != nil {
		gate.once.Do(func() { close(gate.ready) })
		_ = listener.Close()
		return nil, sudokuSafeStartError{cause: err}
	}
	return &sudokuListenerCloser{listener: started, gate: gate}, nil
}

type sudokuListenGate struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (g *sudokuListenGate) Accept() (net.Conn, error) {
	c, err := g.Listener.Accept()
	if errors.Is(err, net.ErrClosed) {
		<-g.ready
	}
	return c, err
}

type sudokuGateConfig struct{ listener net.Listener }

func (c sudokuGateConfig) Listen(context.Context, string, string) (net.Listener, error) {
	return c.listener, nil
}
func (sudokuGateConfig) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errors.New("UDP listening is not supported")
}

type sudokuListenerCloser struct {
	listener *sudokulistener.Listener
	gate     *sudokuListenGate
	once     sync.Once
	err      error
}

func (c *sudokuListenerCloser) Close() error {
	c.once.Do(func() {
		c.err = c.listener.Close()
		c.gate.once.Do(func() { close(c.gate.ready) })
	})
	return c.err
}

type sudokuSafeStartError struct{ cause error }

func (e sudokuSafeStartError) Error() string        { return "sudoku listener start failed" }
func (e sudokuSafeStartError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e sudokuSafeStartError) String() string       { return e.Error() }
func (e sudokuSafeStartError) GoString() string     { return e.Error() }
func (sudokuServer) Link(Spec, string, string) (string, error) {
	return "", errors.New("Sudoku has no standard URI; use --format mihomo")
}
func (sudokuServer) Mihomo(spec Spec, login, host string) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", fmt.Errorf("invalid sudoku server spec: %w", err)
	}
	if spec.Protocol != "sudoku" || len(spec.Accounts) != 1 || spec.Accounts[0] != login || host == "" {
		return "", errors.New("sudoku requires its configured account and a host")
	}
	key := spec.Users[login]["key"]
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid sudoku secret")
	}
	proxy := map[string]any{"name": spec.Name, "type": "sudoku", "server": host, "port": spec.Port, "key": key, "udp": false}
	for _, name := range []string{"table-type", "padding-min", "padding-max", "enable-pure-downlink", "http-mask-mode"} {
		v, ok := spec.Params[name]
		if !ok {
			continue
		}
		switch name {
		case "padding-min", "padding-max":
			n, _ := strconv.Atoi(v)
			proxy[name] = n
		case "enable-pure-downlink":
			proxy[name] = v == "true"
		default:
			proxy[name] = v
		}
	}
	if spec.Params["disable-http-mask"] == "true" {
		proxy["http-mask"] = false
	}
	encoded, err := yaml.Marshal(map[string]any{"proxies": []any{proxy}})
	if err != nil {
		return "", errors.New("encode sudoku Mihomo configuration failed")
	}
	return string(encoded), nil
}
