package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestChainValidation(t *testing.T) {
	valid := []string{"", "none", "NONE", "SoCkS5://proxy.example:1080", "socks5://user:p%40ss@[2001:db8::1]:1080",
		"http://proxy.example:8080", "HTTP://user:p%40ss@proxy.example:3128", "https://proxy.example:8443", "http://[::1]:3128"}
	for _, value := range valid {
		hops := HopList(nil)
		if value != "" {
			hops = HopList{value}
		}
		c := Default()
		c.Chain = Chain{PreSOCKS5: hops, PostSOCKS5: hops}
		c.Accounts = []Account{{Login: "alice", Password: "init", PreSOCKS5: hops, PostSOCKS5: hops}}
		if err := c.Validate(); err != nil {
			t.Errorf("accepted valid hop list: %v", err)
		}
	}
	for _, value := range []string{"http://proxy.example", "https://proxy.example", "http://proxy.example:8080/path", "http://proxy.example:0", "https://proxy.example:70000", "ftp://proxy.example:21", "socks4://proxy.example:1080", "socks5h://proxy.example:1080", "socks5://", "socks5://proxy.example", "socks5://proxy.example:1080/path", "socks5://proxy.example:1080?token=x", "socks5://proxy.example:1080#fragment", "socks5://proxy.example:bad", "socks5://user:%ZZ@proxy.example:1080", " socks5://proxy.example"} {
		c := Default()
		c.Chain.PreSOCKS5 = HopList{value}
		if err := c.Validate(); err == nil {
			t.Error("accepted invalid hop")
		} else if !strings.Contains(err.Error(), "chain.pre_socks5") {
			t.Errorf("error lacks chain.pre_socks5 field prefix: %v", err)
		} else if _, leak, _ := strings.Cut(value, "://"); leak != "" && strings.Contains(err.Error(), leak) {
			t.Error("error leaked hop credentials")
		}
	}
}

func TestConfigValidateHopLimits(t *testing.T) {
	if MaxHopsPerSide != 16 {
		t.Fatalf("MaxHopsPerSide = %d, want 16", MaxHopsPerSide)
	}
	valid := make(HopList, MaxHopsPerSide)
	for i := range valid {
		valid[i] = fmt.Sprintf("socks5://hop-%d.example:1080", i)
	}
	if err := ValidateHopList("pre_socks5", valid); err != nil {
		t.Fatalf("accepted maximum-length hop pool: %v", err)
	}
	tooMany := append(append(HopList(nil), valid...), "socks5://hop-extra.example:1080")
	if err := ValidateHopList("pre_socks5", tooMany); err == nil || !strings.Contains(err.Error(), "at most 16 hops per side") {
		t.Fatalf("17-hop list error = %v, want maximum 16 diagnostic", err)
	}
	cases := []struct {
		name  string
		field string
		set   func(*Config, HopList)
	}{
		{"chain pre", "chain.pre_socks5", func(c *Config, hops HopList) { c.Chain.PreSOCKS5 = hops }},
		{"chain post", "chain.post_socks5", func(c *Config, hops HopList) { c.Chain.PostSOCKS5 = hops }},
		{"subscription pre", "subscriptions[0].pre_socks5", func(c *Config, hops HopList) {
			c.Subscriptions = []Subscription{{Name: "provider", URL: "https://provider.example/sub", PreSOCKS5: hops}}
		}},
		{"subscription post", "subscriptions[0].post_socks5", func(c *Config, hops HopList) {
			c.Subscriptions = []Subscription{{Name: "provider", URL: "https://provider.example/sub", PostSOCKS5: hops}}
		}},
		{"account pre", "accounts[0].pre_socks5", func(c *Config, hops HopList) {
			c.Accounts = []Account{{Login: "alice", Password: InitPlaceholder, PreSOCKS5: hops}}
		}},
		{"account post", "accounts[0].post_socks5", func(c *Config, hops HopList) {
			c.Accounts = []Account{{Login: "alice", Password: InitPlaceholder, PostSOCKS5: hops}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			// A 17-entry global post list is rejected by the separate MATCH
			// fallback guard before the size check unless account-only auth has
			// a configured named account. Keep these limit cases independent
			// of that guard so they exercise the list cap itself.
			cfg.Accounts = []Account{{Login: "alice", Password: InitPlaceholder, Provider: "provider", ExitCountry: "DE"}}
			hops := HopList{"socks5://one.example:1080", "socks5://two.example:1080"}
			for len(hops) < MaxHopsPerSide+1 {
				hops = append(hops, fmt.Sprintf("socks5://extra-%d.example:1080", len(hops)))
			}
			tc.set(cfg, hops)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "at most 16 hops per side") {
				t.Fatalf("validation error = %v, want field %s and maximum-length diagnostic", err, tc.field)
			}
		})
	}
}

