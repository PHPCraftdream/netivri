package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

func TestWrappedHopPoolMarkerSkipsNodeDialHealth(t *testing.T) {
	health := newHealthTable()
	health.register([]string{"node"})
	group := newStickyGroupMembers("route", []member{&fakeMember{
		name: "node", err: fmt.Errorf("node.invalid:443 connect error: %s", (&hopPoolDialError{}).Error()),
	}}, []string{"node"}, newAffinityTable(nil, time.Minute), health, nil)
	_, err := group.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) {
		t.Fatalf("wrapped pool marker not preserved: %v", err)
	}
	h := health.health("node")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.consDialFail != 0 || h.dialSites != 0 {
		t.Fatalf("wrapped pool failure counted against carrier node: failures=%d sites=%d", h.consDialFail, h.dialSites)
	}
}

func TestMemberProbeKeysSeparateBaseAndPostProbePaths(t *testing.T) {
	node := map[string]any{"name": "node", "type": "ss", "server": "node.invalid", "port": 443, "password": "test"}
	options := Options{
		RouteNodes:   map[string][]map[string]any{"route": {node}},
		NodePrePool:  map[string][][]string{"route": {{"socks5://one.invalid:1080", "socks5://two.invalid:1080"}}},
		NodePostPool: map[string][][]string{"route": {{"http://post.invalid:8080"}}},
	}
	postName := memberProxyName(0, 0, 0) + "-post"
	keys := memberProbeKeys([]routePlan{{key: "route", names: []string{postName}}}, options)
	if keys[postName] == "" || keys[memberProxyName(0, 0, 0)] == "" || keys[postName] == keys[memberProxyName(0, 0, 0)] {
		t.Fatal("base probe and outer post probe unexpectedly share an identity")
	}

	options.NodePostPool = nil
	options.NodePrePool = nil
	baseName := memberProxyName(0, 0, 0)
	keys = memberProbeKeys([]routePlan{{key: "route", names: []string{baseName}}}, options)
	if keys[baseName] == "" || keys[baseName] != probeKey(node, "", "") {
		t.Fatal("legacy no-post probe key changed")
	}
	options.RouteNodes["other"] = []map[string]any{node}
	keys = memberProbeKeys([]routePlan{{key: "route", names: []string{"first"}}, {key: "other", names: []string{"second"}}}, options)
	if keys["first"] == "" || keys["first"] != keys["second"] {
		t.Fatal("duplicate no-post route probes did not share identity")
	}
}

func TestSameFamilyPostCarrierRefusalIsNotAttributedToEitherHop(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{"http://post.invalid:8080"}, 0)
	now := time.Unix(100, 0)
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	adapter := newHopPoolAdapter("post", postHopPool, spec, map[string]member{
		spec.keys[0]: &fakeMember{name: "post", err: errors.New("post.invalid:8080 connect error: http need auth")},
	}, p, nil, carrier, func() time.Time { return now })
	adapter.hops[0].nextAddress = "node.invalid:443"
	adapter.hops[0].nextProtocol = "http"
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) {
		t.Fatalf("ambiguous refusal was not safely returned: %v", err)
	}
	if got := poolState(p, spec.keys[0]); got.HardFailures != 0 || got.Sand != 0 {
		t.Fatalf("same-family refusal changed post-hop health: %#v", got)
	}
}

func TestPostProbeUsesCarrierAttributionBeforeGenericClassification(t *testing.T) {
	target := hopProbeTarget{
		role: postHopPool, hopAddress: "post.invalid:8080", protocol: "socks5",
		nodeAddress: "node.invalid:443", nodeProtocol: "http",
	}
	if got := classifyProbe(target, errors.New("post.invalid:8080 connect error: 502 bad gateway")); got != pool.ProbeHardFailure {
		t.Fatalf("HTTP failure from carrier was not a hard post failure: %v", got)
	}
	target.protocol, target.nodeProtocol = "http", "http"
	if got := classifyProbe(target, errors.New("post.invalid:8080 connect error: http need auth")); got != pool.ProbeInconclusive {
		t.Fatalf("same-family refusal was not inconclusive: %v", got)
	}
	target.protocol, target.nodeProtocol = "socks5", "http"
	if got := classifyProbe(target, errors.New("node.invalid:443 connect error: socks error: 5")); got != pool.ProbeInconclusive {
		t.Fatalf("node-inner failure was attributed to post hop: %v", got)
	}
}

func TestPostCarrierFailureDoesNotTreatDirectCarrierAsNode(t *testing.T) {
	now := time.Unix(200, 0)
	spec := newHopPoolSpec(postHopPool, []string{"http://post.invalid:8080"}, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	a := newHopPoolAdapter("direct-post", postHopPool, spec, map[string]member{spec.keys[0]: &fakeMember{name: "post"}}, p, nil, nil, func() time.Time { return now })
	a.hops[0].directCarrier = true
	if _, err := a.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443}); err != nil {
		t.Fatalf("direct carrier incorrectly required node evidence: %v", err)
	}
	if got := poolState(p, spec.keys[0]); got.HardFailures != 0 {
		t.Fatalf("successful direct carrier changed hop health: %#v", got)
	}
}
