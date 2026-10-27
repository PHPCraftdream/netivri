package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

type probeCall struct {
	url     string
	timeout time.Duration
}

// confirmWarmer is a warmer whose probe answers from script, one answer per
// call, and records each call's URL and deadline.
func confirmWarmer(t *testing.T, o WarmOptions, script ...error) (*warmer, *[]probeCall) {
	t.Helper()
	o.ProbeTimeout = 5 * time.Second
	if o.ProbeURL == "" {
		o.ProbeURL = defaultProbeURL
	}
	o, err := normalizeWarmOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []probeCall
	w := &warmer{options: o, clock: testWarmClock{now: time.Unix(1, 0)}}
	w.probe = func(ctx context.Context, _ C.Proxy, target string) (uint16, error) {
		mu.Lock()
		defer mu.Unlock()
		deadline, _ := ctx.Deadline()
		calls = append(calls, probeCall{url: target, timeout: time.Until(deadline).Round(time.Second)})
		if len(calls) > len(script) {
			t.Fatalf("probe called %d times, script has %d answers", len(calls), len(script))
		}
		if err := script[len(calls)-1]; err != nil {
			return 0, err
		}
		return 7, nil
	}
	return w, &calls
}

// probeRecorder collects what the warmer reports for a probe.
type probeRecorder struct {
	alive []bool
	errs  []error
}

func (r *probeRecorder) attach(w *warmer) {
	w.options.OnProbe = func(_ string, ok bool, _ time.Duration) { r.alive = append(r.alive, ok) }
	w.options.OnProbeError = func(_ string, err error) { r.errs = append(r.errs, err) }
}

func TestConfirmProbeSavesANodeThatAnswersTheSecondTarget(t *testing.T) {
	w, calls := confirmWarmer(t, WarmOptions{ConfirmProbe: true}, context.DeadlineExceeded, nil)
	var rec probeRecorder
	rec.attach(w)
	w.probeOne(context.Background(), testWarmProxy{name: "netivri-n", typ: C.Socks5}, 0)

	if len(rec.alive) != 1 || !rec.alive[0] || len(rec.errs) != 0 {
		t.Fatalf("result alive=%v errs=%v, want one success and no error", rec.alive, rec.errs)
	}
	want := []probeCall{{defaultProbeURL, 5 * time.Second}, {defaultConfirmURL, 10 * time.Second}}
	if len(*calls) != 2 || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
		t.Errorf("probe calls = %+v, want %+v", *calls, want)
	}
	if s := w.Stats(); s.Probes != 1 || s.ProbeFailures != 0 || len(s.Nodes) != 1 || !s.Nodes[0].Alive {
		t.Errorf("stats = %+v, want one probe, no failure, node alive", s)
	}
}

func TestConfirmProbeFailsWhenBothTargetsFail(t *testing.T) {
	second := errors.New("second target failed")
	w, calls := confirmWarmer(t, WarmOptions{ConfirmProbe: true}, context.DeadlineExceeded, second)
	var rec probeRecorder
	rec.attach(w)
	w.probeOne(context.Background(), testWarmProxy{name: "netivri-n", typ: C.Socks5}, 0)

	if len(rec.alive) != 1 || rec.alive[0] || len(rec.errs) != 1 || rec.errs[0] != second {
		t.Fatalf("result alive=%v errs=%v, want one failure carrying the confirmation's error", rec.alive, rec.errs)
	}
	if len(*calls) != 2 {
		t.Errorf("%d probe calls, want 2", len(*calls))
	}
	if s := w.Stats(); s.Probes != 1 || s.ProbeFailures != 1 {
		t.Errorf("stats = %+v, want one probe that failed", s)
	}
}

func TestConfirmProbeDoesNotRunForASuccessOrWhenOff(t *testing.T) {
	w, calls := confirmWarmer(t, WarmOptions{ConfirmProbe: true}, nil)
	w.probeOne(context.Background(), testWarmProxy{name: "netivri-n", typ: C.Socks5}, 0)
	if len(*calls) != 1 {
		t.Errorf("a successful probe was confirmed: %+v", *calls)
	}

	w, calls = confirmWarmer(t, WarmOptions{}, context.DeadlineExceeded)
	w.probeOne(context.Background(), testWarmProxy{name: "netivri-n", typ: C.Socks5}, 0)
	if len(*calls) != 1 {
		t.Errorf("a failure was confirmed with ConfirmProbe off: %+v", *calls)
	}
}

func TestConfirmProbeIsSkippedOnShutdown(t *testing.T) {
	w, calls := confirmWarmer(t, WarmOptions{ConfirmProbe: true}, context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	probe := w.probe
	w.probe = func(c context.Context, p C.Proxy, target string) (uint16, error) {
		cancel() // shutdown during the first probe
		return probe(c, p, target)
	}
	var rec probeRecorder
	rec.attach(w)
	w.probeOne(ctx, testWarmProxy{name: "netivri-n", typ: C.Socks5}, 0)
	if len(*calls) != 1 || len(rec.alive) != 0 {
		t.Errorf("calls=%+v alive=%v, want one call and nothing recorded", *calls, rec.alive)
	}
	if s := w.Stats(); s.Probes != 0 {
		t.Errorf("probes = %d, want 0", s.Probes)
	}
}

func TestNormalizeWarmOptionsConfirmDefaults(t *testing.T) {
	o, err := normalizeWarmOptions(WarmOptions{ConfirmProbe: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.ConfirmURL != defaultConfirmURL || o.ConfirmTimeout != 2*defaultProbeTimeout {
		t.Errorf("defaults = %q, %v", o.ConfirmURL, o.ConfirmTimeout)
	}

	o, err = normalizeWarmOptions(WarmOptions{ConfirmProbe: true, ProbeURL: "http://probe.test/x", ProbeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if o.ConfirmURL != "http://probe.test/x" || o.ConfirmTimeout != 2*time.Second {
		t.Errorf("a custom probe URL is confirmed against itself, got %q, %v", o.ConfirmURL, o.ConfirmTimeout)
	}

	o, err = normalizeWarmOptions(WarmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if o.ConfirmProbe || o.ConfirmURL != "" || o.ConfirmTimeout != 0 {
		t.Errorf("confirmation set up while off: %+v", o)
	}

	for _, bad := range []WarmOptions{
		{ConfirmProbe: true, ConfirmURL: "ftp://x/"},
		{ConfirmProbe: true, ConfirmTimeout: -time.Second},
	} {
		if _, err := normalizeWarmOptions(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}