func TestHopListValidation(t *testing.T) {
	valid := make(HopList, MaxHopsPerSide)
	for i := range valid {
		valid[i] = fmt.Sprintf("socks5://valid-%d.example:1080", i)
	}
	if err := ValidateHopList("pre_socks5", valid); err != nil {
		t.Fatalf("accepted 16-hop list: %v", err)
	}
	tooMany := append(append(HopList(nil), valid...), "socks5://extra.example:1080")
	if err := ValidateHopList("pre_socks5", tooMany); err == nil || !strings.Contains(err.Error(), "at most 16 hops per side") {
		t.Fatalf("17-hop list error = %v", err)
	}
	cases := []struct {
		name string
		hops HopList
		want string
	}{
		{"none mixed", HopList{"none", "socks5://proxy.example:1080"}, "none must stand alone"},
		{"duplicate", HopList{"socks5://proxy.example:1080", "socks5://proxy.example:1080"}, "duplicate"},
		{"empty entry", HopList{"socks5://proxy.example:1080", ""}, "empty hop"},
		{"invalid later entry", HopList{"socks5://one.example:1080", "ftp://secret.example:21"}, "pre_socks5[1]"},
		{"empty later entry", HopList{"socks5://one.example:1080", ""}, "pre_socks5[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHopList("pre_socks5", tc.hops)
			if err == nil || !strings.Contains(err.Error(), "pre_socks5") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want field pre_socks5 and %q", err, tc.want)
			}
		})
	}
	if err := ValidateHopList("pre_socks5", valid); err != nil {
		t.Fatalf("accepted maximum hop list: %v", err)
	}
	secret := "socks5://fake-user:fake-password@proxy.example:1080"
	for _, hops := range []HopList{
		{secret, secret},
		{"ftp://fake-user:fake-password@proxy.example:21"},
	} {
		err := ValidateHopList("pre_socks5", hops)
		if err == nil || strings.Contains(err.Error(), "fake-user") || strings.Contains(err.Error(), "fake-password") {
			t.Error("hop validation error was missing or exposed URL credentials")
		}
	}
	if got := InheritHops(HopList{"NONE"}, HopList{"parent"}); len(got) != 1 || !strings.EqualFold(got[0], "none") {
		t.Fatalf("explicit none was not preserved: %#v", got)
	}
	if got := InheritHops(nil, HopList{"parent"}); len(got) != 1 || got[0] != "parent" {
		t.Fatalf("inherited = %#v", got)
	}
	if got := InheritHops(HopList{"own"}, HopList{"parent"}); len(got) != 1 || got[0] != "own" {
		t.Fatalf("own = %#v", got)
	}
}

func TestSOCKS5ChainMarshalRoundTrip(t *testing.T) {
	c := Default()
	c.Chain = Chain{PreSOCKS5: HopList{"socks5://user:p%40ss@[::1]:1080"}, PostSOCKS5: HopList{"none"}}
	c.Accounts = []Account{{Login: "alice", Password: "init", PreSOCKS5: HopList{"none"}, PostSOCKS5: HopList{"socks5://proxy.example:1080"}}}
	c.Subscriptions = []Subscription{{Name: "provider", URL: "https://provider.example/sub", PreSOCKS5: HopList{"socks5://provider.example:1080"}, PostSOCKS5: HopList{"none"}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("chain round-trip fixture failed validation: %v", err)
	}
	doc, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "pre_socks5") || !strings.Contains(doc, "post_socks5") {
		t.Fatal("chain keys not rendered")
	}
	if !strings.Contains(doc, "pre_socks5: none") {
		t.Fatal("account none was not preserved in output")
	}
	if !strings.Contains(doc, "socks5://user:p%40ss@") {
		t.Fatal("marshal did not preserve scalar URL")
	}
	if c.Accounts[0].PreSOCKS5[0] != "none" {
		t.Fatal("Marshal mutated the source account")
	}
	roundTrip, err := ktavLoadsIntoConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !configEqual(roundTrip, c) {
		t.Fatal("chain values did not round-trip")
	}
}

