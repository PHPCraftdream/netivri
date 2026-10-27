package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountResolverListShowStatusMatrix(t *testing.T) {
	const accountURL = "socks5://a.example:1080"
	const globalURL = "socks5://g.example:1080"
	cases := []struct {
		name, own, global, value, source string
		ownValue                         *string
	}{
		{"account overrides global", accountURL, globalURL, accountURL, "account", ptrString(accountURL)},
		{"account overrides global none", accountURL, "none", accountURL, "account", ptrString(accountURL)},
		{"account alone", accountURL, "", accountURL, "account", ptrString(accountURL)},
		{"global fallback", "", globalURL, globalURL, "global", nil},
		{"global none", "", "none", "none", "none", nil},
		{"both absent", "", "", "none", "none", nil},
		{"account none overrides global", "none", globalURL, "none", "none", ptrString("none")},
		{"account none with global none", "none", "none", "none", "none", ptrString("none")},
		{"account none alone", "none", "", "none", "none", ptrString("none")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Chain: config.Chain{PreSOCKS5: tc.global}, Accounts: []config.Account{{Login: "alice", PreSOCKS5: tc.own}}}
			listed := accountList(cfg)
			if len(listed) != 1 {
				t.Fatalf("list length = %d", len(listed))
			}
			list := listed[0].PreSOCKS5
			if list.Value != tc.value || list.Source != tc.source || list.Own != nil {
				t.Fatalf("list = %+v", list)
			}
			show := infoForAccount(cfg, &cfg.Accounts[0]).PreSOCKS5
			if show.Value != tc.value || show.Source != tc.source || !equalStringPtr(show.Own, tc.ownValue) {
				t.Fatalf("show = %+v", show)
			}
			showJSON, err := json.Marshal(showChainInfoForAccount(show))
			if err != nil || !strings.Contains(string(showJSON), `"own":`) {
				t.Fatalf("show JSON must include own: %s (%v)", showJSON, err)
			}
			status := statusAccountChain(tc.own, "", tc.global, "")
			if status.PreSOCKS5 != tc.value || status.PreSOCKS5Source != tc.source {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func ptrString(s string) *string { return &s }
func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
