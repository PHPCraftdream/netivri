package angel

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestHandshakeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs.json")
	want := testState()
	if err := WriteHandshake(path, want); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	h, err := readHandshake(path)
	if err != nil {
		t.Fatalf("readHandshake: %v", err)
	}
	if !h.OK || h.State == nil || !reflect.DeepEqual(*h.State, want) {
		t.Fatal("handshake did not contain the expected success state")
	}
	got, err := handshakeResult(h)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("handshakeResult did not return the expected state")
	}
}

func TestHandshakeErrorRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs.json")
	orig := &cli.Error{Code: "PORT_IN_USE", Message: "listen 127.0.0.1:7890: port is already in use", Exit: cli.ExitConflict}
	if err := WriteHandshakeError(path, NewHandshakeError(orig)); err != nil {
		t.Fatalf("WriteHandshakeError: %v", err)
	}
	h, err := readHandshake(path)
	if err != nil {
		t.Fatalf("readHandshake: %v", err)
	}
	if h.OK || h.Error == nil {
		t.Fatal("handshake did not contain the expected failure")
	}
	_, herr := handshakeResult(h)
	ce, ok := herr.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", herr, herr)
	}
	if ce.Code != "PORT_IN_USE" || ce.Exit != cli.ExitConflict {
		t.Errorf("code/exit = %q/%d, want PORT_IN_USE/%d", ce.Code, ce.Exit, cli.ExitConflict)
	}
	if ce.Message != orig.Message {
		t.Errorf("message = %q, want %q", ce.Message, orig.Message)
	}
}

func TestNewHandshakeErrorPlainError(t *testing.T) {
	h := NewHandshakeError(errors.New("boom"))
	if h.Code != "INTERNAL" || h.Exit != cli.ExitInternal {
		t.Errorf("code/exit = %q/%d, want INTERNAL/%d", h.Code, h.Exit, cli.ExitInternal)
	}
}

func TestReadHandshakeInvalid(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty":       "",
		"not json":    "junk",
		"ok no state": `{"ok":true}`,
		"fail no err": `{"ok":false}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := readHandshake(path); err == nil {
				t.Errorf("readHandshake(%s) = nil, want an error", name)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := readHandshake(filepath.Join(dir, "absent.json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want os.ErrNotExist", err)
		}
	})
}
