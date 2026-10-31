package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// healthClock is a settable clock for a health table.
type healthClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *healthClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *healthClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// eventTable is a health table on a fake clock that collects its events.
func eventTable(labels ...string) (*healthTable, *healthClock, func() []NodeEvent) {
	clock := &healthClock{t: time.Unix(1000, 0)}
	var mu sync.Mutex
	var events []NodeEvent
	table := newHealthTable()
	table.now = clock.now
	table.notify = func(ev NodeEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}
	table.register(labels)
	return table, clock, func() []NodeEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]NodeEvent(nil), events...)
	}
}

func TestNodeEventNamesTheProbeErrorAndTimeSinceTheLastAnswer(t *testing.T) {
	table, clock, events := eventTable("p/a")
	table.recordProbe("p/a", true, 20*time.Millisecond)
	clock.advance(3 * time.Minute)
	table.noteProbeError("p/a", context.DeadlineExceeded)
	table.recordProbe("p/a", false, 0)
	table.noteProbeError("p/a", context.DeadlineExceeded)
	table.recordProbe("p/a", false, 0)
	got := events()
	if len(got) != 1 {
		t.Fatalf("events = %+v, want one", got)
	}
	want := NodeEvent{Label: "p/a", Down: true, Reason: "2 probe failures (timeout)", SinceOK: 3 * time.Minute}
	if got[0] != want {
		t.Errorf("down event = %+v, want %+v", got[0], want)
	}
}

func TestNodeEventWithoutAProbeErrorOrAnswer(t *testing.T) {
	table, _, events := eventTable("p/a")
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	got := events()
	if len(got) != 1 || got[0].Reason != "2 probe failures" || got[0].SinceOK != 0 || !got[0].NeverOK {
		t.Fatalf("events = %+v, want a bare 2 probe failures from a node that never answered", got)
	}
}

