package core

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

const (
	defaultProbeInterval = 45 * time.Second
	defaultProbeTimeout  = 5 * time.Second

	// probeDownMaxInterval caps the probe interval of a node that stays
	// down (45 s, 90 s, 3 m, then this cap).
	probeDownMaxInterval = 10 * time.Minute
	defaultProbeURL      = "http://www.gstatic.com/generate_204"
	// defaultConfirmURL is where a failed probe is tried once more, so one
	// unreachable or slow target does not make a working node look down.
	defaultConfirmURL = "http://cp.cloudflare.com/generate_204"
	defaultPoolTTL    = 30 * time.Second

	// defaultProbeConcurrency bounds the probes of one round.
	defaultProbeConcurrency = 8
)

// WarmOptions configures periodic node probes and the optional raw TCP pool.
type WarmOptions struct {
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	ProbeURL      string
	PoolSize      int
	PoolTTL       time.Duration

	// ConfirmProbe retries a failed probe at once against ConfirmURL with
	// ConfirmTimeout, and only a failure of both counts as a failed probe.
	// Off by default; the angel turns it on.
	ConfirmProbe bool
	// ConfirmURL defaults to defaultConfirmURL when ProbeURL is the default
	// and to ProbeURL otherwise; ConfirmTimeout defaults to twice ProbeTimeout.
	ConfirmURL     string
	ConfirmTimeout time.Duration

	// ProbeConcurrency bounds how many probes of one round run at a
	// time; 0 means defaultProbeConcurrency, negative is invalid.
	ProbeConcurrency int
	// OnProbe, when set, is called after every probe from the probing
	// goroutine, outside any warmer lock: name is the internal proxy
	// name, alive reports probe success and delay is the measured
	// delay (0 on failure).
	OnProbe func(name string, alive bool, delay time.Duration)
	// OnProbeError, when set, is called with the error of a failed probe,
	// from the probing goroutine just before OnProbe reports the failure.
	OnProbeError func(name string, err error)
	// Down, when set, reports whether the node behind an internal proxy
	// name is down (see nodeHealth.Down); down nodes are probed with a
	// lengthened interval (probeBackoffInterval). Start sets it from the
	// sticky health table.
	Down func(name string) bool

	// only, when set, limits the probed proxies to the names it accepts;
	// seed is the probe schedule each name starts with. Reconfigure sets both,
	// so the earlier generations still in Mihomo's proxy map are not probed,
	// nodes that stay down keep their lengthened interval and the others are
	// not probed again before their interval is up.
	only func(name string) bool
	seed map[string]probeSchedule
}

// NodeHealth is the most recent probe result for one proxy.
type NodeHealth struct {
	Name      string
	Alive     bool // the last probe succeeded
	Delay     time.Duration
	CheckedAt time.Time
	// Down is the routing state of the node (see nodeHealth.Down): false for
	// a node whose last probe failed once or that carries traffic. Set by
	// Instance.WarmStats, not by the warmer.
	Down bool
}

// WarmStats is a point-in-time copy of warmer counters and health results.
type WarmStats struct {
	Probes        uint64
	ProbeFailures uint64
	PoolHits      uint64
	PoolMisses    uint64
	PoolDiscards  uint64
	LastProbe     time.Time
	Nodes         []NodeHealth
}

type warmProbe func(context.Context, C.Proxy, string) (uint16, error)
type warmClock interface {
	Now() time.Time
	NewTicker(time.Duration) warmTicker
}
type warmTicker interface {
	Chan() <-chan time.Time
	Stop()
}
type realWarmClock struct{}
type realWarmTicker struct{ ticker *time.Ticker }

func (realWarmClock) Now() time.Time                       { return time.Now() }
func (realWarmClock) NewTicker(d time.Duration) warmTicker { return realWarmTicker{time.NewTicker(d)} }
func (t realWarmTicker) Chan() <-chan time.Time            { return t.ticker.C }
func (t realWarmTicker) Stop()                             { t.ticker.Stop() }

