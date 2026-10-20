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
	defaultProbeURL      = "http://www.gstatic.com/generate_204"
	defaultPoolTTL       = 30 * time.Second

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

	// ProbeConcurrency bounds how many probes of one round run at a
	// time; 0 means defaultProbeConcurrency, negative is invalid.
	ProbeConcurrency int
	// OnProbe, when set, is called after every probe from the probing
	// goroutine, outside any warmer lock: name is the internal proxy
	// name, alive reports probe success and delay is the measured
	// delay (0 on failure).
	OnProbe func(name string, alive bool, delay time.Duration)
}

// NodeHealth is the most recent probe result for one proxy.
type NodeHealth struct {
	Name      string
	Alive     bool
	Delay     time.Duration
	CheckedAt time.Time
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

// probeBackoff tracks consecutive probe failures of one node so dead
// nodes are skipped in some rounds (no randomness, recovered nodes
// reset).
type probeBackoff struct {
	failures int
	skipped  int
}

type warmer struct {
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
	mu            sync.RWMutex
	stats         WarmStats
	proxies       []C.Proxy
	backoff       []probeBackoff
	options       WarmOptions
	clock         warmClock
	probe         warmProbe
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
	if o.PoolTTL == 0 {
		o.PoolTTL = defaultPoolTTL
	}
	if o.ProbeConcurrency == 0 {
		o.ProbeConcurrency = defaultProbeConcurrency
	}
	if o.ProbeInterval < 0 || o.ProbeTimeout <= 0 || o.PoolTTL < 0 || o.PoolSize < 0 || o.ProbeConcurrency < 0 {
		return o, errors.New("core: invalid warm options")
	}
	u, err := url.Parse(o.ProbeURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return o, errors.New("core: probe URL must be absolute HTTP(S)")
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
	w := &warmer{done: make(chan struct{}), options: o, clock: clock, probe: probe}
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
	for _, p := range tunnel.Proxies() {
		if p != nil && isWarmLeaf(p) {
			w.proxies = append(w.proxies, p)
		}
	}
	sort.Slice(w.proxies, func(i, j int) bool { return w.proxies[i].Name() < w.proxies[j].Name() })
	go w.run(ctx)
	return w, nil
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

// probeAll runs one probe round: every node that is due (dead nodes are
// skipped in some rounds, see probeBackoff) is probed concurrently with
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
	w.mu.Lock()
	if len(w.backoff) != len(w.proxies) {
		w.backoff = make([]probeBackoff, len(w.proxies))
	}
	var due []scheduled
	for i, p := range w.proxies {
		st := &w.backoff[i]
		if st.failures >= 2 && st.skipped < min(st.failures-1, 3) {
			st.skipped++
			continue
		}
		st.skipped = 0
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
	if index >= 0 && index < len(w.backoff) {
		if err == nil {
			w.backoff[index] = probeBackoff{}
		} else {
			w.backoff[index].failures++
			w.backoff[index].skipped = 0
		}
	}
	w.mu.Unlock()
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
