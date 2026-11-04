package core

import (
	"reflect"
	"testing"
)

// Nodes of one route may take different hops (nodes of different providers).
func TestPrepareRoutesPerNodeHops(t *testing.T) {
	n0, n1 := socks5Node(), socks5Node()
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {n0, n1}},
		AccountRoute: map[string]string{"a": "r"},
		RoutePre:     map[string]string{"r": ""},
		RoutePost:    map[string]string{"r": ""},
		NodePre:      map[string][]string{"r": {"http://pre.example:8080", "none"}},
		NodePost:     map[string][]string{"r": {"", "socks5://post.example:1080"}},
	}
	proxies, groups, _, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, p := range proxies {
		byName[p["name"].(string)] = p
	}
	if byName["netivri-r0-0-pre"] == nil || byName["netivri-r0-0-pre"]["type"] != "http" {
		t.Errorf("node 0 must have an http pre hop: %v", proxies)
	}
	if byName["netivri-r0-0"]["dialer-proxy"] != "netivri-r0-0-pre" {
		t.Errorf("node 0 is not dialled through its pre hop: %v", byName["netivri-r0-0"])
	}
	if byName["netivri-r0-1-pre"] != nil || byName["netivri-r0-1"]["dialer-proxy"] != nil {
		t.Errorf("node 1 must have no pre hop: %v", proxies)
	}
	if byName["netivri-r0-0-post"] != nil {
		t.Errorf("node 0 must have no post hop: %v", proxies)
	}
	if post := byName["netivri-r0-1-post"]; post == nil || post["type"] != "socks5" || post["dialer-proxy"] != "netivri-r0-1" {
		t.Errorf("node 1 must have a socks5 post hop behind it: %v", proxies)
	}
	if len(groups) != 1 || !reflect.DeepEqual(groups[0]["proxies"], []string{"netivri-r0-0", "netivri-r0-1-post"}) {
		t.Errorf("group members = %v", groups)
	}
}

// Without per-node hops the route-level ones apply to every node, as before.
func TestPrepareRoutesRouteHopsWithoutNodeHops(t *testing.T) {
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node(), socks5Node()}},
		AccountRoute: map[string]string{"a": "r"},
		RoutePre:     map[string]string{"r": "http://pre.example:8080"},
	}
	proxies, _, _, err := prepareRoutes(o)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, p := range proxies {
		if n, _ := p["name"].(string); n == "netivri-r0-0-pre" || n == "netivri-r0-1-pre" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("want a pre hop for each of the two nodes, got %d in %v", found, proxies)
	}
}

func TestPrepareRoutesRejectsMismatchedNodeHops(t *testing.T) {
	o := Options{
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node(), socks5Node()}},
		AccountRoute: map[string]string{"a": "r"},
		NodePre:      map[string][]string{"r": {"http://pre.example:8080"}},
	}
	if _, _, _, err := prepareRoutes(o); err == nil {
		t.Fatal("a pre hop list shorter than the nodes must be rejected")
	}
	o.NodePre = nil
	o.NodePost = map[string][]string{"r": {"", "", ""}}
	if _, _, _, err := prepareRoutes(o); err == nil {
		t.Fatal("a post hop list longer than the nodes must be rejected")
	}
}

func TestNodeHopsFallBackToTheRoute(t *testing.T) {
	o := Options{RoutePre: map[string]string{"r": "x"}, RoutePost: map[string]string{"r": "y"}, NodePre: map[string][]string{"r": {"a"}}}
	if o.nodePre("r", 0) != "a" || o.nodePre("r", 1) != "x" || o.nodePost("r", 0) != "y" {
		t.Errorf("nodePre/nodePost = %q %q %q", o.nodePre("r", 0), o.nodePre("r", 1), o.nodePost("r", 0))
	}
}
