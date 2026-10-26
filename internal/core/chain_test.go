package core

import (
	"reflect"
	"testing"
)

func TestPrepareRoutesDirectAccountRouteUsesDirect(t *testing.T) {
	o := Options{AccountRoute: map[string]string{"a": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "none"}}
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 0 || len(groups) != 1 || groups[0]["proxies"].([]string)[0] != "DIRECT" {
		t.Fatalf("proxies/groups=%v/%v", proxies, groups)
	}
	if rules[0] != "IN-USER,a,netivri-group-0" || rules[1] != "MATCH,DIRECT" {
		t.Fatalf("rules=%v", rules)
	}
}
func TestPrepareRoutesDirectAccountPostDoesNotUseDefaultNode(t *testing.T) {
	o := Options{Node: socks5Node(), AccountRoute: map[string]string{"a": "direct"}, RoutePost: map[string]string{"direct": "socks5://post.example:1080"}}
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0]["proxies"].([]string)[0] != "netivri-r0-0-post" {
		t.Fatalf("groups=%v", groups)
	}
	var post map[string]any
	for _, proxy := range proxies {
		if proxy["name"] == "netivri-r0-0-post" {
			post = proxy
		}
	}
	if post == nil || post["dialer-proxy"] != "DIRECT" || rules[0] != "IN-USER,a,netivri-group-0" || rules[len(rules)-1] != "MATCH,netivri-node" {
		t.Fatalf("post=%v rules=%v", post, rules)
	}
}
func TestPrepareRoutesDirectAccountPostUsesDirectBase(t *testing.T) {
	o := Options{AccountRoute: map[string]string{"a": "direct"}, RoutePost: map[string]string{"direct": "socks5://post.example:1080"}}
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0]["name"] != "netivri-r0-0-post" || proxies[0]["dialer-proxy"] != "DIRECT" {
		t.Fatalf("proxies=%v", proxies)
	}
	if len(groups) != 1 || groups[0]["proxies"].([]string)[0] != "netivri-r0-0-post" || rules[0] != "IN-USER,a,netivri-group-0" {
		t.Fatalf("groups/rules=%v/%v", groups, rules)
	}
}
func TestPrepareRoutesPostPreservesDirectAccountRoute(t *testing.T) {
	o := Options{RouteNodes: map[string][]map[string]any{"r": {socks5Node()}}, AccountRoute: map[string]string{"a": "r"}, RoutePost: map[string]string{"r": "socks5://post.example:1080"}}
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 2 || len(groups) != 1 {
		t.Fatalf("proxies/groups=%d/%d", len(proxies), len(groups))
	}
	if proxies[0]["name"] != "netivri-r0-0-post" || proxies[1]["name"] != "netivri-r0-0" || proxies[0]["dialer-proxy"] != "netivri-r0-0" {
		t.Fatalf("proxies=%v", proxies)
	}
	if groups[0]["proxies"].([]string)[0] != "netivri-r0-0-post" || rules[len(rules)-1] != "MATCH,DIRECT" {
		t.Fatalf("groups/rules=%v/%v", groups, rules)
	}
}
func TestPrepareRoutesGlobalPost(t *testing.T) {
	proxies, _, rules, err := prepareRoutes(Options{Node: socks5Node(), GlobalPost: "socks5://post.example:1080"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := proxies[1]["dialer-proxy"]; !ok || proxies[1]["dialer-proxy"] != nodeName || rules[0] != "MATCH,netivri-global-post" {
		t.Fatalf("proxies/rules=%v/%v", proxies, rules)
	}
}
func TestPrepareRoutesGlobalPostWithoutDefaultNode(t *testing.T) {
	o := Options{AccountRoute: map[string]string{"a": "r"}, RouteNodes: map[string][]map[string]any{"r": {socks5Node()}}, GlobalPost: "socks5://post.example:1080"}
	proxies, groups, rules, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	if proxies[0]["name"] != "netivri-global-post" || proxies[0]["dialer-proxy"] != nil || rules[len(rules)-1] != "MATCH,netivri-global-post" || len(groups) != 1 {
		t.Fatalf("proxies/groups/rules=%v/%v/%v", proxies, groups, rules)
	}
}
func TestPrepareRoutesStandaloneGlobalPost(t *testing.T) {
	proxies, _, rules, err := prepareRoutes(Options{GlobalPost: "socks5://post.example:1080"})
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || rules[0] != "MATCH,netivri-global-post" {
		t.Fatalf("proxies/rules=%v/%v", proxies, rules)
	}
}
func TestPrepareRoutesPreUsesIndependentHelper(t *testing.T) {
	o := Options{RouteNodes: map[string][]map[string]any{"r": {socks5Node()}}, AccountRoute: map[string]string{"a": "r"}, RoutePre: map[string]string{"r": "socks5://pre.example:1080"}}
	proxies, groups, _, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	pre, member := proxies[0], proxies[1]
	if _, ok := pre["dialer-proxy"]; ok || member["dialer-proxy"] != pre["name"] || groups[0]["proxies"].([]string)[0] != member["name"] {
		t.Fatalf("proxies/groups=%v/%v", proxies, groups)
	}
}
func TestChainProxyFullMappings(t *testing.T) {
	tests := []struct {
		name, raw string
		want      map[string]any
	}{
		{"no credentials", "socks5://proxy.example:1080", map[string]any{"name": "hop", "type": "socks5", "server": "proxy.example", "port": "1080"}},
		{"username only", "socks5://alice@proxy.example:1081", map[string]any{"name": "hop", "type": "socks5", "server": "proxy.example", "port": "1081", "username": "alice"}},
		{"encoded credentials ipv6", "socks5://user%40realm:p%3Ass@[2001:db8::1]:65535", map[string]any{"name": "hop", "type": "socks5", "server": "2001:db8::1", "port": "65535", "username": "user@realm", "password": "p:ss"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chainProxy(tt.raw, "hop")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mapping = %#v, want %#v", got, tt.want)
			}
		})
	}
	for _, raw := range []string{"socks5://proxy.example", "socks5://proxy.example:0", "socks5://proxy.example:65536"} {
		t.Run("invalid port "+raw, func(t *testing.T) {
			if mapping, err := chainProxy(raw, "hop"); err == nil {
				t.Fatalf("chainProxy(%q) = %v, want invalid-port error", raw, mapping)
			}
		})
	}
}
