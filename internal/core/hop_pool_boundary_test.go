package core

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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

func TestPostProbeUsesLifecycleAndRequiresFreshCarrierEvidence(t *testing.T) {
	t.Run("fresh carrier probes configured URL and succeeds", func(t *testing.T) {
		proxy := newNativeLoopbackProxy(t, "socks5")
		now := time.Unix(1000, 0)
		health := &nodeHealth{now: func() time.Time { return now }}
		health.recordProbe(true, time.Millisecond)
		carrier := &countedNativeCarrier{nativeMemberCarrier: *nativeCarrierForLoopback(t, proxy)}
		target := hopProbeTarget{role: postHopPool, uri: proxy.uri, hopAddress: hopAddress(proxy.uri), protocol: "socks5", carrier: health, carrierMember: carrier, nodeAddress: "127.0.0.1:1"}
		outcome := runNativeLifecyclePostProbe(t, &now, target, "http://probe.example.test:7777/path")
		if outcome != pool.ProbeSuccess || proxy.requests.Load() != 1 || carrier.calls.Load() != 1 {
			t.Fatalf("fresh-evidence post probe outcome=%v proxy requests=%d carrier attempts=%d", outcome, proxy.requests.Load(), carrier.calls.Load())
		}
		if got := proxy.requestedTarget(); got != "probe.example.test:7777" {
			t.Fatalf("post probe target=%q, want configured ProbeURL host and port", got)
		}
	})

	for _, tc := range []struct {
		name        string
		evidence    string
		rep         int32
		advance     bool
		wantCalls   int32
		wantOutcome pool.ProbeOutcome
	}{
		{name: "stale evidence", evidence: "stale", wantCalls: 0, wantOutcome: pool.ProbeInconclusive},
		{name: "down carrier", evidence: "down", wantCalls: 0, wantOutcome: pool.ProbeInconclusive},
		{name: "evidence expires during dial", evidence: "fresh", advance: true, wantCalls: 1, wantOutcome: pool.ProbeInconclusive},
		{name: "handshake failure", evidence: "fresh", wantCalls: 1, wantOutcome: pool.ProbeHardFailure},
		{name: "refusal is hard", evidence: "fresh", rep: 2, wantCalls: 1, wantOutcome: pool.ProbeHardFailure},
		{name: "target", evidence: "fresh", rep: 3, wantCalls: 1, wantOutcome: pool.ProbeInconclusive},
		{name: "other", evidence: "fresh", rep: 7, wantCalls: 1, wantOutcome: pool.ProbeInconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newNativeLoopbackProxy(t, "socks5")
			if tc.rep != 0 {
				proxy.mode.Store(nativeProxyREP)
				proxy.rep.Store(tc.rep)
			} else {
				proxy.mode.Store(nativeProxyBadGreeting)
			}
			now := time.Unix(2000, 0)
			health := &nodeHealth{now: func() time.Time { return now }}
			health.recordProbe(true, time.Millisecond)
			switch tc.evidence {
			case "stale":
				now = now.Add(trafficAliveFor + time.Second)
			case "down":
				health.downFlag.Store(true)
			}
			carrier := &countedNativeCarrier{nativeMemberCarrier: *nativeCarrierForLoopback(t, proxy), clock: &now}
			if tc.advance {
				carrier.advance = trafficAliveFor + time.Second
			}
			target := hopProbeTarget{role: postHopPool, uri: proxy.uri, hopAddress: hopAddress(proxy.uri), protocol: "socks5", carrier: health, carrierMember: carrier, nodeAddress: "127.0.0.1:1"}
			outcome := runNativeLifecyclePostProbe(t, &now, target, "http://probe.invalid:8080/")
			if outcome != tc.wantOutcome {
				t.Fatalf("post probe outcome=%v, want %v", outcome, tc.wantOutcome)
			}
			if got := carrier.calls.Load(); got != tc.wantCalls {
				t.Fatalf("carrier attempts=%d, want %d", got, tc.wantCalls)
			}
			if got := proxy.requests.Load(); got != tc.wantCalls {
				t.Fatalf("post proxy requests=%d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func runNativeLifecyclePostProbe(t *testing.T, now *time.Time, target hopProbeTarget, probeURL string) pool.ProbeOutcome {
	t.Helper()
	lifecycle := newHopPoolLifecycleWithClock(nil, func() time.Time { return *now })
	spec := newHopPoolSpec(postHopPool, []string{target.uri}, 0)
	key := spec.keys[0]
	target.key = key
	p := pool.New([]string{key}, pool.Options{Clock: func() time.Time { return *now }})
	lifecycle.targets[p] = map[string][]hopProbeTarget{key: {target}}
	prober := lifecyclePoolProber{lifecycle: lifecycle, pool: p, options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: probeURL}}
	return prober.Probe(context.Background(), &mihomoPoolHop{key: key})
}

type countedNativeCarrier struct {
	nativeMemberCarrier
	calls   atomic.Int32
	clock   *time.Time
	advance time.Duration
}

func (m *countedNativeCarrier) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	m.calls.Add(1)
	if m.clock != nil && m.advance != 0 {
		*m.clock = m.clock.Add(m.advance)
	}
	return m.nativeMemberCarrier.DialContext(ctx, metadata)
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
