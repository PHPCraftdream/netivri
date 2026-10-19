package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultHomeEnv(t *testing.T) {
	t.Setenv("NETIVRI_HOME", "/tmp/netivri-home")
	if got := DefaultHome(); got != "/tmp/netivri-home" {
		t.Fatalf("DefaultHome = %q", got)
	}
}

func TestConfigPath(t *testing.T) {
	if got, want := ConfigPath("home"), filepath.Join("home", "config.ktav"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://provider.example/sub/SECRET?token=x", "https://provider.example/****"},
		{"http://provider.example:8443/a/b", "http://provider.example:8443/****"},
		{"socks5://alice:pw@127.0.0.1:1080", "socks5://alice:****@127.0.0.1:1080"},
		{"socks5h://bob@proxy.example:1080", "socks5h://bob:****@proxy.example:1080"},
		{"https://plain.example", "https://plain.example/****"},
		{"not a url", "****"},
		{"", "****"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
