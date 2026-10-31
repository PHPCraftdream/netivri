package core

import (
	"reflect"
	"testing"
)

// A chain hop is a socks5, http or https proxy; https is an http proxy over TLS.
func TestChainProxyRendersTheMihomoOutbound(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want map[string]any
	}{
		{"socks5://proxy.example:1080", map[string]any{"name": "hop", "type": "socks5", "server": "proxy.example", "port": "1080"}},
		{"SOCKS5://u:p%40ss@proxy.example:1080", map[string]any{"name": "hop", "type": "socks5", "server": "proxy.example", "port": "1080", "username": "u", "password": "p@ss"}},
		{"http://proxy.example:8080", map[string]any{"name": "hop", "type": "http", "server": "proxy.example", "port": "8080"}},
		{"HTTP://alice:secret@proxy.example:3128", map[string]any{"name": "hop", "type": "http", "server": "proxy.example", "port": "3128", "username": "alice", "password": "secret"}},
		{"https://proxy.example:8443", map[string]any{"name": "hop", "type": "http", "server": "proxy.example", "port": "8443", "tls": true}},
		{"https://bob@[::1]:8443", map[string]any{"name": "hop", "type": "http", "server": "::1", "port": "8443", "tls": true, "username": "bob"}},
	} {
		got, err := chainProxy(c.raw, "hop")
		if err != nil {
			t.Errorf("chainProxy(%q): %v", c.raw, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("chainProxy(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestChainProxyRejectsWhatIsNotAHop(t *testing.T) {
	for _, raw := range []string{
		"", "://", "proxy.example:1080", "ftp://proxy.example:21", "socks4://proxy.example:1080", "socks5h://proxy.example:1080",
		"http://proxy.example", "https://proxy.example", "http://proxy.example:0", "http://proxy.example:70000", "http://proxy.example:80x",
		"http://proxy.example:8080/path", "http://proxy.example:8080?x=1", "http://proxy.example:8080#f", "http://:8080", "http:proxy.example:8080",
	} {
		if got, err := chainProxy(raw, "hop"); err == nil {
			t.Errorf("chainProxy(%q) = %v, want an error", raw, got)
		}
	}
}

// An https hop sits in the chain like any other: dialled through the node, and
// the destination reached through it.
func TestPrepareRoutesHTTPPostHop(t *testing.T) {
	o := Options{RouteNodes: map[string][]map[string]any{"r": {socks5Node()}}, AccountRoute: map[string]string{"a": "r"},
		RoutePre: map[string]string{"r": "http://pre.example:8080"}, RoutePost: map[string]string{"r": "https://post.example:8443"}}
	proxies, _, _, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, p := range proxies {
		byName[p["name"].(string)] = p
	}
	pre, node, post := byName["netivri-r0-0-pre"], byName["netivri-r0-0"], byName["netivri-r0-0-post"]
	if pre == nil || node == nil || post == nil {
		t.Fatalf("proxies = %v", proxies)
	}
	if pre["type"] != "http" || pre["tls"] != nil || node["dialer-proxy"] != "netivri-r0-0-pre" {
		t.Errorf("pre = %v, node = %v: want a plain http hop that the node is dialled through", pre, node)
	}
	if post["type"] != "http" || post["tls"] != true || post["dialer-proxy"] != "netivri-r0-0" {
		t.Errorf("post = %v: want an https hop dialled through the node", post)
	}
}
