package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
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

func TestNativePostRefusalIsSoftForTheRefusingHop(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "http")
	proxy.status.Store(407)
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
	now := time.Unix(100, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	adapter := newHopPoolAdapter("post", postHopPool, spec, p, nil, carrier, func() time.Time { return now })
	for _, hop := range adapter.hops {
		hop.directCarrier = true
	}
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	var staged *dial.StageError
	if !errors.As(err, &staged) || staged == nil {
		t.Fatal("native HTTP refusal did not produce a staged error")
	}
	if staged.Stage != dial.Refusal || !staged.TargetSent {
		t.Fatalf("native HTTP refusal stage = %v, target sent = %t", staged.Stage, staged.TargetSent)
	}
	got := poolState(p, spec.keys[0])
	if got.HardFailures != 0 || got.Sand != 1 || got.State != pool.Healthy {
		t.Fatalf("HTTP refusal did not record only a soft current-hop failure: hard=%d soft=%v state=%s", got.HardFailures, got.Sand, got.State)
	}
}

func TestPostProbeUsesTypedAttributionAndRequiresEvidence(t *testing.T) {
	target := hopProbeTarget{role: postHopPool, uri: "http://post.invalid:8080", hopAddress: "post.invalid:8080", protocol: "http", nodeAddress: "node.invalid:443", nodeProtocol: "http"}
	cases := []struct {
		name string
		err  error
		want pool.ProbeOutcome
	}{
		{"connect", &dial.StageError{Stage: dial.Connect}, pool.ProbeHardFailure},
		{"handshake", &dial.StageError{Stage: dial.Handshake}, pool.ProbeHardFailure},
		{"target", &dial.StageError{Stage: dial.Target, NextUnreachable: true, TargetSent: true}, pool.ProbeInconclusive},
		{"refusal", &dial.StageError{Stage: dial.Refusal, TargetSent: true}, pool.ProbeInconclusive},
		{"unknown", errors.New("unclassified failure"), pool.ProbeInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProbe(target, tc.err); got != tc.want {
				t.Fatalf("typed post probe outcome = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDirectPostUsesNativeTunnelWithoutNodeEvidence(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "http")
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
	now := time.Unix(200, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	a := newHopPoolAdapter("direct-post", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	for _, hop := range a.hops {
		hop.directCarrier = true
	}
	conn, err := a.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err != nil {
		t.Fatalf("direct carrier incorrectly required node evidence: %v", err)
	}
	_ = conn.Close()
	if got := poolState(p, spec.keys[0]); got.HardFailures != 0 || got.Sand != 0 {
		t.Fatalf("successful direct carrier changed hop health: hard=%d soft=%v", got.HardFailures, got.Sand)
	}
}
