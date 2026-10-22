package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// fullConfig is a fully populated configuration, including a login
// that only survives as a raw String and a subscription with via.
func fullConfig() *Config {
	cfg := Default()
	cfg.Auth = Auth{Login: "123456", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$hashhashhash"}
	cfg.Fetch = Fetch{Proxy: "socks5://alice:pw@proxy.example:1080", UpdateInterval: "30m"}
	cfg.Subscriptions = []Subscription{
		{Name: "main", URL: "https://provider.example/sub", Via: "http://via.example:3128"},
	}
	cfg.Selection.Country = "DE"
	cfg.Selection.Mode = ModeFastest
	return cfg
}

func TestMarshalHasHeaderComment(t *testing.T) {
	requireKtavLib(t)
	doc, err := Marshal(Default())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.HasPrefix(doc, "## Managed by netivri") {
		t.Fatalf("missing header comment:\n%s", doc)
	}
}

// A login of "123456" and a subscription with via must survive the
// whole-file rewrite: Marshal round-trips, and Save+Load is deeply
// equal to the input.
func TestMarshalSaveLoadRoundTrip(t *testing.T) {
	requireKtavLib(t)
	cfg := fullConfig()
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(doc, "123456") {
		t.Fatalf("login lost:\n%s", doc)
	}
	path := filepath.Join(t.TempDir(), "config.ktav")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("round trip mismatch:\nloaded: %+v\nwant:   %+v", loaded, cfg)
	}
}

// An explicitly empty subscription list renders like an absent one and
// must survive the round-trip comparison (nil vs empty slice).
func TestMarshalEmptySubscriptions(t *testing.T) {
	requireKtavLib(t)
	cfg := Default()
	cfg.Subscriptions = []Subscription{}
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal with empty subscriptions: %v", err)
	}
	if strings.Contains(doc, "subscriptions") {
		t.Fatalf("empty list should be omitted:\n%s", doc)
	}
}

func TestMarshalNilConfig(t *testing.T) {
	requireKtavLib(t)
	if _, err := Marshal(nil); err == nil {
		t.Fatal("expected an error for a nil config")
	}
}

// Errors from the guarded wrappers must propagate UNCHANGED through
// every public entry point: with KTAV_LIB_PATH pointing at a missing
// file, Load, Marshal and Save return code KTAV_LIB_MISSING.
func TestMissingLibPropagatesUnchanged(t *testing.T) {
	t.Setenv("KTAV_LIB_PATH", filepath.Join(t.TempDir(), "no-such-lib.so"))
	path := filepath.Join(t.TempDir(), "config.ktav")
	checks := map[string]func() error{
		"Load":    func() error { _, err := Load(path); return err },
		"Marshal": func() error { _, err := Marshal(Default()); return err },
		"Save":    func() error { return Save(path, Default()) },
	}
	for name, check := range checks {
		err := check()
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		var cliErr *cli.Error
		if !errors.As(err, &cliErr) {
			t.Fatalf("%s: error type = %T, want *cli.Error", name, err)
		}
		if cliErr.Code != CodeKtavLibMissing {
			t.Errorf("%s: code = %q, want %q", name, cliErr.Code, CodeKtavLibMissing)
		}
	}
}
