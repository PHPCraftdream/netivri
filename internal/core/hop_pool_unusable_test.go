package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

const unusableHopURI = "socks5://user:secret@bücher.invalid:1080"

func TestUnusableHopIsAHardConnectFailureAndGetsQuarantined(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{unusableHopURI}, 0)
	if spec == nil || !strings.HasPrefix(spec.keys[0], "u:") {
		t.Fatal("unusable hop did not get a fallback identity")
	}
	now := time.Unix(300, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	adapter := newHopPoolAdapter("unusable", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)
	for i := 1; i <= 3; i++ {
		_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
		var staged *hopdial.StageError
		if !errors.As(err, &staged) || staged.Stage != hopdial.Connect || !errors.Is(err, errUnusableHop) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("attempt %d: err=%v, want a sanitized Connect failure", i, err)
		}
	}
	if got := poolState(p, spec.keys[0]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("unusable hop: state=%s trips=%d, want quarantined", got.State, got.Trips)
	}
}

func TestReviverStartsWithAnUnusableHopAndNeverProbesIt(t *testing.T) {
	good := newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(postHopPool, []string{good.uri, unusableHopURI}, 0)
	now := time.Unix(400, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	for range 3 {
		p.RecordHard(spec.keys[1])
	}
	lifecycle := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	targets := map[string][]hopProbeTarget{}
	for index, key := range spec.keys {
		targets[key] = []hopProbeTarget{{
			role: postHopPool, key: key, uri: spec.uris[index], direct: true,
			hopAddress: hopAddress(spec.uris[index]), protocol: "socks5",
		}}
	}
	lifecycle.targets[p] = targets
	logical := hopPoolLogicalKey{role: postHopPool, route: "route", node: 0}
	lifecycle.uses[logical] = &hopPoolUse{logical: logical, route: "route", role: postHopPool, spec: spec, pool: p, direct: true}
	reviver, err := lifecycle.newReviver(WarmOptions{ProbeInterval: time.Second, ProbeTimeout: time.Second, ProbeConcurrency: 1, ProbeURL: "http://probe.invalid:80/"})
	if err != nil {
		t.Fatalf("reviver with an unusable hop failed to start: %v", err)
	}
	now = now.Add(time.Minute)
	if err := reviver.Round(context.Background()); err != nil {
		t.Fatalf("revival round failed: %v", err)
	}
	if got := poolState(p, spec.keys[1]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("unusable hop after a round: state=%s trips=%d, want untouched quarantine", got.State, got.Trips)
	}
	if good.requests.Load() != 0 {
		t.Fatalf("healthy hop was probed %d times, want 0", good.requests.Load())
	}
}
