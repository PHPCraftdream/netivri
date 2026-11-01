package servers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"go.yaml.in/yaml/v3"
)

func TestSudokuListenGateAccept(t *testing.T) {
	transientErr := errors.New("temporary accept error")
	transientGate := &sudokuListenGate{Listener: sudokuAcceptErrorListener{err: transientErr}, ready: make(chan struct{})}
	defer transientGate.once.Do(func() { close(transientGate.ready) })
	started := make(chan error, 1)
	go func() {
		_, err := transientGate.Accept()
		started <- err
	}()
	select {
	case err := <-started:
		if err != transientErr {
			t.Fatalf("transient error = %v, want original error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transient accept error was gated")
	}

	closedGate := &sudokuListenGate{Listener: sudokuAcceptErrorListener{err: net.ErrClosed}, ready: make(chan struct{})}
	defer closedGate.once.Do(func() { close(closedGate.ready) })
	entered := make(chan struct{})
	closedGate.Listener = sudokuAcceptEnteredListener{err: net.ErrClosed, entered: entered}
	finished := make(chan error, 1)
	go func() {
		_, err := closedGate.Accept()
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("closed accept did not start")
	}
	select {
	case err := <-finished:
		t.Fatalf("closed accept returned before release: %v", err)
	default:
	}
	closedGate.once.Do(func() { close(closedGate.ready) })
	select {
	case err := <-finished:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed accept error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed accept did not return after release")
	}
}

type sudokuAcceptErrorListener struct{ err error }

func (l sudokuAcceptErrorListener) Accept() (net.Conn, error) { return nil, l.err }
func (l sudokuAcceptErrorListener) Close() error              { return nil }
func (l sudokuAcceptErrorListener) Addr() net.Addr            { return sudokuTestAddr("test") }

type sudokuAcceptEnteredListener struct {
	err     error
	entered chan struct{}
}

func (l sudokuAcceptEnteredListener) Accept() (net.Conn, error) {
	close(l.entered)
	return nil, l.err
}
func (l sudokuAcceptEnteredListener) Close() error   { return nil }
func (l sudokuAcceptEnteredListener) Addr() net.Addr { return sudokuTestAddr("test") }

type sudokuTestAddr string

func (a sudokuTestAddr) Network() string { return "test" }
func (a sudokuTestAddr) String() string  { return string(a) }

func TestSudokuValidationSecretsAndMihomoImport(t *testing.T) {
	p := sudokuServer{}
	valid := []map[string]string{
		nil, {}, {"table-type": "prefer_ascii"}, {"table-type": "prefer_entropy"},
		{"table-type": "up_ascii_down_entropy"}, {"table-type": "up_entropy_down_ascii"},
		{"padding-min": "0", "padding-max": "100"}, {"enable-pure-downlink": "false", "disable-http-mask": "true"},
		{"http-mask-mode": "stream"}, {"padding-min": "10", "padding-max": "30"},
	}
	for _, params := range valid {
		if err := p.Validate(params); err != nil {
			t.Errorf("valid params %v rejected: %v", params, err)
		}
	}
	invalid := []map[string]string{
		{"unknown": "sensitive"}, {"table-type": "bad"}, {"padding-min": "-1"}, {"padding-max": "101"},
		{"padding-min": "x"}, {"padding-min": "31"}, {"padding-max": "9"}, {"padding-min": "31", "padding-max": "30"}, {"enable-pure-downlink": "yes"},
		{"disable-http-mask": "0"}, {"http-mask-mode": "bad"}, {"custom-table": "bad"},
		{"custom-tables": "bad"}, {"path-root": "/root"},
	}
	for _, params := range invalid {
		if err := p.Validate(params); err == nil {
			t.Errorf("invalid params accepted: %v", params)
		}
	}
	first, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	decodedKey, err := base64.RawURLEncoding.DecodeString(first["key"])
	if err != nil || len(decodedKey) != 32 || len(first["key"]) != 43 {
		t.Fatal("generated Sudoku key is not 32-byte raw base64url")
	}
	if first["key"] == second["key"] {
		t.Fatal("generated keys are not unique")
	}
	if _, err := p.NewSecret("alice", map[string]string{"table-type": "bad"}); err == nil {
		t.Fatal("NewSecret accepted invalid parameters")
	}
	if material, err := p.NewMaterial(map[string]string{"table-type": "bad"}); err == nil || material != nil {
		t.Fatal("NewMaterial accepted invalid parameters")
	}
	if material, err := p.NewMaterial(nil); err != nil || len(material) != 0 {
		t.Fatalf("material=%v err=%v", material, err)
	}
	params := map[string]string{"table-type": "prefer_ascii", "padding-min": "10", "padding-max": "30", "enable-pure-downlink": "false", "disable-http-mask": "true", "http-mask-mode": "stream"}
	spec := Spec{Name: "edge", Protocol: "sudoku", Bind: "127.0.0.1", Port: 8388, Accounts: []string{"alice"}, Params: params, Users: UserSecrets{"alice": first}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	snippet, err := p.Mihomo(spec, "alice", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snippet, `padding-min: "`) || strings.Contains(snippet, `enable-pure-downlink: "`) || strings.Contains(snippet, `http-mask: "`) {
		t.Fatal("Mihomo YAML contains a quoted typed option")
	}
	imported, err := convert.ConvertsV2Ray([]byte("sudoku://example.org:8388"))
	if err == nil || len(imported) != 0 {
		t.Fatal("Sudoku URI unexpectedly imported")
	}
	var decodedYAML map[string][]map[string]any
	if err := yaml.Unmarshal([]byte(snippet), &decodedYAML); err != nil {
		t.Fatal("could not decode emitted Mihomo YAML")
	}
	if len(decodedYAML["proxies"]) != 1 {
		t.Fatal("snippet must contain exactly one proxy")
	}
	proxy := decodedYAML["proxies"][0]
	for field, want := range map[string]any{"name": "edge", "type": "sudoku", "server": "example.org", "port": 8388, "key": first["key"], "table-type": "prefer_ascii", "padding-min": 10, "padding-max": 30, "enable-pure-downlink": false, "http-mask": false, "http-mask-mode": "stream", "udp": false} {
		if got, ok := proxy[field]; !ok || got != want {
			t.Errorf("proxy field %s has wrong value or type", field)
		}
	}
	parsed, err := adapter.ParseProxy(proxy)
	if err != nil || parsed == nil {
		t.Fatal("Mihomo adapter rejected the emitted proxy options")
	}
	parsed.Close()
	if _, err := p.Link(spec, "alice", "example.org"); err == nil || !strings.Contains(err.Error(), "--format mihomo") {
		t.Fatal("URI error did not direct to Mihomo format")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formatted := fmt.Sprintf(format, spec)
		if strings.Contains(formatted, first["key"]) {
			t.Fatalf("%s leaked Sudoku key", format)
		}
		safe := fmt.Sprintf(format, sudokuSafeStartError{cause: fmt.Errorf("cause contains %s", first["key"])})
		if strings.Contains(safe, first["key"]) {
			t.Fatalf("%s leaked listener cause", format)
		}
	}
	if !errors.Is(sudokuSafeStartError{cause: net.ErrClosed}, net.ErrClosed) {
		t.Fatal("sanitized listener error lost its cause for errors.Is")
	}
	badKeySpec := spec.Clone()
	badKeySpec.Users["alice"]["key"] = "invalid-key-secret"
	if _, err := p.Mihomo(badKeySpec, "alice", "example.org"); err == nil || strings.Contains(err.Error(), "invalid-key-secret") {
		t.Fatal("invalid key error was missing or exposed the key")
	}
	startErrValue, err := (sudokuServer{}).Start(badKeySpec, Env{})
	if err == nil || startErrValue != nil {
		t.Fatal("Start accepted invalid stored key")
	}
	startErr := fmt.Sprintf("%v %+v %#v", err, err, err)
	if !strings.Contains(startErr, "invalid sudoku secret") || strings.Contains(startErr, "invalid-key-secret") {
		t.Fatal("invalid stored key error did not identify the invalid secret safely")
	}
}
