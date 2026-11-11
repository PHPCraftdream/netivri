package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountShowChainOwnIsPresentWhenAbsent(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid account fixture: %v", err)
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	encoded, err := json.Marshal(map[string]any{
		"pre_socks5":  showChainInfoForAccount(info.PreSOCKS5),
		"post_socks5": showChainInfoForAccount(info.PostSOCKS5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); !strings.Contains(got, `"own":null`) {
		t.Fatal("show JSON omitted the null own field")
	}
	list, err := json.Marshal(info.PreSOCKS5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(list), `"own"`) || strings.Contains(string(list), `"own_set"`) {
		t.Fatalf("list output includes absent own: %s", list)
	}
	var listed accountChainInfo
	if err := json.Unmarshal(list, &listed); err != nil || listed.Value == nil || len(listed.Value) != 0 {
		t.Fatal("empty account list value is not a non-nil empty array")
	}
}

func TestAccountChainJSONContainsRedactedArrays(t *testing.T) {
	cfg := config.Default()
	account := config.Account{
		Login:    "alice",
		Password: config.InitPlaceholder,
		PreSOCKS5: config.HopList{
			"socks5://hidden-pre-user:hidden-pre-password@relay-pre.example:1080",
		},
		PostSOCKS5: config.HopList{
			"https://hidden-post-user:hidden-post-password@relay-post.example:8443",
		},
	}
	cfg.Accounts = []config.Account{account}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid account fixture: %v", err)
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		want  string
	}{
		{"pre_socks5", "socks5://relay-pre.example:1080"},
		{"post_socks5", "https://relay-post.example:8443"},
	} {
		chain, ok := decoded[tc.field].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", tc.field)
		}
		values, ok := chain["value"].([]any)
		if !ok || !slices.Equal(values, []any{tc.want}) {
			t.Fatalf("%s value is not the expected redacted array", tc.field)
		}
		own, ok := chain["own"].([]any)
		if !ok || !slices.Equal(own, []any{tc.want}) {
			t.Fatalf("%s own is not the expected redacted array", tc.field)
		}
		if chain["auth"] != true || chain["source"] != "account" {
			t.Fatalf("%s auth/source metadata is incorrect", tc.field)
		}
		chainInfo := info.PreSOCKS5
		if tc.field == "post_socks5" {
			chainInfo = info.PostSOCKS5
		}
		if !chainInfo.OwnSet {
			t.Fatalf("%s internal account override metadata was not populated", tc.field)
		}
	}
	if strings.Contains(string(encoded), `"own_set"`) {
		t.Fatal("account JSON exposed internal override metadata")
	}
	for _, secret := range []string{"hidden-pre-user", "hidden-pre-password", "hidden-post-user", "hidden-post-password"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("account JSON leaked hop credentials")
		}
	}
}

func TestAccountSetSOCKS5Dispatch(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	var out strings.Builder
	saved := false
	err := accountSetSOCKS5(cfg, []string{"alice", "--pre", "socks5://u:secret@relay.example:1080"}, func() error { saved = true; return nil }, &out)
	if err != nil || !saved {
		t.Fatal("setting account hops failed")
	}
	if !slices.Equal(cfg.Accounts[0].PreSOCKS5, config.HopList{"socks5://u:secret@relay.example:1080"}) {
		t.Fatal("set value not persisted")
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "u:") {
		t.Fatal("account set output disclosed credentials")
	}
	out.Reset()
	err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "inherit"}, func() error { return nil }, &out)
	if err != nil || len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatal("inherit did not clear the account-specific pre-hop list")
	}
	out.Reset()
	err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "none"}, func() error { return nil }, &out)
	if err != nil || !slices.Equal(cfg.Accounts[0].PreSOCKS5, config.HopList{"none"}) {
		t.Fatal("none did not persist as a singleton disabled hop list")
	}
	if err = accountSetSOCKS5(cfg, []string{"alice", "--pre", "https://hidden"}, func() error { return nil }, &out); err == nil || strings.Contains(err.Error(), "hidden") {
		t.Fatal("invalid proxy URL was accepted or echoed in its diagnostic")
	}
	if err = accountSetSOCKS5(cfg, []string{"alice", "--pre-socks5", "socks5://relay.example"}, func() error { return nil }, &out); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err = accountSetSOCKS5(cfg, []string{"missing", "--pre", "inherit"}, func() error { return nil }, &out); err == nil {
		t.Fatal("unknown account accepted")
	}
}

func TestRedactChainRemovesAllCredentials(t *testing.T) {
	got := redactChain("socks5://alice:secret@relay.example:1080/path?token=hidden")
	if strings.Contains(got, "alice") || strings.Contains(got, "secret") || strings.Contains(got, "hidden") {
		t.Fatal("redacted URL retained credential material")
	}
	if got != "socks5://relay.example:1080" {
		t.Fatalf("redacted URL = %q", got)
	}
}
func TestRedactChainsPreservesArrayIndependentOfCoreLimit(t *testing.T) {
	got := redactChains(config.HopList{
		"socks5://first-user:first-password@first-relay.example:1080",
		"https://second-user:second-password@second-relay.example:8443",
	})
	want := []string{"socks5://first-relay.example:1080", "https://second-relay.example:8443"}
	if !slices.Equal(got, want) {
		t.Fatalf("redacted list = %v, want %v", got, want)
	}
	for _, secret := range []string{"first-user", "first-password", "second-user", "second-password"} {
		if strings.Contains(strings.Join(got, " "), secret) {
			t.Fatal("redacted list retained credentials")
		}
	}
}

func TestAccountChainOutputSourceAndAuth(t *testing.T) {
	cfg := config.Default()
	cfg.Chain = config.Chain{
		PreSOCKS5:  config.HopList{"socks5://global:secret@g.example:1080"},
		PostSOCKS5: config.HopList{"socks5://exit.example:1080"},
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, PreSOCKS5: config.HopList{"none"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid chain fixture: %v", err)
	}
	info := infoForAccount(cfg, &cfg.Accounts[0])
	if !slices.Equal(info.PreSOCKS5.Value, []string{"none"}) || info.PreSOCKS5.Source != "none" || info.PreSOCKS5.Auth {
		t.Fatal("pre chain output has unexpected value, source, or auth metadata")
	}
	if !slices.Equal(info.PostSOCKS5.Value, []string{"socks5://exit.example"}) || info.PostSOCKS5.Source != "global" || info.PostSOCKS5.Auth {
		t.Fatal("post chain output has unexpected value, source, or auth metadata")
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
