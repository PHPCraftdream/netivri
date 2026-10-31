package core

import (
	"context"
	"net"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// probeNow makes a round probe every node at once: a node that is down is not
// due for minutes, but after the computer slept its schedule means nothing.
func TestProbeNowProbesEveryNodeAtOnce(t *testing.T) {
	ticks := make(chan time.Time)
	clock := runWarmClock{testWarmClock{now: time.Unix(100, 0)}, &runWarmTicker{ticks: ticks}}
	w := &warmer{done: make(chan struct{}), kick: make(chan struct{}, 1),
		options: WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: clock}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-down", typ: C.Socks5}, testWarmProxy{name: "netivri-up", typ: C.Socks5}}
	w.down = func(name string) bool { return name == "netivri-down" }
	probed := make(chan string, 16)
	w.probe = func(_ context.Context, p C.Proxy, _ string) (uint16, error) {
		probed <- p.Name()
		return 0, net.ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
	t.Cleanup(w.Close)
	wait := func(what string) {
		t.Helper()
		got := map[string]bool{}
		for len(got) < 2 {
			select {
			case name := <-probed:
				got[name] = true
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: only %v probed", what, got)
			}
		}
	}
	wait("first round")
	// The down node is now waiting out its interval: without probeNow nothing
	// is probed until the ticker fires, which here never happens.
	select {
	case name := <-probed:
		t.Fatalf("%s probed before probeNow", name)
	case <-time.After(50 * time.Millisecond):
	}
	w.probeNow()
	wait("after probeNow")
}

func TestProbeNowIsSafeWithoutAWarmer(t *testing.T) {
	var w *warmer
	w.probeNow()
	(&warmer{}).probeNow() // no kick channel: nothing to wake, nothing to block on
}
