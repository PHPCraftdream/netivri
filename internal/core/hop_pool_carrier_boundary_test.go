package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

type boundaryFailingCarrier struct {
	nativeFailingCarrier
	calls atomic.Int32
}

func (m *boundaryFailingCarrier) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	m.calls.Add(1)
	return nil, m.err
}

func TestPostCarrierStageErrorsStayOutsidePostHopClassification(t *testing.T) {
	cases := []struct {
		name       string
		stage      hopdial.Stage
		next       bool
		targetSent bool
		marker     bool
	}{
		{name: "connect", stage: hopdial.Connect},
		{name: "handshake", stage: hopdial.Handshake},
		{name: "target", stage: hopdial.Target, next: true, targetSent: true},
		{name: "refusal", stage: hopdial.Refusal, targetSent: true},
		{name: "nested pre-pool marker", stage: hopdial.Connect, marker: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1200, 0)
			spec := newHopPoolSpec(postHopPool, []string{
				"http://first.invalid:8080", "http://second.invalid:8080",
			}, 0)
			p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
			binder := &testSiteBinder{values: map[string]string{"alice\x00example.test": spec.keys[0]}}
			health := newHealthTable()
			health.register([]string{"node"})
			carrierHealth := health.health("node")
			carrierHealth.recordProbe(true, time.Millisecond)

			sentinel := errors.New("carrier failure sentinel")
			cause := error(&hopdial.StageError{
				Stage: tc.stage, NextUnreachable: tc.next,
				TargetSent: tc.targetSent, Err: sentinel,
			})
			if tc.marker {
				cause = &hopPoolDialError{cause: cause}
			}
			carrier := &boundaryFailingCarrier{
				nativeFailingCarrier: nativeFailingCarrier{name: "node", err: cause},
			}
			adapter := newHopPoolAdapter("post-carrier-boundary", postHopPool, spec, p, binder, carrierHealth, func() time.Time { return now })
			setNativeCarrier(adapter, carrier, false)
			group := newStickyGroupMembers("route", []member{adapter}, []string{"node"}, newAffinityTable(nil, time.Minute), health, nil)

			_, err := group.DialContext(context.Background(), &C.Metadata{
				NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443,
			})
			if err == nil || !errors.Is(err, sentinel) {
				t.Fatalf("carrier cause was not retained: %v", err)
			}
			var carrierErr *poolCarrierError
			if !errors.As(err, &carrierErr) {
				t.Fatalf("carrier classification was not retained: %v", err)
			}
			if got := isHopPoolDialError(err); got != tc.marker {
				t.Fatalf("hop-pool marker = %t, want %t", got, tc.marker)
			}
			if got := carrier.calls.Load(); got != 1 {
				t.Fatalf("carrier acquisition calls = %d, want exactly one", got)
			}
			for _, key := range spec.keys {
				if got := poolState(p, key); got.HardFailures != 0 || got.Sand != 0 {
					t.Fatalf("carrier failure changed post-hop health for %q: hard=%d soft=%v", key, got.HardFailures, got.Sand)
				}
			}
			if got := carrierHealth.consDialFail; tc.marker && got != 0 {
				t.Fatalf("nested pre-pool marker changed node health: failures=%d", got)
			} else if !tc.marker && got == 0 {
				t.Fatal("carrier transport evidence was not recorded against the node")
			}
		})
	}
}

func TestPostTransientCarrierFailuresDoNotChangeNodeOrHopHealth(t *testing.T) {
	wrappedCapacity := errors.New("capacity reached")
	joinedCapacity := errors.New("joined capacity cause")
	genuineCarrierCause := errors.New("carrier refused connection")
	cases := []struct {
		name      string
		err       error
		cause     error
		transient bool
	}{
		{name: "canceled", err: context.Canceled, cause: context.Canceled, transient: true},
		{name: "wrapped canceled", err: fmt.Errorf("carrier wait: %w", context.Canceled), cause: context.Canceled, transient: true},
		{name: "deadline", err: context.DeadlineExceeded, cause: context.DeadlineExceeded, transient: true},
		{name: "wrapped capacity", err: fmt.Errorf("carrier: %w", wrappedCapacity), cause: wrappedCapacity, transient: true},
		{name: "joined capacity", err: errors.Join(errors.New("capacity reached"), joinedCapacity), cause: joinedCapacity, transient: true},
		{name: "genuine carrier failure", err: genuineCarrierCause, cause: genuineCarrierCause, transient: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1400, 0)
			spec := newHopPoolSpec(postHopPool, []string{"http://first.invalid:8080", "http://second.invalid:8080"}, 0)
			p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
			health := newHealthTable()
			health.register([]string{"node"})
			carrierHealth := health.health("node")
			carrierHealth.recordProbe(true, time.Millisecond)
			beforeOK := carrierHealth.lastOKAt()
			beforeRing := carrierHealth.ringLen
			beforeDown := carrierHealth.Down()
			beforePool := p.Snapshot()
			binder := &testSiteBinder{values: map[string]string{"alice\x00example.test": spec.keys[0]}}

			carrier := &boundaryFailingCarrier{
				nativeFailingCarrier: nativeFailingCarrier{name: "node", err: tc.err},
			}
			adapter := newHopPoolAdapter("post-carrier-transient", postHopPool, spec, p, binder, carrierHealth, func() time.Time { return now })
			setNativeCarrier(adapter, carrier, false)
			group := newStickyGroupMembers("route", []member{adapter}, []string{"node"}, newAffinityTable(nil, time.Minute), health, nil)

			_, err := group.DialContext(context.Background(), &C.Metadata{
				NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443,
			})
			if err == nil || !errors.Is(err, tc.cause) {
				t.Fatalf("carrier cause was not retained: %v", err)
			}
			if got := isHopPoolDialError(err); got != tc.transient {
				t.Fatalf("hop-pool marker = %t, want %t", got, tc.transient)
			}
			var poolDialErr *hopPoolDialError
			if errors.As(err, &poolDialErr) != tc.transient {
				t.Fatalf("hop-pool wrapper presence disagrees with transient=%t: %v", tc.transient, err)
			}
			var carrierErr *poolCarrierError
			if !errors.As(err, &carrierErr) {
				t.Fatalf("carrier cause classification not retained: %v", err)
			}
			if got := carrier.calls.Load(); got != 1 {
				t.Fatalf("carrier calls = %d, want one", got)
			}
			if !reflect.DeepEqual(p.Snapshot(), beforePool) {
				t.Fatalf("carrier failure changed post-hop pool health: before=%#v after=%#v", beforePool, p.Snapshot())
			}
			if !tc.transient {
				if carrierHealth.consDialFail == 0 || carrierHealth.ringLen <= beforeRing {
					t.Fatal("genuine carrier failure was not recorded against the node")
				}
				return
			}
			if carrierHealth.consDialFail != 0 || carrierHealth.dialSites != 0 || carrierHealth.Down() != beforeDown || carrierHealth.ringLen != beforeRing || !carrierHealth.lastOKAt().Equal(beforeOK) {
				t.Fatalf("transient carrier failure changed node health: failures=%d sites=%d ring=%d down=%t lastOK=%v", carrierHealth.consDialFail, carrierHealth.dialSites, carrierHealth.ringLen, carrierHealth.Down(), carrierHealth.lastOKAt())
			}
		})
	}
}

var _ member = (*boundaryFailingCarrier)(nil)