func TestHopListLegacyScalarAndListRoundTrip(t *testing.T) {
	emptyScalar, err := ktavLoadsIntoConfig("chain: {\n  pre_socks5::\n  post_socks5: NONE\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := emptyScalar.Validate(); err != nil {
		t.Fatalf("empty/none scalar fixture failed validation: %v", err)
	}
	if len(emptyScalar.Chain.PreSOCKS5) != 0 || len(emptyScalar.Chain.PostSOCKS5) != 1 || !strings.EqualFold(emptyScalar.Chain.PostSOCKS5[0], "none") {
		t.Fatal("empty scalar inheritance or case-insensitive none was not preserved")
	}
	legacy := "chain: {\n  pre_socks5: socks5://legacy-pre.example:1080\n  post_socks5: http://legacy-post.example:8080\n}\nsubscriptions: [\n  {\n    name: provider\n    url: https://provider.example/sub\n    pre_socks5: socks5://provider-pre.example:1080\n    post_socks5: http://provider-post.example:8080\n  }\n]\naccounts: [\n  {\n    login: alice\n    password: init\n    pre_socks5: socks5://account-pre.example:1080\n    post_socks5: http://account-post.example:8080\n  }\n]\n"
	cfg, err := ktavLoadsIntoConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("legacy scalar fixture failed validation: %v", err)
	}
	want := []string{
		"socks5://legacy-pre.example:1080", "http://legacy-post.example:8080",
		"socks5://provider-pre.example:1080", "http://provider-post.example:8080",
		"socks5://account-pre.example:1080", "http://account-post.example:8080",
	}
	got := []HopList{cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5, cfg.Subscriptions[0].PreSOCKS5, cfg.Subscriptions[0].PostSOCKS5, cfg.Accounts[0].PreSOCKS5, cfg.Accounts[0].PostSOCKS5}
	for i := range got {
		if len(got[i]) != 1 || got[i][0] != want[i] {
			t.Fatalf("legacy field %d was not represented as its one-item list", i)
		}
	}
	doc, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	legacyWire := *cfg
	legacyWire.Listen.AllowedClients = ""
	oldDoc, err := ktavDumps(&configWithScalarHops{
		Listen: legacyWire.Listen, Auth: legacyWire.Auth, Fetch: legacyWire.Fetch,
		Chain:         scalarChain{PreSOCKS5: "socks5://legacy-pre.example:1080", PostSOCKS5: "http://legacy-post.example:8080"},
		Subscriptions: []scalarSubscription{{Name: "provider", URL: "https://provider.example/sub", PreSOCKS5: "socks5://provider-pre.example:1080", PostSOCKS5: "http://provider-post.example:8080"}},
		Accounts:      []scalarAccount{{Login: "alice", Password: "init", PreSOCKS5: "socks5://account-pre.example:1080", PostSOCKS5: "http://account-post.example:8080"}},
		Selection:     legacyWire.Selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimPrefix(doc, managedHeader) != oldDoc {
		t.Fatal("legacy scalar marshal output changed")
	}
	round, err := ktavLoadsIntoConfig(doc)
	if err != nil || !configEqual(round, cfg) {
		t.Fatal("six scalar hop fields did not round-trip")
	}

	multi := Default()
	multi.Chain = Chain{PreSOCKS5: HopList{"socks5://first.example:1080", "http://second.example:8080"}, PostSOCKS5: HopList{"https://first.example:8443", "socks5://last.example:1080"}}
	multi.Subscriptions = []Subscription{{Name: "provider", URL: "https://provider.example/sub", PreSOCKS5: HopList{"socks5://first.example:1080", "http://second.example:8080"}, PostSOCKS5: HopList{"https://first.example:8443", "socks5://last.example:1080"}}}
	multi.Accounts = []Account{{Login: "alice", Password: "init", PreSOCKS5: HopList{"socks5://first.example:1080", "http://second.example:8080"}, PostSOCKS5: HopList{"https://first.example:8443", "socks5://last.example:1080"}}}
	doc, err = Marshal(multi)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "pre_socks5: [\n") || !strings.Contains(doc, "post_socks5: [\n") || !strings.Contains(doc, "http://second.example:8080") || !strings.Contains(doc, "socks5://last.example:1080") {
		t.Fatal("multi-hop list was not rendered as an ordered array")
	}
	if roundTrip, err := ktavLoadsIntoConfig(doc); err != nil || !configEqual(roundTrip, multi) {
		t.Fatalf("multi-hop round trip failed")
	}
}

