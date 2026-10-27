package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountShowChainOwnIsPresentWhenAbsent(t *testing.T) {
	info := infoForAccount(&config.Config{}, &config.Account{Login: "alice"})
	encoded, err := json.Marshal(map[string]any{
		"pre_socks5":  showChainInfoForAccount(info.PreSOCKS5),
		"post_socks5": showChainInfoForAccount(info.PostSOCKS5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); !strings.Contains(got, `"own":null`) {
		t.Fatalf("show output omits own null: %s", got)
	}
	list, err := json.Marshal(info.PreSOCKS5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(list), `"own"`) {
		t.Fatalf("list output includes absent own: %s", list)
	}
}

func TestAccountSetSOCKS5Dispatch(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice"}}}
	var out strings.Builder
	saved := false
	err := accountSetSOCKS5(cfg, []string{"alice", "--pre", "socks5://u:secret@relay.example:1080"}, func() error { saved = true; return nil }, &out)
	if err != nil || !saved {
		t.Fatalf("set failed: saved=%v err=%v", saved, err)
	}
	if cfg.Accounts[0].PreSOCKS5 != "socks5://u:secret@relay.example:1080" {
		t.Fatal("set value not persisted")
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "u:") {
		t.Fatalf("output leaked: %s", out.String())
	}
	out.Reset()
	err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "inherit"}, func() error { return nil }, &out)
	if err != nil || cfg.Accounts[0].PreSOCKS5 != "" {
		t.Fatalf("inherit failed: %v", err)
	}
	out.Reset()
	err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "none"}, func() error { return nil }, &out)
	if err != nil || cfg.Accounts[0].PreSOCKS5 != "none" {
		t.Fatalf("none failed: %v", err)
	}
	if err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "https://hidden"}, func() error { return nil }, &out); err == nil || strings.Contains(err.Error(), "hidden") {
		t.Fatalf("invalid validation: %v", err)
	}
	if err = accountSetSOCKS5(cfg, []string{"alice", "--pre-socks5", "socks5://relay.example"}, func() error { return nil }, &out); err == nil {
		t.Fatal("accepted invalid flag")
	}
	if err = accountSetSOCKS5(cfg, []string{"missing", "--pre", "inherit"}, func() error { return nil }, &out); err == nil {
		t.Fatal("accepted unknown account")
	}
}

func TestRedactChainRemovesAllCredentials(t *testing.T) {
	got := redactChain("socks5://alice:secret@relay.example:1080/path?token=hidden")
	if strings.Contains(got, "alice") || strings.Contains(got, "secret") || strings.Contains(got, "hidden") {
		t.Fatalf("redaction leaked credentials: %q", got)
	}
	if got != "socks5://relay.example:1080" {
		t.Fatalf("redacted URL = %q", got)
	}
}
func TestAccountChainOutputSourceAndAuth(t *testing.T) {
	cfg := &config.Config{Chain: config.Chain{PreSOCKS5: "socks5://global:secret@g.example:1080", PostSOCKS5: "socks5://exit.example"}}
	a := &config.Account{Login: "alice", Password: config.InitPlaceholder, PreSOCKS5: "none"}
	info := infoForAccount(cfg, a)
	if info.PreSOCKS5.Value != "none" || info.PreSOCKS5.Source != "none" || info.PreSOCKS5.Auth {
		t.Fatalf("pre output = %+v", info)
	}
	if info.PostSOCKS5.Value != "socks5://exit.example" || info.PostSOCKS5.Source != "global" || info.PostSOCKS5.Auth {
		t.Fatalf("post output = %+v", info)
	}
}
func TestValidateCLIChainUsesDefaultConfig(t *testing.T) {
	if err := validateCLIChain("socks5://relay.example:1080", "--pre"); err != nil {
		t.Fatal(err)
	}
	err := validateCLIChain("https://relay.example", "--post")
	if err == nil {
		t.Fatal("expected invalid SOCKS5 URL")
	}
}
