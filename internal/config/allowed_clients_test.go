package config

import (
	"bytes"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAllowedClientsAllows(t *testing.T) {
	in := netip.MustParseAddr
	cases := []struct {
		value string
		addr  netip.Addr
		want  bool
	}{
		{"*", in("127.0.0.1"), true},
		{"*", in("8.8.8.8"), true},
		{"*", in("::1"), true},
		{"loopback", in("127.0.0.1"), true},
		{"loopback", in("::1"), true},
		{"loopback", in("192.168.1.5"), false},
		{"loopback", in("10.0.0.1"), false},
		{"Loopback", in("127.0.0.1"), true},
		{" LOOPBACK ", in("::1"), true},
		{"127.0.0.1", in("127.0.0.1"), true},
		{"127.0.0.1", in("127.0.0.2"), false},
		{"::1", in("::1"), true},
		{"::1", in("::2"), false},
		{"192.168.1.20", in("192.168.1.20"), true},
		{"127.0.0.1,::1", in("192.168.1.5"), false},
		{"::ffff:127.0.0.1", in("127.0.0.1"), true},
		{"::ffff:127.0.0.1", in("::ffff:127.0.0.1"), true},
		{"127.0.0.1", in("::ffff:127.0.0.1"), true},
		{"::ffff:127.0.0.1", in("127.0.0.2"), false},
		{"192.168.1.*", in("192.168.1.5"), true},
		{"192.168.1.*", in("192.168.2.5"), false},
		{"192.168.*.*", in("192.168.9.9"), true},
		{"192.168.*.*", in("192.168.1.1"), true},
		{"192.168.*.*", in("192.168.2.1"), true},
		{"192.168.*.*", in("192.169.0.1"), false},
		{"10.*.*.*", in("10.9.9.9"), true},
		{"10.*.*.*", in("11.0.0.1"), false},
		{"*.*.*.*", in("203.0.113.9"), true},
		{"192.168.*.*", in("192.169.9.9"), false},
		{"192.168.*", in("192.168.9.9"), true},
		{"192.168.*", in("192.169.9.9"), false},
		{"10.*", in("10.255.255.255"), true},
		{"10.*", in("11.0.0.1"), false},
		{"10.0.0.0/8", in("10.1.2.3"), true},
		{"10.0.0.0/8", in("11.0.0.1"), false},
		{"fd00::/8", in("fd00::1"), true},
		{"fd00::/8", in("2001::1"), false},
		{" 127.0.0.1 , 10.0.0.0/8 ", in("127.0.0.1"), true},
		{" 127.0.0.1 , 10.0.0.0/8 ", in("10.9.9.9"), true},
		{" 127.0.0.1 , 10.0.0.0/8 ", in("8.8.8.8"), false},
		{"loopback", in("::ffff:127.0.0.1"), true},
		{"loopback,10.0.0.0/8", in("::1"), true},
		{"loopback,10.0.0.0/8", in("10.2.3.4"), true},
		{"loopback,10.0.0.0/8", in("8.8.8.8"), false},
		{"*,loopback", in("203.0.113.9"), true},
	}
	for _, tc := range cases {
		rules, err := ParseAllowedClients(tc.value)
		if err != nil {
			t.Errorf("ParseAllowedClients(%q): %v", tc.value, err)
			continue
		}
		if got := rules.Allows(tc.addr); got != tc.want {
			t.Errorf("ParseAllowedClients(%q).Allows(%v) = %v, want %v", tc.value, tc.addr, got, tc.want)
		}
	}
}

func TestParseAllowedClientsEmptyAllowsAll(t *testing.T) {
	for _, in := range []string{"", "   "} {
		rules, err := ParseAllowedClients(in)
		if err != nil {
			t.Fatalf("ParseAllowedClients(%q): %v", in, err)
		}
		if rules != nil {
			t.Errorf("ParseAllowedClients(%q) = %v, want nil", in, rules)
		}
		if !rules.Allows(netip.MustParseAddr("203.0.113.9")) {
			t.Errorf("an empty value must allow every address")
		}
		if !rules.AllowsAddr(&net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 1}) {
			t.Errorf("AllowsAddr must allow every address")
		}
	}
	if (AllowedClients)(nil).AllowsAddr(&net.UnixAddr{Name: "sock"}) {
		t.Error("a non-IP net.Addr must match nothing")
	}
}

