package core

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

func TestNativePostStaleEvidenceHandshakeRetriesBoundHopHealth(t *testing.T) {
	firstProxy, secondProxy := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	firstProxy.mode.Store(nativeProxyBadGreeting)
	spec := newHopPoolSpec(postHopPool, []string{firstProxy.uri, secondProxy.uri}, 0)
	now := time.Unix(3000, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	carrierHealth := &nodeHealth{now: func() time.Time { return now }}
	carrierHealth.recordProbe(true, time.Millisecond)
	now = now.Add(trafficAliveFor + time.Second)
	carrier := &nativeMemberCarrier{
		name: "node",
		routes: map[string]string{
			nativeProxyAddress(t, firstProxy):  firstProxy.listener.Addr().String(),
			nativeProxyAddress(t, secondProxy): secondProxy.listener.Addr().String(),
		},
	}
	binder := &testSiteBinder{values: map[string]string{"fixture-user\x00example.test": spec.keys[0]}}
	adapter := newHopPoolAdapter("native-stale-retry", postHopPool, spec, p, binder, carrierHealth, func() time.Time { return now })
	setNativeCarrier(adapter, carrier, false)
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "www.example.test", DstPort: 443}

	conn, err := adapter.DialContext(context.Background(), metadata)
	if err != nil || conn == nil {
		t.Fatalf("stale-evidence handshake failure did not retry to the healthy post hop: %v", err)
	}
	_ = conn.Close()

	if firstProxy.requests.Load() != 1 || secondProxy.requests.Load() != 1 {
		t.Fatalf("native endpoint attempts=%d/%d, want exactly one each", firstProxy.requests.Load(), secondProxy.requests.Load())
	}
	bound, ok := binder.Get("fixture-user", "example.test")
	if !ok || bound != spec.keys[1] {
		t.Fatal("successful retry did not replace the failed post-hop binding")
	}
	firstState, secondState := poolState(p, spec.keys[0]), poolState(p, spec.keys[1])
	if firstState.HardFailures != 0 || firstState.Sand != 0 || firstState.Trips != 0 || firstState.State != pool.Healthy {
		t.Fatalf("stale carrier evidence was attributed to the first post hop: hard=%d sand=%v trips=%d state=%s", firstState.HardFailures, firstState.Sand, firstState.Trips, firstState.State)
	}
	if secondState.HardFailures != 0 || secondState.Sand != 0 || secondState.Trips != 0 || secondState.State != pool.Healthy {
		t.Fatal("successful second post hop changed health")
	}
}

func TestNativePostConnectQuarantineAndLifecycleHandshakeBackoffAreBounded(t *testing.T) {
	now := time.Unix(4000, 0)
	connectListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("TLS fixture listener failed")
	}
	t.Cleanup(func() { _ = connectListener.Close() })
	go func() {
		for {
			conn, acceptErr := connectListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				_, _ = io.WriteString(conn, "not tls")
			}()
		}
	}()
	connectPort := connectListener.Addr().(*net.TCPAddr).Port
	connectURI := "https://localhost:" + strconv.Itoa(connectPort)
	connectSpec := newHopPoolSpec(postHopPool, []string{connectURI}, 0)
	p := pool.New(connectSpec.keys, pool.Options{Clock: func() time.Time { return now }})
	carrierHealth := &nodeHealth{now: func() time.Time { return now }}
	carrierHealth.recordProbe(true, time.Millisecond)
	ordinary := newHopPoolAdapter("native-hard-connect", postHopPool, connectSpec, p, nil, carrierHealth, func() time.Time { return now })
	setNativeCarrier(ordinary, &nativeMemberCarrier{
		name:   "node",
		routes: map[string]string{net.JoinHostPort("localhost", strconv.Itoa(connectPort)): connectListener.Addr().String()},
	}, false)
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "site.invalid", DstPort: 443}
	for range 3 {
		_, dialErr := ordinary.DialContext(context.Background(), metadata)
		var staged *hopdial.StageError
		if dialErr == nil || !errors.As(dialErr, &staged) || staged.Stage != hopdial.Connect {
			t.Fatal("native HTTPS TLS setup failure was not classified as a hard-attribution Connect error")
		}
	}
	key := connectSpec.keys[0]
	state := poolState(p, key)
	if state.State != pool.Quarantined || state.HardFailures != 3 || state.Trips != 1 || !state.NextProbe.Equal(now.Add(30*time.Second)) {
		t.Fatalf("native TLS Connect hard failures did not trip at the threshold: hard=%d trips=%d state=%s", state.HardFailures, state.Trips, state.State)
	}
	for range 3 {
		if _, dialErr := ordinary.DialContext(context.Background(), metadata); dialErr == nil {
			t.Fatal("quarantined native TLS setup unexpectedly succeeded")
		}
		if got := poolState(p, key); got.HardFailures != 3 || got.Trips != 1 || !got.NextProbe.Equal(state.NextProbe) {
			t.Fatal("ordinary attempts while quarantined grew hard count or trip")
		}
	}

	// Revival uses check.New, where a malformed SOCKS greeting is a hard
	// Handshake probe. Ordinary post dials intentionally map ambiguous
	// Handshake failures to SOFT; these are separate evidence paths.
	probeProxy := newNativeLoopbackProxy(t, "socks5")
	probeProxy.mode.Store(nativeProxyBadGreeting)
	probeSpec := newHopPoolSpec(postHopPool, []string{probeProxy.uri}, 0)
	probeKey := probeSpec.keys[0]
	probePool := pool.New([]string{probeKey}, pool.Options{Clock: func() time.Time { return now }})
	for range 3 {
		probePool.RecordHard(probeKey)
	}
	lifecycle := newHopPoolLifecycleWithClock(nil, func() time.Time { return now })
	probeTarget := hopProbeTarget{
		role: postHopPool, key: probeKey, uri: probeProxy.uri,
		hopAddress: hopAddress(probeProxy.uri), protocol: "socks5", direct: true,
	}
	lifecycle.targets[probePool] = map[string][]hopProbeTarget{probeKey: {probeTarget}}
	lifecycle.uses[hopPoolLogicalKey{role: postHopPool, route: "route", node: 0}] = &hopPoolUse{
		logical: hopPoolLogicalKey{role: postHopPool, route: "route", node: 0}, route: "route",
		role: postHopPool, spec: probeSpec, pool: probePool,
	}
	reviver, err := lifecycle.newReviver(WarmOptions{ProbeInterval: time.Second, ProbeTimeout: time.Second, ProbeConcurrency: 1, ProbeURL: "http://probe.invalid:80/"})
	if err != nil {
		t.Fatal("native lifecycle reviver setup failed")
	}
	backoffs := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 600 * time.Second, 600 * time.Second}
	for index, wantBackoff := range backoffs {
		before := poolState(probePool, probeKey)
		now = before.NextProbe
		if roundErr := reviver.Round(context.Background()); roundErr != nil {
			t.Fatalf("native lifecycle probe round %d failed", index+1)
		}
		got := poolState(probePool, probeKey)
		if got.State != pool.Quarantined || got.HardFailures != 3 || got.Trips != index+2 || !got.NextProbe.Equal(now.Add(wantBackoff)) {
			t.Fatalf("native handshake revival trip %d: hard=%d trips=%d state=%s backoff=%s", index+2, got.HardFailures, got.Trips, got.State, got.NextProbe.Sub(now))
		}
		if probeProxy.requests.Load() != int32(index+1) {
			t.Fatalf("native lifecycle probe attempts=%d after trip %d, want %d", probeProxy.requests.Load(), index+2, index+1)
		}
	}
}