// probeBackoffInterval maps how many times a down node has been probed
// to its probe interval: base (45 s), 2*base (90 s), 4*base (3 m), then
// the 10 minute cap. A node that is not down is always probed every base.
func probeBackoffInterval(base time.Duration, downRounds int) time.Duration {
	interval := base
	switch downRounds {
	case 2:
		interval = 2 * base
	case 3:
		interval = 4 * base
	}
	if downRounds > 3 || interval > probeDownMaxInterval {
		return probeDownMaxInterval
	}
	return interval
}

// probeSchedule is the backoff state of one node: how many consecutive
// probes found it down and when it is next due. The zero value is due
// immediately and is used for every node that is not down.
type probeSchedule struct {
	downRounds int
	nextDue    time.Time
}

type warmer struct {
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
	mu            sync.RWMutex
	stats         WarmStats
	proxies       []C.Proxy
	backoff       []probeSchedule
	options       WarmOptions
	clock         warmClock
	probe         warmProbe
	down          func(name string) bool
	pool          *poolDialer
	probeCount    atomic.Uint64
	probeFailures atomic.Uint64
	poolHits      atomic.Uint64
	poolMisses    atomic.Uint64
	poolDiscards  atomic.Uint64
}

func normalizeWarmOptions(o WarmOptions) (WarmOptions, error) {
	if o.ProbeInterval == 0 {
		o.ProbeInterval = defaultProbeInterval
	}
	if o.ProbeTimeout == 0 {
		o.ProbeTimeout = defaultProbeTimeout
	}
	if o.ProbeURL == "" {
		o.ProbeURL = defaultProbeURL
	}
	if o.ConfirmProbe {
		if o.ConfirmURL == "" {
			o.ConfirmURL = o.ProbeURL
			if o.ProbeURL == defaultProbeURL {
				o.ConfirmURL = defaultConfirmURL
			}
		}
		if o.ConfirmTimeout == 0 {
			o.ConfirmTimeout = 2 * o.ProbeTimeout
		}
	}
	if o.PoolTTL == 0 {
		o.PoolTTL = defaultPoolTTL
	}
	if o.ProbeConcurrency == 0 {
		o.ProbeConcurrency = defaultProbeConcurrency
	}
	if o.ProbeInterval < 0 || o.ProbeTimeout <= 0 || o.PoolTTL < 0 || o.PoolSize < 0 || o.ProbeConcurrency < 0 || o.ConfirmTimeout < 0 {
		return o, errors.New("core: invalid warm options")
	}
	for _, raw := range []string{o.ProbeURL, o.ConfirmURL} {
		if raw == "" && !o.ConfirmProbe {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return o, errors.New("core: probe URL must be absolute HTTP(S)")
		}
	}
	return o, nil
}

// startWarm starts periodic Mihomo URL tests. Call it after ApplyConfig and
// call Close before shutting Mihomo down.
func startWarm(o WarmOptions, node map[string]any) (*warmer, error) {
	return startWarmWith(o, node, realWarmClock{}, defaultWarmProbe)
}

func startWarmWith(o WarmOptions, node map[string]any, clock warmClock, probe warmProbe) (*warmer, error) {
	var err error
	o, err = normalizeWarmOptions(o)
	if err != nil {
		return nil, err
	}
	if clock == nil || probe == nil {
		return nil, errors.New("core: warm clock and probe are required")
	}
	w := &warmer{done: make(chan struct{}), options: o, clock: clock, probe: probe, down: o.Down}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	// Replace first so the initial probe observes the dialer-hook adapter.
	if o.PoolSize > 0 && node != nil {
		w.pool, err = newPoolDialer(node, o.PoolSize, o.PoolTTL, w)
		if err != nil {
			cancel()
			return nil, err
		}
		if err = w.installPool(node); err != nil {
			w.pool.stop()
			w.pool.wait(5 * time.Second)
			cancel()
			return nil, errors.New("core: could not install pooled proxy")
		}
		w.pool.start()
	}
	w.proxies = warmLeaves(tunnel.Proxies(), o.only)
	w.backoff = seededBackoff(w.proxies, o.seed)
	go w.run(ctx)
	return w, nil
}

