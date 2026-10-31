package core

import (
	"errors"
	"testing"
	"time"
)

var chainSet = hopSet{
	pre:  hopInfo{addr: "pre.example:8080", proto: "http"},
	node: hopInfo{addr: "node.example:443", proto: "vless"},
	post: hopInfo{addr: "post.example:1080", proto: "socks5"},
}

func TestHopAttributionByTheAddressNamedLast(t *testing.T) {
	for _, c := range []struct {
		name string
		hops hopSet
		err  string
		want string
	}{
		{"pre unreachable, wrapped by the node", chainSet,
			"node.example:443 connect error: pre.example:8080 connect error: dial tcp 10.0.0.1:8080: i/o timeout", HopPre},
		{"node unreachable, wrapped by the post hop", chainSet,
			"post.example:1080 connect error: node.example:443 connect error: dial tcp 10.0.0.2:443: connect: connection refused", HopNode},
		{"post hop unreachable", chainSet,
			"post.example:1080 connect error: dial tcp 10.0.0.3:1080: i/o timeout", HopPost},
		{"upper case in the error", chainSet, "POST.EXAMPLE:1080 connect error: boom", HopPost},
		{"a longer port is another address", chainSet, "post.example:10800 connect error: boom", HopUnknown},
		{"the address of a stage twice: the last one counts", chainSet,
			"pre.example:8080 connect error: x; node.example:443 connect error: y", HopNode},
		{"a hop given by IPv6 address", hopSet{pre: hopInfo{addr: "[::1]:8080", proto: "http"}, node: hopInfo{addr: "node.example:443"}},
			"[::1]:8080 connect error: dial tcp [::1]:8080: refused", HopPre},
	} {
		if got := c.hops.attribute(errors.New(c.err)); got != c.want {
			t.Errorf("%s: attribute = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHopAttributionByTheProtocolOfAnAddresslessRefusal(t *testing.T) {
	for _, c := range []struct {
		name string
		hops hopSet
		err  string
		want string
	}{
		{"an HTTP proxy that wants credentials: the one http stage", chainSet, "HTTP need auth", HopPre},
		{"a SOCKS5 proxy that refuses the password: the one socks stage", chainSet, "rejected username/password", HopPost},
		{"SOCKS need auth", chainSet, "SOCKS need auth", HopPost},
		{"a gateway error of an HTTP hop", chainSet, "502 Bad Gateway", HopPre},
		{"two SOCKS5 stages cannot be told apart",
			hopSet{pre: hopInfo{addr: "a:1", proto: "socks5"}, node: hopInfo{addr: "n:2", proto: "ss"}, post: hopInfo{addr: "b:3", proto: "socks5"}},
			"rejected username/password", HopUnknown},
		{"no stage speaks the protocol of the error",
			hopSet{node: hopInfo{addr: "n:2", proto: "ss"}, post: hopInfo{addr: "b:3", proto: "socks5"}}, "HTTP need auth", HopUnknown},
		{"nothing to go by", chainSet, "something odd happened", HopUnknown},
		{"an https hop counts as http", hopSet{node: hopInfo{addr: "n:2", proto: "ss"}, post: hopInfo{addr: "b:3", proto: "https"}}, "HTTP need auth", HopPost},
	} {
		if got := c.hops.attribute(errors.New(c.err)); got != c.want {
			t.Errorf("%s: attribute = %q, want %q", c.name, got, c.want)
		}
	}
}

// A route that is only a node has nothing to tell apart.
func TestHopAttributionNeedsAChain(t *testing.T) {
	onlyNode := hopSet{node: hopInfo{addr: "node.example:443", proto: "vless"}}
	if got := onlyNode.attribute(errors.New("node.example:443 connect error: boom")); got != "" {
		t.Errorf("a route of only a node: %q, want no stage", got)
	}
	if got := chainSet.attribute(nil); got != "" {
		t.Errorf("no error: %q", got)
	}
	if (hopSet{}).chained() || !chainSet.chained() || !(hopSet{post: hopInfo{addr: "p:1"}}).chained() {
		t.Error("chained() is wrong")
	}
}

func TestHopInfoOfChainURLsAndNodes(t *testing.T) {
	for raw, want := range map[string]hopInfo{
		"":                                     {},
		"none":                                 {},
		"NONE":                                 {},
		"::bad":                                {},
		"socks5://u:p%40ss@Proxy.Example:1080": {addr: "proxy.example:1080", proto: "socks5"},
		"HTTP://proxy.example:8080":            {addr: "proxy.example:8080", proto: "http"},
		"https://[::1]:8443":                   {addr: "[::1]:8443", proto: "https"},
	} {
		if got := chainHop(raw); got != want {
			t.Errorf("chainHop(%q) = %+v, want %+v", raw, got, want)
		}
	}
	for _, c := range []struct {
		node map[string]any
		want hopInfo
	}{
		{map[string]any{"type": "VLESS", "server": "Node.Example", "port": 443}, hopInfo{addr: "node.example:443", proto: "vless"}},
		{map[string]any{"type": "ss", "server": "1.2.3.4", "port": "8388"}, hopInfo{addr: "1.2.3.4:8388", proto: "ss"}},
		{map[string]any{"type": "trojan", "server": "::1", "port": 443}, hopInfo{addr: "[::1]:443", proto: "trojan"}},
		{map[string]any{"type": "ss", "server": "1.2.3.4"}, hopInfo{}},
		{map[string]any{"type": "ss", "port": 1}, hopInfo{}},
	} {
		if got := nodeHop(c.node); got != c.want {
			t.Errorf("nodeHop(%v) = %+v, want %+v", c.node, got, c.want)
		}
	}
	nodes := []map[string]any{{"type": "ss", "server": "a", "port": 1}, {"type": "ss", "server": "b", "port": 2}}
	hops := routeHops(nodes, "http://pre:1", "none")
	if len(hops) != 2 || hops[1].node.addr != "b:2" || hops[1].pre.addr != "pre:1" || hops[1].post.addr != "" {
		t.Errorf("routeHops = %+v", hops)
	}
	if withHop("timeout", "") != "timeout" || withHop("timeout", HopPost) != "timeout, post hop" {
		t.Error("withHop")
	}
}

// The plans carry the chain of every member, in the order of the members.
func TestRoutePlansCarryTheChainOfEveryMember(t *testing.T) {
	o := Options{
		AccountRoute: map[string]string{"alice": "de"},
		RouteNodes:   map[string][]map[string]any{"de": {{"type": "ss", "server": "a.example", "port": 1}, {"type": "ss", "server": "b.example", "port": 2}}},
		RoutePre:     map[string]string{"de": "http://pre.example:8080"},
		RoutePost:    map[string]string{"de": "socks5://post.example:1080"},
	}
	plans, err := buildRoutePlans(o)
	if err != nil || len(plans) != 1 || len(plans[0].hops) != 2 {
		t.Fatalf("plans = %+v, err %v", plans, err)
	}
	want := hopSet{pre: hopInfo{addr: "pre.example:8080", proto: "http"}, node: hopInfo{addr: "b.example:2", proto: "ss"}, post: hopInfo{addr: "post.example:1080", proto: "socks5"}}
	if plans[0].hops[1] != want {
		t.Errorf("hops[1] = %+v, want %+v", plans[0].hops[1], want)
	}
}

// The stage is in the reason of a node going down, for dials and for probes, and
// only for a node that has a chain.
func TestDownReasonNamesTheStage(t *testing.T) {
	var events []NodeEvent
	table := newHealthTable()
	table.notify = func(ev NodeEvent) { events = append(events, ev) }
	table.register([]string{"p/dial", "p/probe", "p/plain"})
	table.hops = map[string]hopSet{"p/dial": chainSet, "p/probe": chainSet}
	for _, label := range []string{"p/dial", "p/probe", "p/plain"} {
		table.recordDial(label, true, time.Millisecond) // each has answered once, so a failure is a real down
	}

	postErr := errors.New("post.example:1080 connect error: dial tcp 10.0.0.3:1080: i/o timeout")
	table.recordDialAt("p/dial", "site-a", postErr, time.Second)
	table.recordDialAt("p/dial", "site-b", postErr, time.Second)
	table.recordDialAt("p/dial", "site-a", postErr, time.Second)
	if len(events) != 1 || events[0].Reason != "3 dial failures (timeout, post hop)" {
		t.Fatalf("dials: events = %+v, want one down with the stage", events)
	}

	events = nil
	preErr := errors.New("node.example:443 connect error: pre.example:8080 connect error: dial tcp 10.0.0.1:8080: i/o timeout")
	table.noteProbeError("p/probe", preErr)
	table.recordProbe("p/probe", false, 0)
	table.recordProbe("p/probe", false, 0)
	if len(events) != 1 || events[0].Reason != "2 probe failures (timeout, pre hop)" {
		t.Fatalf("probes: events = %+v, want a down at the pre hop", events)
	}

	events = nil
	table.noteProbeError("p/plain", preErr)
	table.recordProbe("p/plain", false, 0)
	table.recordProbe("p/plain", false, 0)
	if len(events) != 1 || events[0].Reason != "2 probe failures (timeout)" {
		t.Fatalf("a node without a chain: events = %+v, want no stage", events)
	}

	if got := table.hopOf("p/unknown", preErr); got != "" {
		t.Errorf("a node the table does not know: %q", got)
	}
	var nilTable *healthTable
	if got := nilTable.hopOf("p/dial", preErr); got != "" {
		t.Errorf("a nil table: %q", got)
	}
}
