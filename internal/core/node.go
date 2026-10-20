package core

import (
	"fmt"
	"math"
	"strconv"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// nodeName is the fixed internal name of the routed node. It overwrites any
// provider-supplied name so a name can never collide with Mihomo's built-in
// proxies (DIRECT, REJECT, GLOBAL, ...) or inject rule syntax ("a,DIRECT").
const nodeName = "netivri-node"

// knownNodeTypes is the set of upstream proxy types Mihomo v1.19.32 accepts
// in adapter.ParseProxy, minus the pseudo-types direct, dns, reject and
// rematch: a "direct" node would dial the destination itself and defeat the
// single-upstream guarantee. Anything else is rejected before ApplyConfig so
// the user gets a clear NODE_INVALID error instead of a Mihomo parse error.
var knownNodeTypes = map[string]bool{
	"ss": true, "ssr": true, "socks5": true, "http": true,
	"vmess": true, "vless": true, "snell": true, "trojan": true,
	"hysteria": true, "hysteria2": true, "wireguard": true, "tuic": true,
	"shadowquic": true, "gost-relay": true, "ssh": true, "mieru": true,
	"anytls": true, "sudoku": true, "masque": true, "trusttunnel": true,
	"openvpn": true, "tailscale": true, "zerotier": true, "easytier": true,
}

// nodeInvalid builds a NODE_INVALID error. Messages contain only type/server
// shape information, never passwords, uuids or ciphers.
func nodeInvalid(format string, args ...any) *cli.Error {
	return &cli.Error{
		Code:    "NODE_INVALID",
		Message: "invalid node: " + fmt.Sprintf(format, args...),
		Exit:    cli.ExitUsage,
	}
}

// prepareNode validates a caller-supplied Mihomo proxy mapping and turns it
// into the raw-config values for single-node routing: one proxy entry with
// the fixed internal name and one MATCH rule pointing at it. It never
// mutates the caller's map. Mihomo's built-in proxies (DIRECT, REJECT, ...)
// stay available but unused: the MATCH rule always routes to the node, so
// there is no fallback to DIRECT when the node is unreachable.
func prepareNode(node map[string]any) ([]map[string]any, []string, error) {
	if node == nil {
		return nil, nil, nodeInvalid("node is missing")
	}

	tp, _ := node["type"].(string)
	if tp == "" {
		return nil, nil, nodeInvalid("type is missing")
	}
	if !knownNodeTypes[tp] {
		return nil, nil, nodeInvalid("unknown type %q", tp)
	}

	server, _ := node["server"].(string)
	if server == "" {
		return nil, nil, nodeInvalid("server is missing for type %q", tp)
	}
	switch p := node["port"].(type) {
	case int:
		if p < 1 || p > 65535 {
			return nil, nil, nodeInvalid("port %d out of range for type %q", p, tp)
		}
	case float64:
		// A JSON round trip yields float64: it must be a whole number in range.
		if p != math.Trunc(p) || p < 1 || p > 65535 {
			return nil, nil, nodeInvalid("port %v is not a valid port for type %q", p, tp)
		}
	case string:
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, nil, nodeInvalid("port %q is not a valid port for type %q", p, tp)
		}
	default:
		return nil, nil, nodeInvalid("port is missing for type %q", tp)
	}

	// Shallow copy: the caller keeps ownership of its map.
	entry := make(map[string]any, len(node)+1)
	for k, v := range node {
		entry[k] = v
	}
	entry["name"] = nodeName
	// dialer-proxy would chain the node through another proxy; strip it so
	// every connection really goes through this node only.
	delete(entry, "dialer-proxy")

	return []map[string]any{entry}, []string{"MATCH," + nodeName}, nil
}

// Route results reported by Instance.Route.
const (
	routeDirect = "direct"
	routeNode   = "node"
)