// warmLeaves returns the probed proxies of a proxy map sorted by name: the
// netivri leaves that only accepts (every one when only is nil).
func warmLeaves(proxies map[string]C.Proxy, only func(name string) bool) []C.Proxy {
	var out []C.Proxy
	for _, p := range proxies {
		if p == nil || !isWarmLeaf(p) || (only != nil && !only(p.Name())) {
			continue
		}
		if generatedNodeName(p.Name()) && proxies[p.Name()+"-post"] != nil {
			continue
		}
		if isChainHelperName(p.Name()) && !isRoutePostName(p.Name()) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// seededBackoff lays the seed out in proxy order; nil without a seed.
func seededBackoff(proxies []C.Proxy, seed map[string]probeSchedule) []probeSchedule {
	if len(seed) == 0 {
		return nil
	}
	out := make([]probeSchedule, len(proxies))
	for i, p := range proxies {
		out[i] = seed[p.Name()]
	}
	return out
}

func isWarmLeaf(p C.Proxy) bool {
	if p == nil || !strings.HasPrefix(p.Name(), "netivri-") {
		return false
	}
	switch p.Type() {
	case C.Direct, C.Compatible, C.Pass, C.PassRule, C.Reject, C.RejectDrop, C.Dns, C.Rematch,
		C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
		return false
	}
	return true
}

func defaultWarmProbe(ctx context.Context, p C.Proxy, target string) (uint16, error) {
	return p.URLTest(ctx, target, nil)
}

func (w *warmer) run(ctx context.Context) {
	defer close(w.done)
	w.probeAll(ctx)
	ticker := w.clock.NewTicker(w.options.ProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			w.probeAll(ctx)
		}
	}
}

// probeAll runs one probe round: every node that is due (down nodes are
// skipped until their backoff interval has passed, see probeSchedule) is probed concurrently with
// at most ProbeConcurrency probes in flight. The round returns when all
// probes are done or the context ends (each probe inherits its
// cancellation through its timeout context).
func (w *warmer) probeAll(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	concurrency := w.options.ProbeConcurrency
	if concurrency <= 0 {
		concurrency = defaultProbeConcurrency
	}
	type scheduled struct {
		proxy C.Proxy
		index int
	}
	now := w.clock.Now()
	w.mu.Lock()
	if len(w.backoff) != len(w.proxies) {
		w.backoff = make([]probeSchedule, len(w.proxies))
	}
	var due []scheduled
	for i, p := range w.proxies {
		st := &w.backoff[i]
		if !w.isDown(p.Name()) {
			if st.downRounds == 0 && now.Before(st.nextDue) {
				// Seeded by Reconfigure: probed by the previous generation
				// less than an interval ago.
				continue
			}
			*st = probeSchedule{}
			due = append(due, scheduled{proxy: p, index: i})
			continue
		}
		if now.Before(st.nextDue) {
			continue
		}
		st.downRounds++
		st.nextDue = now.Add(probeBackoffInterval(w.options.ProbeInterval, st.downRounds))
		due = append(due, scheduled{proxy: p, index: i})
	}
	w.mu.Unlock()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
schedule:
	for _, item := range due {
		select {
		case sem <- struct{}{}:
		case <-parent.Done():
			break schedule
		}
		if parent.Err() != nil {
			// Both cases of the select can be ready after a cancellation and
			// Go picks one at random: never start a probe once it is seen.
			<-sem
			break schedule
		}
		wg.Add(1)
		go func(p C.Proxy, index int) {
			defer wg.Done()
			defer func() { <-sem }()
			w.probeOne(parent, p, index)
		}(item.proxy, item.index)
	}
	wg.Wait()
}

// probeOne runs a single probe and records its result. OnProbe runs
// outside w.mu, from the probing goroutine.
func (w *warmer) probeOne(parent context.Context, p C.Proxy, index int) {
	ctx, cancel := context.WithTimeout(parent, w.options.ProbeTimeout)
	delay, err := w.probe(ctx, p, w.options.ProbeURL)
	cancel()
	if err != nil && w.options.ConfirmProbe && parent.Err() == nil {
		// One failure may be the target and not the node: try another
		// target, with more time, before the node is blamed.
		ctx, cancel = context.WithTimeout(parent, w.options.ConfirmTimeout)
		delay, err = w.probe(ctx, p, w.options.ConfirmURL)
		cancel()
	}
	if parent.Err() != nil {
		// Shutdown interrupted the probe: that says nothing about the node.
		return
	}
	now := w.clock.Now()
	w.probeCount.Add(1)
	var measured time.Duration
	if err == nil {
		measured = time.Duration(delay) * time.Millisecond
	} else {
		w.probeFailures.Add(1)
	}
	w.mu.Lock()
	w.stats.LastProbe = now
	i := sort.Search(len(w.stats.Nodes), func(i int) bool { return w.stats.Nodes[i].Name >= p.Name() })
	if i == len(w.stats.Nodes) || w.stats.Nodes[i].Name != p.Name() {
		w.stats.Nodes = append(w.stats.Nodes, NodeHealth{})
		copy(w.stats.Nodes[i+1:], w.stats.Nodes[i:])
	}
	h := &w.stats.Nodes[i]
	h.Name, h.Alive, h.CheckedAt = p.Name(), err == nil, now
	if err == nil {
		h.Delay = measured
	}
	if index >= 0 && index < len(w.backoff) && err == nil {
		// Success: back to the base interval (probeSchedule zero value).
		w.backoff[index] = probeSchedule{}
	}
	w.mu.Unlock()
	if err != nil && w.options.OnProbeError != nil {
		w.options.OnProbeError(p.Name(), err)
	}
	if w.options.OnProbe != nil {
		w.options.OnProbe(p.Name(), err == nil, measured)
	}
}

// Stats returns counters and an independent copy of the sorted node snapshot.
func (w *warmer) Stats() WarmStats {
	w.mu.RLock()
	s := w.stats
	s.Nodes = append([]NodeHealth(nil), s.Nodes...)
	w.mu.RUnlock()
	s.Probes = w.probeCount.Load()
	s.ProbeFailures = w.probeFailures.Load()
	s.PoolHits = w.poolHits.Load()
	s.PoolMisses = w.poolMisses.Load()
	s.PoolDiscards = w.poolDiscards.Load()
	return s
}

// Close cancels probes and the raw TCP pool. It waits at most five seconds for
// its goroutines, after which context cancellation still proceeds asynchronously.
func (w *warmer) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		if w.pool != nil {
			w.pool.stop()
		}
		if w.done != nil {
			select {
			case <-w.done:
			case <-time.After(5 * time.Second):
			}
		}
		if w.pool != nil {
			w.pool.wait(5 * time.Second)
		}
	})
}

