package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/ktav-lang/golang/pure"
)

const validSample = `## netivri sample configuration
listen.bind: 127.0.0.1
listen.port: 7890
selection.mode: fastest
subscriptions: [
    {
        name: main
        url:: https://provider.example/sub/SECRET
    }
]
`

func TestLoadValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := os.WriteFile(path, []byte(validSample), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen.Port != 7890 || cfg.Listen.Bind != "127.0.0.1" {
		t.Errorf("listen = %+v", cfg.Listen)
	}
	if cfg.Selection.Mode != ModeFastest {
		t.Errorf("mode = %q, want %q", cfg.Selection.Mode, ModeFastest)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "main" {
		t.Errorf("subscriptions = %+v", cfg.Subscriptions)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.ktav"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestLoadOrDefaultFallsBack(t *testing.T) {
	cfg, err := LoadOrDefault(filepath.Join(t.TempDir(), "absent.ktav"))
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	want := Default()
	if cfg.Listen.Port != want.Listen.Port || cfg.Listen.Bind != want.Listen.Bind {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

// A missing file yields the defaults.
func TestLoadOrDefaultMissingFileYieldsDefaults(t *testing.T) {
	cfg, err := LoadOrDefault(filepath.Join(t.TempDir(), "absent.ktav"))
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if cfg.Listen.Port != Default().Listen.Port {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

// A minimal existing file inherits the defaults for every key it does
// not set.
func TestLoadMinimalFileInheritsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := os.WriteFile(path, []byte("listen.port: 8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if cfg.Listen.Bind != want.Listen.Bind || cfg.Listen.Port != 8443 {
		t.Fatalf("listen = %+v", cfg.Listen)
	}
	if cfg.Selection.Mode != want.Selection.Mode {
		t.Fatalf("mode = %q, want %q", cfg.Selection.Mode, want.Selection.Mode)
	}
	if len(cfg.Subscriptions) != 0 || cfg.Auth != (Auth{}) {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsBadSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ktav")
	// Missing separator space: a syntax error (MissingSeparatorSpace).
	if err := os.WriteFile(path, []byte("listen.port:7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	assertConfigInvalid(t, err)
}

func TestLoadRejectsLossyNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ktav")
	// Base-prefixed literal: a lossy scalar the strict parser rejects.
	if err := os.WriteFile(path, []byte("listen.port: 0x1ED2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	assertConfigInvalid(t, err)
}

// A syntax error placed on the line AFTER auth.password must not leak
// any document text: the CONFIG_INVALID message is built only from the
// binding's structured fields (Class, Reason, Line, SpecSection, Path).
func TestLoadErrorHidesDocumentText(t *testing.T) {
	// (a) a lossy numeric spelling on the line after the secret;
	// (b) a real syntax error (missing separator space, spec section 4)
	// on the line after the secret. Both must fail without leaking the
	// secret or any document text.
	fixtures := []string{
		"auth.password: s3cr3t-value\nlisten.port: 0x1ED2\n",
		"auth.password: s3cr3t-value\nlisten.port:7890\n",
	}
	for i, src := range fixtures {
		path := filepath.Join(t.TempDir(), "config.ktav")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatalf("fixture %d: expected an error", i)
		}
		cliErr, ok := err.(*cli.Error)
		if !ok {
			t.Fatalf("fixture %d: error type = %T, want *cli.Error", i, err)
		}
		if cliErr.Code != CodeConfigInvalid {
			t.Errorf("fixture %d: code = %q, want %q", i, cliErr.Code, CodeConfigInvalid)
		}
		// Key PATHS (e.g. "listen.port") are allowed in the message; the
		// secret, the lossy literal and full source lines are not.
		for _, fragment := range []string{"s3cr3t-value", "auth.password:", "listen.port: ", "0x1ED2", "7890"} {
			if strings.Contains(cliErr.Message, fragment) {
				t.Errorf("fixture %d: error leaks document text %q: %q", i, fragment, cliErr.Message)
			}
		}
	}
}

func TestConfigFromBindingErrorHidesSource(t *testing.T) {
	// A ktav error is reduced to structured fields only.
	kerr := &pure.Error{
		Msg:   "LossyScalar: body s3cr3t-value canonical 7890",
		Class: "LossyScalar", Reason: "LossyScalar", Line: 2,
		SpecSection: "8.1", Path: []string{"listen", "port"},
		Body: "s3cr3t-value", Canonical: "7890",
	}
	out := configFromBindingError(kerr)
	if out.Code != CodeConfigInvalid {
		t.Errorf("code = %q, want %q", out.Code, CodeConfigInvalid)
	}
	for _, fragment := range []string{"s3cr3t-value", "7890"} {
		if strings.Contains(out.Message, fragment) {
			t.Errorf("message leaks %q: %q", fragment, out.Message)
		}
	}
	if !strings.Contains(out.Message, "LossyScalar") || !strings.Contains(out.Message, "listen.port") {
		t.Errorf("message lost structured fields: %q", out.Message)
	}
	// Any other error gets the fixed generic message.
	other := configFromBindingError(errors.New("plain failure"))
	if other.Code != CodeConfigInvalid || strings.Contains(other.Message, "plain failure") {
		t.Errorf("plain error mishandled: %+v", other)
	}
}

func assertConfigInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	cliErr, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("error type = %T, want *cli.Error", err)
	}
	if cliErr.Code != CodeConfigInvalid {
		t.Errorf("code = %q, want %q", cliErr.Code, CodeConfigInvalid)
	}
}
