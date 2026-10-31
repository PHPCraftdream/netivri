package core

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// The stage of a route a failure is put down to, see hopSet.attribute. A route
// with only its node has nothing to tell apart and has no stage named.
const (
	HopPre     = "pre hop"
	HopNode    = "node"
	HopPost    = "post hop"
	HopUnknown = "unknown hop"
)

// hopInfo is one stage of an account route as the attribution of a failure sees
// it: the address Mihomo names it by in its errors and the protocol it speaks.
type hopInfo struct {
	addr  string // host:port in lower case; empty when the stage does not exist
	proto string // socks5, http, https; the node's own type for the node
}

// hopSet is the chain of one member of a route: [pre] -> node -> [post].
type hopSet struct{ pre, node, post hopInfo }

// chainHop reads a pre or post URL (socks5://, http:// or https://); a value
// that is empty, none or not a proxy URL is no stage.
func chainHop(raw string) hopInfo {
	if raw == "" || isNoChain(raw) {
		return hopInfo{}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return hopInfo{}
	}
	return hopInfo{addr: strings.ToLower(u.Host), proto: strings.ToLower(u.Scheme)}
}

// nodeHop reads the address and type of a node mapping.
func nodeHop(node map[string]any) hopInfo {
	server, _ := node["server"].(string)
	port := fmt.Sprint(node["port"])
	if server == "" || node["port"] == nil {
		return hopInfo{}
	}
	typ, _ := node["type"].(string)
	return hopInfo{addr: strings.ToLower(net.JoinHostPort(server, port)), proto: strings.ToLower(typ)}
}

// routeHops is the chain of every node of a route.
func routeHops(nodes []map[string]any, pre, post string) []hopSet {
	out := make([]hopSet, len(nodes))
	for i, node := range nodes {
		out[i] = hopSet{pre: chainHop(pre), node: nodeHop(node), post: chainHop(post)}
	}
	return out
}

// chained reports whether there is a hop besides the node: only then a failure
// has more than one place to come from.
func (h hopSet) chained() bool { return h.pre.addr != "" || h.post.addr != "" }

// attribute says which stage of the chain a dial or probe error came from, or
// "" when the chain is only the node. The error text names the address of the
// stage that could not be reached or answered ("HOST:PORT connect error: ..."),
// and each stage wraps the one below it, so the stage named last is the one
// that failed. An error without an address (a proxy refusing the credentials
// in its handshake) is put down to the one stage that speaks that protocol, if
// there is one; anything else is HopUnknown. The address is only matched, never
// returned.
func (h hopSet) attribute(err error) string {
	if err == nil || !h.chained() {
		return ""
	}
	msg := strings.ToLower(err.Error())
	stages := [...]struct {
		name string
		hop  hopInfo
	}{{HopPost, h.post}, {HopPre, h.pre}, {HopNode, h.node}}

	best, bestAt := "", -1
	for _, s := range stages {
		if at := lastAddress(msg, s.hop.addr); at > bestAt {
			best, bestAt = s.name, at
		}
	}
	if best != "" {
		return best
	}
	family := errorFamily(msg)
	if family == "" {
		return HopUnknown
	}
	found := ""
	for _, s := range stages {
		if s.hop.addr != "" && protoFamily(s.hop.proto) == family {
			if found != "" {
				return HopUnknown
			}
			found = s.name
		}
	}
	if found == "" {
		return HopUnknown
	}
	return found
}

// lastAddress is the position of the last whole occurrence of addr in msg (not
// followed by a digit, so a port is not mistaken for a longer one), or -1.
func lastAddress(msg, addr string) int {
	if addr == "" {
		return -1
	}
	end := len(msg)
	for end > 0 {
		at := strings.LastIndex(msg[:end], addr)
		if at < 0 {
			return -1
		}
		after := at + len(addr)
		if after >= len(msg) || msg[after] < '0' || msg[after] > '9' {
			return at
		}
		end = at
	}
	return -1
}

// errorFamily tells an HTTP proxy's refusal from a SOCKS5 one by the words of
// the error; "" when it is neither.
func errorFamily(msg string) string {
	switch {
	case strings.Contains(msg, "socks") || strings.Contains(msg, "rejected username"):
		return "socks"
	case strings.Contains(msg, "need auth") || strings.Contains(msg, "not allowed by proxy") || strings.Contains(msg, "err code") ||
		strings.Contains(msg, "proxy authentication") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "bad gateway"):
		return "http"
	}
	return ""
}

// protoFamily maps a protocol to the family of errorFamily.
func protoFamily(proto string) string {
	switch proto {
	case "http", "https":
		return "http"
	case "socks5", "socks":
		return "socks"
	}
	return ""
}

// hopOf is attribute for a node the table knows the chain of, "" for any other.
func (t *healthTable) hopOf(label string, err error) string {
	if t == nil || err == nil {
		return ""
	}
	hs, ok := t.hops[label]
	if !ok {
		return ""
	}
	return hs.attribute(err)
}

// withHop adds the stage to the kind of an error: "timeout, post hop".
func withHop(kind, hop string) string {
	if hop == "" {
		return kind
	}
	return kind + ", " + hop
}