// backoffByName returns when every probed proxy is next due, keyed by proxy
// name: the backoff of a node that is down, and one interval after the last
// probe for the others, so a Reconfigure keeps the probe cadence instead of
// probing every node again at once. Nil-safe.
func (w *warmer) backoffByName() map[string]probeSchedule {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make(map[string]probeSchedule)
	// Down nodes, and nodes still waiting for the schedule they were seeded
	// with: a generation that never probed a node passes its seed on.
	for i, st := range w.backoff {
		if i < len(w.proxies) && st != (probeSchedule{}) {
			out[w.proxies[i].Name()] = st
		}
	}
	for _, h := range w.stats.Nodes {
		if h.CheckedAt.IsZero() {
			continue
		}
		st, ok := out[h.Name]
		if ok && st.downRounds > 0 {
			continue
		}
		if due := h.CheckedAt.Add(w.options.ProbeInterval); !ok || due.After(st.nextDue) {
			out[h.Name] = probeSchedule{nextDue: due}
		}
	}
	return out
}

// isDown reports whether the node behind an internal proxy name is down;
// without a Down hook (standalone warmer) no node is ever down.
func (w *warmer) isDown(name string) bool {
	return w.down != nil && w.down(name)
}

func (w *warmer) addPoolMetric(kind string) {
	switch kind {
	case "hit":
		w.poolHits.Add(1)
	case "miss":
		w.poolMisses.Add(1)
	case "discard":
		w.poolDiscards.Add(1)
	}
}
