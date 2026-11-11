package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountResolverListShowStatusMatrix(t *testing.T) {
	const accountURL = "socks5://a.example:1080"
	const globalURL = "socks5://g.example:1080"
	cases := []struct {
		name, own, global, value, source string
		ownValue                         *[]string
	}{
		{"account overrides global", accountURL, globalURL, accountURL, "account", ptrHops(accountURL)},
		{"account overrides global none", accountURL, "none", accountURL, "account", ptrHops(accountURL)},
		{"account alone", accountURL, "", accountURL, "account", ptrHops(accountURL)},
		{"global fallback", "", globalURL, globalURL, "global", nil},
		{"global none", "", "none", "none", "none", nil},
		{"both absent", "", "", "none", "none", nil},
		{"account none overrides global", "none", globalURL, "none", "none", ptrHops("none")},
		{"account none with global none", "none", "none", "none", "none", ptrHops("none")},
		{"account none alone", "none", "", "none", "none", ptrHops("none")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			own, global := legacyHopList(tc.own), legacyHopList(tc.global)
			cfg := &config.Config{Chain: config.Chain{PreSOCKS5: global}, Accounts: []config.Account{{Login: "alice", PreSOCKS5: own}}}
			listed := accountList(cfg)
			if len(listed) != 1 {
				t.Fatalf("list length = %d", len(listed))
			}
			list := listed[0].PreSOCKS5
			if !reflect.DeepEqual(list.Value, []string{tc.value}) || list.Source != tc.source || list.Own != nil {
				t.Fatalf("list = %+v", list)
			}
			show := infoForAccount(cfg, &cfg.Accounts[0]).PreSOCKS5
			if !reflect.DeepEqual(show.Value, []string{tc.value}) || show.Source != tc.source || !equalStringSlicePtr(show.Own, tc.ownValue) {
				t.Fatalf("show = %+v", show)
			}
			showJSON, err := json.Marshal(showChainInfoForAccount(show))
			if err != nil || !strings.Contains(string(showJSON), `"own":`) {
				t.Fatalf("show JSON must include own: %s (%v)", showJSON, err)
			}
			if status := statusAccountChain(own, nil, global, nil); !slices.Equal(status.PreSOCKS5, []string{tc.value}) || status.PreSOCKS5Source != tc.source {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func legacyHopList(value string) config.HopList {
	if value == "" {
		return nil
	}
	return config.HopList{value}
}

func ptrHops(s string) *[]string { value := []string{s}; return &value }
func equalStringSlicePtr(a, b *[]string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return reflect.DeepEqual(*a, *b)
}