func TestNodeEventForDialFailures(t *testing.T) {
	table, clock, events := eventTable("p/a")
	table.recordDial("p/a", true, time.Millisecond)
	clock.advance(time.Minute)
	for i := 0; i < downDialFailures; i++ {
		table.recordDial("p/a", false, 0)
	}
	got := events()
	want := NodeEvent{Label: "p/a", Down: true, Reason: "3 dial failures", SinceOK: time.Minute}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

func TestNodeEventBackCarriesTheDowntime(t *testing.T) {
	table, clock, events := eventTable("p/a")
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	clock.advance(41 * time.Second)
	table.recordProbe("p/a", true, 10*time.Millisecond)
	got := events()
	if len(got) != 2 {
		t.Fatalf("events = %+v, want down then back", got)
	}
	if want := (NodeEvent{Label: "p/a", Downtime: 41 * time.Second}); got[1] != want {
		t.Errorf("back event = %+v, want %+v", got[1], want)
	}
	// A second outage is timed from its own start.
	clock.advance(time.Hour)
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	clock.advance(5 * time.Second)
	table.recordProbe("p/a", true, 10*time.Millisecond)
	if got = events(); len(got) != 4 || got[3].Downtime != 5*time.Second {
		t.Errorf("second outage events = %+v", got)
	}
}

func TestProbeErrorKindIsClearedBySuccess(t *testing.T) {
	table, _, events := eventTable("p/a")
	table.noteProbeError("p/a", io.EOF)
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", true, time.Millisecond) // the kind goes with the failure streak
	table.recordProbe("p/a", false, 0)
	table.recordProbe("p/a", false, 0)
	got := events()
	if len(got) != 1 || got[0].Reason != "2 probe failures" {
		t.Fatalf("events = %+v, a stale probe error kind was reused", got)
	}
}

func TestCarryHealthKeepsDownSinceAndLastAnswer(t *testing.T) {
	old, clock, _ := eventTable("p/a")
	old.recordProbe("p/a", true, time.Millisecond)
	old.recordProbe("p/a", false, 0)
	old.recordProbe("p/a", false, 0)
	clock.advance(30 * time.Second)

	var got []NodeEvent
	carried := carryHealth(old, []string{"p/a"}, func(ev NodeEvent) { got = append(got, ev) })
	carried.recordProbe("p/a", true, time.Millisecond)
	if len(got) != 1 || got[0].Downtime != 30*time.Second || got[0].Down {
		t.Fatalf("events = %+v, want a back event with the 30s downtime carried over", got)
	}
}

func TestProbeFailKindNamesAFewCausesWithoutTheAddress(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{fmt.Errorf("probe: %w", context.DeadlineExceeded), "timeout"},
		{&net.DNSError{Err: "i/o timeout", IsTimeout: true}, "timeout"},
		{errors.New("dial tcp 203.0.113.9:443: connectex: No connection could be made because the target machine actively refused it. (connection refused)"), "connection refused"},
		{errors.New("read tcp 10.0.0.1:5000->203.0.113.9:443: connection reset by peer"), "connection reset"},
		{errors.New("lookup node.example: no such host"), "dns failure"},
		{errors.New("connect: network is unreachable"), "network unreachable"},
		{io.EOF, "connection closed"},
		{errors.New("tls: handshake failure"), "handshake failed"},
		{errors.New("dial tcp 203.0.113.9:443: connectex: A connection attempt failed because the connected party did not properly respond after a period of time"), "timeout"},
		{errors.New("wsarecv: An existing connection was forcibly closed by the remote host."), "connection reset"},
		{errors.New("connect: no route to host"), "network unreachable"},
		{errors.New("127.0.0.1:8080 connect error: HTTP need auth"), "proxy auth failed"},
		{errors.New("rejected username/password"), "proxy auth failed"},
		{errors.New("SOCKS need auth"), "proxy auth failed"},
		{errors.New("127.0.0.1:3128 connect error: CONNECT method not allowed by proxy"), "proxy refused"},
		{errors.New("403 Forbidden"), "proxy refused"},
		{errors.New("502 Bad Gateway"), "proxy refused"},
		{errors.New("can not connect remote err code: 503"), "proxy refused"},
		{errors.New("connect: connection refused"), "connection refused"}, // a refused connection is not a refusing proxy
		{errors.New("something odd with 203.0.113.9"), "error: something odd with <addr>"},
	}
	for _, c := range cases {
		got := probeFailKind(c.err)
		if got != c.want {
			t.Errorf("probeFailKind(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestProbeErrorHookReachesTheNodeAndTheCallersHook(t *testing.T) {
	table, _, events := eventTable("p/a")
	var next []string
	hook := probeErrorHook(map[string]string{"netivri-r0-0": "p/a"}, table, func(name string, err error) {
		next = append(next, name)
	})
	probe := probeHook(map[string]string{"netivri-r0-0": "p/a"}, table, nil, nil)
	for i := 0; i < downProbeFailures; i++ {
		hook("netivri-r0-0", context.DeadlineExceeded)
		probe("netivri-r0-0", false, 0)
	}
	hook("netivri-other", errors.New("unknown names are ignored"))
	if len(next) != downProbeFailures+1 {
		t.Errorf("the caller's hook ran %d times, want %d", len(next), downProbeFailures+1)
	}
	got := events()
	if len(got) != 1 || got[0].Reason != "2 probe failures (timeout)" {
		t.Fatalf("events = %+v", got)
	}
}

func TestWarmerReportsTheProbeErrorBeforeTheResult(t *testing.T) {
	w := &warmer{options: WarmOptions{ProbeTimeout: time.Second, ProbeURL: "http://probe.invalid/"}, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.proxies = []C.Proxy{testWarmProxy{name: "netivri-bad", typ: C.Socks5}, testWarmProxy{name: "netivri-ok", typ: C.Socks5}}
	boom := errors.New("boom")
	w.probe = func(_ context.Context, p C.Proxy, _ string) (uint16, error) {
		if p.Name() == "netivri-ok" {
			return 5, nil
		}
		return 0, boom
	}
	var mu sync.Mutex
	var order []string
	w.options.OnProbeError = func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != boom {
			t.Errorf("OnProbeError got %v, want the probe's error", err)
		}
		order = append(order, "err:"+name)
	}
	w.options.OnProbe = func(name string, alive bool, _ time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, fmt.Sprintf("probe:%s:%v", name, alive))
	}
	w.probeAll(context.Background())
	mu.Lock()
	defer mu.Unlock()
	pos := map[string]int{}
	for i, s := range order {
		pos[s] = i
	}
	if len(order) != 3 {
		t.Fatalf("calls = %v, want an error for the failed node and a result for both", order)
	}
	if e, p := pos["err:netivri-bad"], pos["probe:netivri-bad:false"]; e >= p {
		t.Errorf("the error came after the result: %v", order)
	}
}