// The contract of AllowedClients: only nil means unrestricted; a non-nil
// empty set (a restrictive list that yielded nothing) denies everyone.
// ParseAllowedClients never returns a non-nil empty set — its fail-closed
// guard errors instead — so the empty-set case is defense in depth.
// internal/auth's VerifyFrom calls AllowsAddr (multi.go), which delegates
// to Allows, so this distinction covers the auth path without an auth
// change: a nil rule list there allows every address.
func TestAllowedClientsNilVersusEmpty(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	if !(AllowedClients)(nil).Allows(loopback) {
		t.Error("a nil matcher must allow every address")
	}
	if !(AllowedClients)(nil).AllowsAddr(&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 1}) {
		t.Error("a nil matcher must allow every net.Addr")
	}
	unix := &net.UnixAddr{Name: "peer", Net: "unix"}
	if !(AllowedClients)(nil).AllowsAddr(unix) {
		t.Error("a nil matcher must allow an address that is not an IP address")
	}
	if (AllowedClients{netip.MustParsePrefix("10.0.0.0/8")}).AllowsAddr(unix) {
		t.Error("a restrictive matcher must deny an address that is not an IP address")
	}
	if (AllowedClients{}).Allows(loopback) {
		t.Error("a non-nil empty matcher must deny every address")
	}
	if (AllowedClients{}).AllowsAddr(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}) {
		t.Error("a non-nil empty matcher must deny every net.Addr")
	}
}

func TestParseAllowedClientsErrors(t *testing.T) {
	cases := []struct {
		in       string
		contains string
	}{
		{"127.0.0.1,,10.0.0.0/8", "empty rule at position 2"},
		{"a", `invalid rule "a"`},
		{"10.0.0.0/8,,loopback", "empty rule at position 2"},
		{"192.*.1.5", `invalid rule "192.*.1.5"`},
		{"19*", `invalid rule "19*"`},
		{"fd00::*", "CIDR"},
		{"fd00::*", "*"},
		{"999.1.1.1", `invalid rule "999.1.1.1"`},
		{"not-an-ip", `invalid rule "not-an-ip"`},
		{strings.Repeat("10.0.0.0/8,", 65) + "10.0.0.0/8", "more than 64 rules"},
		{strings.Repeat("a", 1025), "longer than 1024 bytes"},
	}
	for _, tc := range cases {
		rules, err := ParseAllowedClients(tc.in)
		if err == nil {
			t.Errorf("ParseAllowedClients(%q...) = %v, want an error", tc.in, rules)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("ParseAllowedClients error %q does not mention %q", err, tc.contains)
		}
	}
}