type scalarChain struct {
	PreSOCKS5  string `json:"pre_socks5,omitempty"`
	PostSOCKS5 string `json:"post_socks5,omitempty"`
}
type scalarSubscription struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Via        string `json:"via,omitempty"`
	PreSOCKS5  string `json:"pre_socks5,omitempty"`
	PostSOCKS5 string `json:"post_socks5,omitempty"`
}
type scalarAccount struct {
	Login       string `json:"login"`
	Password    string `json:"password"`
	Provider    string `json:"provider,omitempty"`
	ExitCountry string `json:"exit_country,omitempty"`
	Allowed     string `json:"allowed_clients,omitempty"`
	PreSOCKS5   string `json:"pre_socks5,omitempty"`
	PostSOCKS5  string `json:"post_socks5,omitempty"`
	Exit        string `json:"exit,omitempty"`
}
type configWithScalarHops struct {
	Listen        Listen               `json:"listen"`
	Auth          Auth                 `json:"auth"`
	Fetch         Fetch                `json:"fetch"`
	Chain         scalarChain          `json:"chain"`
	Subscriptions []scalarSubscription `json:"subscriptions,omitempty"`
	Accounts      []scalarAccount      `json:"accounts,omitempty"`
	Selection     Selection            `json:"selection"`
}

func TestLoadHopListShapes(t *testing.T) {
	const secret = "socks5://fake-user:fake-password@proxy.example:bad"
	cases := []struct {
		name    string
		source  string
		want    HopList
		wantErr bool
	}{
		{
			name:   "legacy scalar",
			source: "chain: {\n    pre_socks5: socks5://legacy.example:1080\n}\n",
			want:   HopList{"socks5://legacy.example:1080"},
		},
		{
			name:   "singleton list",
			source: "chain: {\n    pre_socks5: [\n        socks5://list.example:1080\n    ]\n}\n",
			want:   HopList{"socks5://list.example:1080"},
		},
		{name: "non-array object", source: "chain: {\n    pre_socks5: {}\n}\n", wantErr: true},
		{name: "non-string scalar", source: "chain: {\n    pre_socks5: true\n}\n", wantErr: true},
		{name: "null", source: "chain: {\n    pre_socks5: null\n}\n", wantErr: true},
		{name: "non-string array member", source: "chain: {\n    pre_socks5: [\n        true\n    ]\n}\n", wantErr: true},
		{name: "empty item", source: "chain: {\n    pre_socks5: [\n        ::\n    ]\n}\n", wantErr: true},
		{name: "mixed none", source: "chain: {\n    pre_socks5: [\n        none\n        socks5://other.example:1080\n    ]\n}\n", wantErr: true},
		{name: "duplicate", source: "chain: {\n    pre_socks5: [\n        socks5://same.example:1080\n        socks5://same.example:1080\n    ]\n}\n", wantErr: true},
		{name: "multiple hops", source: "chain: {\n    pre_socks5: [\n        socks5://one.example:1080\n        socks5://two.example:1080\n    ]\n}\n", want: HopList{"socks5://one.example:1080", "socks5://two.example:1080"}},
		{name: "malformed credential URI", source: "chain: {\n    pre_socks5: " + secret + "\n}\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.ktav")
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatal("write config fixture")
			}
			cfg, err := Load(path)
			if !tc.wantErr {
				if err != nil {
					t.Fatal("Load rejected a valid hop-list fixture")
				}
				if err := cfg.Validate(); err != nil {
					t.Fatalf("loaded fixture failed config validation: %v", err)
				}
				if len(cfg.Chain.PreSOCKS5) != len(tc.want) {
					t.Fatal("loaded hop list had the wrong length")
				}
				for i := range tc.want {
					if cfg.Chain.PreSOCKS5[i] != tc.want[i] {
						t.Fatal("loaded hop list differed from fixture")
					}
				}
				return
			}
			if err == nil {
				t.Fatal("Load accepted a malformed hop-list fixture")
			}
			cliErr, ok := err.(*cli.Error)
			if !ok {
				t.Fatal("Load returned an unexpected error type")
			}
			if cliErr.Code != CodeConfigInvalid || cliErr.Exit != cli.ExitUsage {
				t.Fatal("Load error did not use CONFIG_INVALID/ExitUsage")
			}
			for _, output := range []string{err.Error(), cliErr.Message, cliErr.Hint} {
				if strings.Contains(output, "fake-user") || strings.Contains(output, "fake-password") {
					t.Fatal("Load error exposed credentials")
				}
			}
		})
	}
}
