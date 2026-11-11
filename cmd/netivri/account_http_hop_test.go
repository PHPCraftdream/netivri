package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// An http:// or https:// hop is set, kept and shown like a socks5:// one:
// credentials are stored but never printed.
func TestAccountSetHopAcceptsHTTPAndHTTPS(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice"}}}
	for i, url := range []string{"http://u:secret@proxy.example:8080", "https://u:secret@proxy.example:8443", "HTTP://proxy.example:3128"} {
		var out strings.Builder
		if err := accountSetSOCKS5(cfg, []string{"alice", "--post", url}, func() error { return nil }, &out); err != nil {
			t.Fatalf("case %d: set failed", i)
		}
		if !slices.Equal(cfg.Accounts[0].PostSOCKS5, []string{url}) {
			t.Errorf("case %d: stored value did not match expected singleton", i)
		}
		if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "u:") {
			t.Errorf("case %d: output leaked credentials", i)
		}
	}
	if got := redactChain("https://u:secret@proxy.example:8443"); got != "https://proxy.example:8443" {
		t.Error("credential redaction did not match expected output")
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	if !slices.Equal(info.PostSOCKS5.Value, []string{"http://proxy.example:3128"}) || info.PostSOCKS5.Source != "account" || info.PostSOCKS5.Auth {
		t.Error("post chain info did not match expected singleton")
	}
}

func TestAccountSetHopRejectsOtherSchemesWithoutLeaking(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice"}}}
	for i, url := range []string{"ftp://u:hidden@proxy.example:21", "socks4://u:hidden@proxy.example:1080", "http://u:hidden@proxy.example"} {
		err := accountSetSOCKS5(cfg, []string{"alice", "--pre", url}, func() error { return nil }, &strings.Builder{})
		if err == nil {
			t.Errorf("case %d: invalid scheme was accepted", i)
		} else if strings.Contains(err.Error(), "hidden") {
			t.Errorf("case %d: error leaked credentials", i)
		}
	}
	if len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Error("a rejected value was stored")
	}
}
