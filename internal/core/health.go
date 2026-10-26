package core

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// EWMA smoothing factors and the down rule from docs/design/node-stats.md.
	probeEWMAAlpha = 0.3
	dialEWMAAlpha  = 0.2
	healthRingSize = 32

	downProbeFailures = 2
	downDialFailures  = 3

	// minThroughputWindow is the window size from which the throughput EWMA
	// is fed (windows of at least 64 KiB).
	minThroughputWindow = 64 << 10
	throughputEWMAAlpha = 0.2
)

// healthScore implements the score formula of the design: lower is better.
// delay in ms, failRate in [0,1], throughput in MiB/s clamped to 20.
func healthScore(delayMS, failRate, throughputMiBps float64) float64 {
	if throughputMiBps < 0 {
		throughputMiBps = 0
	}
	if throughputMiBps > 20 {
		throughputMiBps = 20
	}
	return delayMS * (1 + 4*failRate) / (1 + throughputMiBps/20)
}

func ewma(old, value, alpha float64) float64 {
	return alpha*value + (1-alpha)*old
}

// nodeHealth is the per-node health record of the sticky group. Each record
// has its own mutex: the dial hot path only takes the locks of the nodes it
// touches, never a table-wide lock. downFlag is recomputed after every
// counter update, so "is this node down" is one lock-free atomic load.
type nodeHealth struct {
	mu       sync.Mutex
	downFlag atomic.Bool

	// hedgeUnits is the hedge budget in 1/10-token units; see hedge.go.
	hedgeUnits atomic.Int64

	probeEWMS     float64 // ms
	dialEWMS      float64 // ms
	throughput    float64 // MiB/s, EWMA over windows of >= 64 KiB
	hasProbe      bool
	hasDial       bool
	hasThroughput bool

	ring    [healthRingSize]bool // last outcomes, probes and dials
	ringLen int
	ringPos int

	consProbeFail int
	consDialFail  int

	// label and notify report a change of the down state; notify may be nil.
	label  string
	notify func(label string, down bool)
}

// recomputeDown refreshes the atomic flag and reports whether it changed; the
// caller holds h.mu.
func (h *nodeHealth) recomputeDown() bool {
	down := h.consProbeFail >= downProbeFailures || h.consDialFail >= downDialFailures
	return h.downFlag.Swap(down) != down
}

// announce tells the owner of the table that the node went down or came back.
// It runs after h.mu is released, so a slow listener never holds the lock.
func (h *nodeHealth) announce(changed bool) {
	if changed && h.notify != nil {
		h.notify(h.label, h.downFlag.Load())
	}
}

// Down reports whether the node is down: 2 consecutive probe failures or 3
// consecutive dial failures. A success of EITHER kind clears both counters.
func (h *nodeHealth) Down() bool { return h.downFlag.Load() }

func (h *nodeHealth) recordProbe(alive bool, delay time.Duration) {
	h.mu.Lock()
	changed := h.applyProbe(alive, delay)
	h.mu.Unlock()
	h.announce(changed)
}

// applyProbe is recordProbe under h.mu; it reports a change of the down state.
func (h *nodeHealth) applyProbe(alive bool, delay time.Duration) bool {
	if alive {
		h.consProbeFail = 0
		h.consDialFail = 0
		// The first sample initialises the EWMA instead of averaging into 0.
		if !h.hasProbe {
			h.hasProbe = true
			h.probeEWMS = delaySeconds(delay) * 1000
		} else {
			h.probeEWMS = ewma(h.probeEWMS, delaySeconds(delay)*1000, probeEWMAAlpha)
		}
	} else {
		h.consProbeFail++
	}
	h.recordOutcome(alive)
	return h.recomputeDown()
}

func (h *nodeHealth) recordDial(ok bool, took time.Duration) {
	h.mu.Lock()
	changed := h.applyDial(ok, took)
	h.mu.Unlock()
	h.announce(changed)
}

// applyDial is recordDial under h.mu; it reports a change of the down state.
func (h *nodeHealth) applyDial(ok bool, took time.Duration) bool {
	if ok {
		h.consDialFail = 0
		h.consProbeFail = 0
		if !h.hasDial {
			h.hasDial = true
			h.dialEWMS = delaySeconds(took) * 1000
		} else {
			h.dialEWMS = ewma(h.dialEWMS, delaySeconds(took)*1000, dialEWMAAlpha)
		}
	} else {
		h.consDialFail++
	}
	h.recordOutcome(ok)
	return h.recomputeDown()
}

func (h *nodeHealth) recordOutcome(ok bool) {
	h.ring[h.ringPos] = ok
	h.ringPos = (h.ringPos + 1) % healthRingSize
	if h.ringLen < healthRingSize {
		h.ringLen++
	}
}

// recordThroughput feeds the throughput EWMA; windows below 64 KiB are
// ignored (the observer still sees the smaller windows through the poller).
func (h *nodeHealth) recordThroughput(bytes int64, window time.Duration) {
	if bytes < minThroughputWindow || window <= 0 {
		return
	}
	mibps := float64(bytes) / (1024 * 1024) / window.Seconds()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasThroughput {
		h.hasThroughput = true
		h.throughput = mibps
	} else {
		h.throughput = ewma(h.throughput, mibps, throughputEWMAAlpha)
	}
}

