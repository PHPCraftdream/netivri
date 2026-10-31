package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// An http:// or https:// hop is set, kept and shown like a socks5:// one:
// credentials are stored but never printed.
func TestAccountSetHopAcceptsHTTPAndHTTPS(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice"}}}
	for _, url := range []string{"http://u:secret@proxy.example:8080", "https://u:secret@proxy.example:8443", "HTTP://proxy.example:3128"} {
		var out strings.Builder
		if err := accountSetSOCKS5(cfg, []string{"alice", "--post", url}, func() error { return nil }, &out); err != nil {
			t.Fatalf("set --post %q: %v", url, err)
		}
		if cfg.Accounts[0].PostSOCKS5 != url {
			t.Errorf("stored %q, want %q", cfg.Accounts[0].PostSOCKS5, url)
		}
		if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "u:") {
			t.Errorf("output leaked the credentials of %q: %s", url, out.String())
		}
	}
	if got := redactChain("https://u:secret@proxy.example:8443"); got != "https://proxy.example:8443" {
		t.Errorf("redacted = %q", got)
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	if info.PostSOCKS5.Value != "http://proxy.example:3128" || info.PostSOCKS5.Source != "account" || info.PostSOCKS5.Auth {
		t.Errorf("post = %+v", info.PostSOCKS5)
	}
}

func TestAccountSetHopRejectsOtherSchemesWithoutLeaking(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice"}}}
	for _, url := range []string{"ftp://u:hidden@proxy.example:21", "socks4://u:hidden@proxy.example:1080", "http://u:hidden@proxy.example"} {
		err := accountSetSOCKS5(cfg, []string{"alice", "--pre", url}, func() error { return nil }, &strings.Builder{})
		if err == nil {
			t.Errorf("accepted %q", url)
		} else if strings.Contains(err.Error(), "hidden") {
			t.Errorf("error leaked the credentials of %q: %v", url, err)
		}
	}
	if cfg.Accounts[0].PreSOCKS5 != "" {
		t.Errorf("a rejected value was stored: %q", cfg.Accounts[0].PreSOCKS5)
	}
}