func TestValidateAllowedClients(t *testing.T) {
	cases := []struct {
		name     string
		change   func(*Config)
		contains string
	}{
		{"listen invalid", func(c *Config) { c.Listen.AllowedClients = "19*" }, "listen.allowed_clients"},
		{"listen valid", func(c *Config) { c.Listen.AllowedClients = "10.0.0.0/8" }, ""},
		{"account invalid", func(c *Config) {
			c.Accounts = []Account{{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$hashhashhash", AllowedClients: "a,,b"}}
		}, "accounts[0].allowed_clients"},
		{"account valid", func(c *Config) {
			c.Accounts = []Account{{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$hashhashhash", AllowedClients: "loopback"}}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseline()
			tc.change(cfg)
			err := cfg.Validate()
			if tc.contains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("message %q does not mention %q", err.Error(), tc.contains)
			}
		})
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config must validate with allowed_clients unset: %v", err)
	}
}

func TestAllowedClientsMarshalRoundTrip(t *testing.T) {
	doc, err := Marshal(Default())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(doc, "allowed_clients") {
		t.Errorf("default render must not contain allowed_clients: %q", doc)
	}
	cfg := validBaseline()
	cfg.Listen.AllowedClients = "10.0.0.0/8"
	cfg.Accounts = []Account{{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$hashhashhash", AllowedClients: "loopback"}}
	doc, err = Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(doc, `allowed_clients: "10.0.0.0/8"`) {
		t.Errorf("render %q does not contain the listen value", doc)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ktav")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Listen.AllowedClients != "10.0.0.0/8" {
		t.Errorf("loaded listen.allowed_clients = %q", loaded.Listen.AllowedClients)
	}
	if len(loaded.Accounts) != 1 || loaded.Accounts[0].AllowedClients != "loopback" {
		t.Errorf("loaded accounts = %+v", loaded.Accounts)
	}
	// A file without the key loads the default "*".
	if err := os.WriteFile(path, []byte("listen.port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Listen.AllowedClients != "*" {
		t.Errorf("loaded listen.allowed_clients = %q, want \"*\"", loaded.Listen.AllowedClients)
	}
}

func TestNormalizeAllowedClients(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "*"},
		{"   ", "*"},
		{" 127.0.0.1 , loopback ", "127.0.0.1,loopback"},
		{"loopback", "loopback"},
	}
	for _, tc := range cases {
		if got := NormalizeAllowedClients(tc.in); got != tc.want {
			t.Errorf("NormalizeAllowedClients(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAllowedClientPrefixes(t *testing.T) {
	prefixes := AllowedClientPrefixes("10.0.0.0/8")
	if len(prefixes) != 1 || !prefixes[0].Contains(netip.MustParseAddr("10.1.2.3")) {
		t.Errorf("AllowedClientPrefixes = %v", prefixes)
	}
	if AllowedClientPrefixes("19*") != nil {
		t.Error("an invalid value must yield nil")
	}
}

// The decoder decodes onto Default(), which carries AllowedClients "*":
// an absent key keeps "*", and any present value — empty in any Ktav
// spelling — overwrites it and must fail Validate with an error naming
// listen.allowed_clients. Saving a config whose value is the default does
// not render the key, and rendering is idempotent: Save; Load; Save yields
// the same bytes. This is not byte-identity with the input file, because
// Marshal always regenerates the document and prepends managedHeader.
func TestAllowedClientsDefaultAndEmpty(t *testing.T) {
	write := func(t *testing.T, doc string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.ktav")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	minimal := func(key string) string {
		return "listen.bind: 127.0.0.1\nlisten.port: 7890\n" + key + "selection.mode: fastest\n"
	}

	// (a) A file without the key loads "*" and validates.
	path := write(t, minimal(""))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen.AllowedClients != "*" {
		t.Errorf("loaded listen.allowed_clients = %q, want \"*\"", cfg.Listen.AllowedClients)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	// (d) Round trip: a config that never set the key (its value is the
	// default "*") renders without the key, and rendering is idempotent:
	// Save; Load; Save to a new path yields the same bytes.
	saved := filepath.Join(t.TempDir(), "saved.ktav")
	if err := Save(saved, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "allowed_clients") {
		t.Errorf("saved file must not contain allowed_clients: %q", b)
	}
	saved2 := filepath.Join(t.TempDir(), "saved2.ktav")
	cfg2, err := Load(saved)
	if err != nil {
		t.Fatalf("Load(saved): %v", err)
	}
	if err := Save(saved2, cfg2); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	b2, err := os.ReadFile(saved2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, b2) {
		t.Errorf("rendering is not idempotent:\nfirst:  %q\nsecond: %q", b, b2)
	}
	if strings.Contains(string(b2), "allowed_clients") {
		t.Errorf("re-saved file must not contain allowed_clients: %q", b2)
	}
	doc, err := Marshal(Default())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(doc, "allowed_clients") {
		t.Errorf("default render must not contain allowed_clients: %q", doc)
	}
	// (c) An explicit "*" loads and validates.
	cfg, err = Load(write(t, minimal(`listen.allowed_clients: "*"`+"\n")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen.AllowedClients != "*" {
		t.Errorf("loaded listen.allowed_clients = %q, want \"*\"", cfg.Listen.AllowedClients)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	// (e) The default config itself validates (it now carries "*").
	if err := Default().Validate(); err != nil {
		t.Errorf("Validate(Default()): %v", err)
	}
	// (b) An explicitly empty or whitespace-only value is CONFIG_INVALID,
	// in every flat spelling. Both substrings prove the failure is the
	// Validate-stage rejection, not an unrelated parse error.
	for _, v := range []string{`listen.allowed_clients: ""`, "listen.allowed_clients:", `listen.allowed_clients: "  "`, "listen.allowed_clients::"} {
		_, err := Load(write(t, minimal(v+"\n")))
		if err == nil {
			t.Errorf("Load with %q must fail", v)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "listen.allowed_clients") || !strings.Contains(msg, "must not be empty") {
			t.Errorf("Load error %q must be the Validate rejection of listen.allowed_clients (must not be empty)", msg)
		}
	}
	// The whole listen section as one nested table (no mixing of a listen
	// table with dotted listen.* keys). docs/config.md records that the
	// nested-table form of the empty value reaches Validate as "" and is
	// rejected there, so the assertion targets the Validate message.
	table := "listen: {\n    bind: 127.0.0.1\n    port: 7890\n    allowed_clients:\n}\nselection.mode: fastest\n"
	_, err = Load(write(t, table))
	if err == nil {
		t.Fatal("Load with an empty nested-table allowed_clients must fail")
	}
	if msg := err.Error(); !strings.Contains(msg, "listen.allowed_clients must not be empty") {
		t.Errorf("Load error %q must be the Validate rejection (must not be empty)", msg)
	}
}

func TestAllowedClientsCascade(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	ten := netip.MustParseAddr("10.1.2.3")
	cases := []struct {
		name        string
		global, acc string
		peer        netip.Addr
		want        bool
	}{
		{"any any", "*", "", ten, true},
		{"any loopback ok", "*", "loopback", loopback, true},
		{"any loopback deny", "*", "loopback", ten, false},
		{"global narrows", "10.0.0.0/8", "loopback", ten, false},
		{"global narrows loopback", "10.0.0.0/8", "loopback", loopback, false},
		{"both match", "10.0.0.0/8", "10.0.0.0/8", ten, true},
		{"global denies", "loopback", "10.0.0.0/8", loopback, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			global, err := ParseAllowedClients(tc.global)
			if err != nil {
				t.Fatalf("ParseAllowedClients(%q): %v", tc.global, err)
			}
			var acc AllowedClients
			if tc.acc != "" {
				if acc, err = ParseAllowedClients(tc.acc); err != nil {
					t.Fatalf("ParseAllowedClients(%q): %v", tc.acc, err)
				}
			}
			got := global.Allows(tc.peer) && (acc == nil || acc.Allows(tc.peer))
			if got != tc.want {
				t.Errorf("global %q + account %q for %v = %v, want %v", tc.global, tc.acc, tc.peer, got, tc.want)
			}
		})
	}
}