// delayMS is the mean of the known EWMAs (probe, dial); ok is false when the
// node has no samples at all. The caller holds h.mu.
func (h *nodeHealth) delayMS() (float64, bool) {
	switch {
	case h.hasProbe && h.hasDial:
		return (h.probeEWMS + h.dialEWMS) / 2, true
	case h.hasProbe:
		return h.probeEWMS, true
	case h.hasDial:
		return h.dialEWMS, true
	default:
		return 0, false
	}
}

func (h *nodeHealth) failRate() float64 {
	if h.ringLen == 0 {
		return 0
	}
	fails := 0
	for i := 0; i < h.ringLen; i++ {
		if !h.ring[i] {
			fails++
		}
	}
	return float64(fails) / float64(h.ringLen)
}

// score is the selection score of the node; lower is better. The caller
// holds h.mu.
func (h *nodeHealth) score() float64 {
	delay, _ := h.delayMS()
	return healthScore(delay, h.failRate(), h.throughput)
}

func delaySeconds(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
}

// memberHealthView is the selection-relevant state of one member.
type memberHealthView struct {
	Label   string
	DelayMS float64
	Known   bool
	Down    bool
	Score   float64
	// Tier is the priority tier of the member in its route (lower first).
	Tier int
}

// view snapshots one record under its lock.
func (h *nodeHealth) view(label string) memberHealthView {
	h.mu.Lock()
	defer h.mu.Unlock()
	delay, known := h.delayMS()
	return memberHealthView{
		Label:   label,
		DelayMS: delay,
		Known:   known,
		Down:    h.downFlag.Load(),
		Score:   h.score(),
	}
}

// healthTable keeps a nodeHealth per node label. Entries are created up
// front by register, so the map is read-only afterwards and lookups need no
// lock; measurements on unknown labels are ignored.
type healthTable struct {
	entries map[string]*nodeHealth
	// notify, set before the first register, hears every change of a node's
	// down state with the node label. It may be nil.
	notify func(label string, down bool)
}

func newHealthTable() *healthTable {
	return &healthTable{entries: make(map[string]*nodeHealth)}
}

// register creates the records for the given labels before any traffic.
func (t *healthTable) register(labels []string) {
	for _, label := range labels {
		if _, ok := t.entries[label]; !ok {
			h := &nodeHealth{label: label, notify: t.notify}
			h.initHedgeBudget()
			t.entries[label] = h
		}
	}
}

// carryHealth builds the table of a new routing generation: a label that
// stays keeps its measurements, down state and hedge budget, the others start
// fresh. Nothing is announced, so a node that was down before stays down
// without a second report. notify is the table's new listener.
func carryHealth(old *healthTable, labels []string, notify func(label string, down bool)) *healthTable {
	t := newHealthTable()
	t.notify = notify
	for _, label := range labels {
		if _, done := t.entries[label]; done || old == nil {
			continue
		}
		if h := old.entries[label]; h != nil {
			t.entries[label] = h.cloneFor(notify)
		}
	}
	t.register(labels)
	return t
}

// cloneFor copies the record under its lock for a new table with listener
// notify.
func (h *nodeHealth) cloneFor(notify func(label string, down bool)) *nodeHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := &nodeHealth{
		probeEWMS:     h.probeEWMS,
		dialEWMS:      h.dialEWMS,
		throughput:    h.throughput,
		hasProbe:      h.hasProbe,
		hasDial:       h.hasDial,
		hasThroughput: h.hasThroughput,
		ring:          h.ring,
		ringLen:       h.ringLen,
		ringPos:       h.ringPos,
		consProbeFail: h.consProbeFail,
		consDialFail:  h.consDialFail,
		label:         h.label,
		notify:        notify,
	}
	c.downFlag.Store(h.downFlag.Load())
	c.hedgeUnits.Store(h.hedgeUnits.Load())
	return c
}

// health returns the record for a registered label, or nil.
func (t *healthTable) health(label string) *nodeHealth { return t.entries[label] }

func (t *healthTable) recordProbe(label string, alive bool, delay time.Duration) {
	if h := t.entries[label]; h != nil {
		h.recordProbe(alive, delay)
	}
}

func (t *healthTable) recordDial(label string, ok bool, took time.Duration) {
	if h := t.entries[label]; h != nil {
		h.recordDial(ok, took)
	}
}

func (t *healthTable) recordThroughput(label string, bytes int64, window time.Duration) {
	if h := t.entries[label]; h != nil {
		h.recordThroughput(bytes, window)
	}
}

func (t *healthTable) down(label string) bool {
	h := t.entries[label]
	return h != nil && h.Down()
}

// views returns one view per label, in the order given.
func (t *healthTable) views(labels []string) []memberHealthView {
	views := make([]memberHealthView, len(labels))
	for i, label := range labels {
		views[i] = memberHealthView{Label: label}
		if h := t.entries[label]; h != nil {
			views[i] = h.view(label)
		}
	}
	return views
}
