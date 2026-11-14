package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

func shortNegotiationBudget(t *testing.T) {
	t.Helper()
	old := hopNegotiationBudget
	hopNegotiationBudget = 200 * time.Millisecond
	t.Cleanup(func() { hopNegotiationBudget = old })
}

func TestSilentPreHopFailsWithinBudgetAndPoolMovesOn(t *testing.T) {
	shortNegotiationBudget(t)
	a, b := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(preHopPool, []string{a.uri, b.uri}, 0)
	now, seed := time.Unix(100, 0), [2]uint64{13, 21}
	first := nativeFirstIndex(spec, now, seed)
	servers := []*nativeLoopbackProxy{a, b}
	servers[first].mode.Store(nativeProxyBlock)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	adapter := newHopPoolAdapter("silent-pre", preHopPool, spec, p, nil, nil, func() time.Time { return now })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	conn, err := adapter.DialContext(ctx, &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	if err != nil || conn == nil {
		t.Fatalf("pre dial past a silent hop = (%v, %v), want success through the other hop", conn, err)
	}
	_ = conn.Close()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("silent hop held the dial for %s, want about the 200ms budget", elapsed)
	}
	silent := poolState(p, spec.keys[first])
	if silent.Sand <= 0 || silent.SoftStreak != 1 || silent.State != pool.Healthy {
		t.Fatalf("silent hop health: sand=%v streak=%d state=%s, want one soft failure", silent.Sand, silent.SoftStreak, silent.State)
	}
	if servers[1-first].targets.Load() != 1 || servers[first].targets.Load() != 0 {
		t.Fatalf("targets sent = %d/%d, want only through the answering hop", servers[first].targets.Load(), servers[1-first].targets.Load())
	}
}

func TestSilentDirectPostHopIsQuarantinedAfterTheSoftStreak(t *testing.T) {
	shortNegotiationBudget(t)
	proxy := newNativeLoopbackProxy(t, "socks5")
	proxy.mode.Store(nativeProxyBlock)
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
	now := time.Unix(200, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	adapter := newHopPoolAdapter("silent-post", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)
	for i := 1; i <= 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := adapter.DialContext(ctx, &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
		ctxErr := ctx.Err()
		cancel()
		var staged *hopdial.StageError
		if !errors.As(err, &staged) || staged.Stage != hopdial.Handshake || staged.TargetSent || ctxErr != nil {
			t.Fatalf("attempt %d: err=%v ctx=%v, want a handshake failure before the target within the budget", i, err, ctxErr)
		}
	}
	got := poolState(p, spec.keys[0])
	if got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("silent post hop after five attempts: state=%s trips=%d, want quarantined once", got.State, got.Trips)
	}
}

func TestConnectFailureClassifiesTheBudgetTimeout(t *testing.T) {
	live := context.Background()
	err := connectFailure(live, true, context.DeadlineExceeded)
	var staged *hopdial.StageError
	if !errors.As(err, &staged) || staged.Stage != hopdial.Connect || !errors.Is(err, errHopConnectTimeout) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("budget timeout with a live context = %#v, want a hard Connect failure without a context error", err)
	}
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	err = connectFailure(dead, true, context.Canceled)
	if !errors.As(err, &staged) || staged.Stage != hopdial.Unknown {
		t.Fatalf("budget timeout with a dead context = %#v, want an unattributed failure", err)
	}
	refused := errors.New("connection refused")
	err = connectFailure(live, false, refused)
	if !errors.As(err, &staged) || staged.Stage != hopdial.Connect || !errors.Is(err, refused) {
		t.Fatalf("plain connect failure = %#v, want Connect wrapping the cause", err)
	}
}

func TestConnectBudgetTimeoutIsHardAgainstTheHop(t *testing.T) {
	p := pool.New([]string{"hop"}, pool.Options{})
	hop := &budgetTimeoutHop{}
	d := hopdial.New(p, []hopdial.Hop{hop}, hopdial.Options{})
	for range 3 {
		if _, err := d.DialContext(context.Background(), "tcp", "node:443"); err == nil {
			t.Fatal("dial through a timing-out hop succeeded")
		}
	}
	if got := poolState(p, "hop"); got.State != pool.Quarantined || got.HardFailures != 3 {
		t.Fatalf("hop after three connect budget timeouts: state=%s hard=%d, want quarantined", got.State, got.HardFailures)
	}
}

type budgetTimeoutHop struct{}

func (budgetTimeoutHop) Key() string { return "hop" }
func (budgetTimeoutHop) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return nil, connectFailure(ctx, true, context.DeadlineExceeded)
}
