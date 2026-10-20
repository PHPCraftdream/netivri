package core

import (
	"reflect"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// socks5Node is the smallest valid non-exempt node mapping.
func socks5Node() map[string]any {
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": 1080}
}

func TestPrepareNodeNil(t *testing.T) {
	_, _, err := prepareNode(nil)
	var cliErr *cli.Error
	if !asNodeInvalid(err, &cliErr) {
		t.Fatalf("prepareNode(nil): err = %v (%T), want *cli.Error", err, err)
	}
	if cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
		t.Fatalf("code = %q, exit = %d, want NODE_INVALID/%d", cliErr.Code, cliErr.Exit, cli.ExitUsage)
	}
}

// asNodeInvalid is errors.As without importing errors just for one check.
func asNodeInvalid(err error, target **cli.Error) bool {
	for err != nil {
		if e, ok := err.(*cli.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestPrepareNodeInvalid(t *testing.T) {
	cases := []struct {
		name string
		node map[string]any
	}{
		{"missing-type", map[string]any{"server": "127.0.0.1", "port": 1080}},
		{"empty-type", map[string]any{"type": "", "server": "127.0.0.1", "port": 1080}},
		{"unknown-type", map[string]any{"type": "not-a-proxy", "server": "127.0.0.1", "port": 1080}},
		{"missing-server", map[string]any{"type": "socks5", "port": 1080}},
		{"empty-server", map[string]any{"type": "socks5", "server": "", "port": 1080}},
		{"missing-port", map[string]any{"type": "socks5", "server": "127.0.0.1"}},
		{"port-zero", map[string]any{"type": "socks5", "server": "127.0.0.1", "port": 0}},
		{"port-too-big", map[string]any{"type": "socks5", "server": "127.0.0.1", "port": 70000}},
		{"port-string-bad", map[string]any{"type": "socks5", "server": "127.0.0.1", "port": "not-a-port"}},
		{"port-float-bad", map[string]any{"type": "socks5", "server": "127.0.0.1", "port": 3.5}},
		{"port-bool", map[string]any{"type": "socks5", "server": "127.0.0.1", "port": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := prepareNode(tc.node)
			var cliErr *cli.Error
			if !asNodeInvalid(err, &cliErr) {
				t.Fatalf("err = %v (%T), want *cli.Error NODE_INVALID", err, err)
			}
			if cliErr.Code != "NODE_INVALID" || cliErr.Exit != cli.ExitUsage {
				t.Fatalf("code = %q, exit = %d, want NODE_INVALID/%d", cliErr.Code, cliErr.Exit, cli.ExitUsage)
			}
			if want := "invalid node: "; len(cliErr.Message) < len(want) || cliErr.Message[:len(want)] != want {
				t.Fatalf("message = %q, want it to start with %q", cliErr.Message, want)
			}
		})
	}
}

// Pseudo-types would dial the destination themselves (direct) or never
// reach an upstream: they can never be the routed node.
func TestPrepareNodeRejectsPseudoTypes(t *testing.T) {
	for _, tp := range []string{"direct", "dns", "reject", "rematch"} {
		for _, node := range []map[string]any{
			{"type": tp},
			{"type": tp, "server": "127.0.0.1", "port": 1080},
		} {
			_, _, err := prepareNode(node)
			var cliErr *cli.Error
			if !asNodeInvalid(err, &cliErr) || cliErr.Code != "NODE_INVALID" {
				t.Fatalf("type %q: err = %v, want NODE_INVALID", tp, err)
			}
		}
	}
}

// A whole-number float64 port (what a JSON round trip produces) is valid.
func TestPrepareNodeWholeFloatPort(t *testing.T) {
	node := map[string]any{"type": "socks5", "server": "127.0.0.1", "port": float64(8388)}
	if _, _, err := prepareNode(node); err != nil {
		t.Fatalf("whole float port rejected: %v", err)
	}
}

// Port encodings Mihomo's weakly-typed decoder also accepts must pass
// validation in their numeric ranges.
func TestPrepareNodePortEncodings(t *testing.T) {
	for _, port := range []any{1080, float64(1080), "1080"} {
		proxies, _, err := prepareNode(map[string]any{
			"type": "socks5", "server": "127.0.0.1", "port": port,
		})
		if err != nil {
			t.Fatalf("port %v (%T): %v", port, port, err)
		}
		if got := proxies[0]["port"]; got != port {
			t.Fatalf("port %v (%T): entry kept %v (%T)", port, port, got, got)
		}
	}
}

// The name is overwritten with the fixed internal one, so a provider-supplied
// name can never collide with built-ins or inject rule syntax via commas.
func TestPrepareNodeNameNeutralised(t *testing.T) {
	node := socks5Node()
	node["name"] = "a,DIRECT"
	proxies, rules, err := prepareNode(node)
	if err != nil {
		t.Fatalf("prepareNode: %v", err)
	}
	if got := proxies[0]["name"]; got != nodeName {
		t.Fatalf("entry name = %q, want %q", got, nodeName)
	}
	wantRules := []string{"MATCH," + nodeName}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
	if node["name"] != "a,DIRECT" {
		t.Fatalf("caller map mutated: name = %v", node["name"])
	}
}

// prepareNode must copy, never mutate, the caller's map.
func TestPrepareNodeDoesNotMutateCallerMap(t *testing.T) {
	node := socks5Node()
	node["name"] = "orig"
	node["dialer-proxy"] = "other-group"
	before := make(map[string]any, len(node))
	for k, v := range node {
		before[k] = v
	}

	proxies, _, err := prepareNode(node)
	if err != nil {
		t.Fatalf("prepareNode: %v", err)
	}
	entry := proxies[0]

	if entry["name"] != nodeName {
		t.Fatalf("entry name = %v, want %q", entry["name"], nodeName)
	}
	if _, ok := entry["dialer-proxy"]; ok {
		t.Fatal("entry still has dialer-proxy, want it stripped")
	}
	if !reflect.DeepEqual(node, before) {
		t.Fatalf("caller map mutated: got %v, want %v", node, before)
	}
}
