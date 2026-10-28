package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
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

	// lastOK is the last successful probe or dial, downSince the moment the
	// node went down (zero while up) and probeFail the kind of the latest
	// probe error: they feed the NodeEvent.
	lastOK    time.Time
	downSince time.Time
	// upSince is when the node was registered or last came back, unix
	// nanoseconds; with the down flag it tells how long the node has been up.
	upSince   atomic.Int64
	probeFail string

	// label and notify report a change of the down state; notify may be nil.
	// now is the clock (nil means time.Now).
	label  string
	notify func(NodeEvent)
	now    func() time.Time
}

// NodeEvent is one change of a route node's down state, for Options.OnNodeHealth.
type NodeEvent struct {
	Label string
	// Down is true when the node just went down, false when it is back.
	Down bool
	// Reason (Down only) names what tripped it, such as "2 probe failures
	// (timeout)" or "3 dial failures". It never holds an address.
	Reason string
	// SinceOK (Down only) is how long ago the node last answered; 0 if never.
	SinceOK time.Duration
	// NeverOK (Down only) is true when no probe or dial of the node has ever
	// succeeded: it did not go down, it never came up.
	NeverOK bool
	// Downtime (back only) is how long the node was down; 0 if unknown.
	Downtime time.Duration
}

func (h *nodeHealth) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// recomputeDown refreshes the atomic flag and reports whether it changed; the
// caller holds h.mu.
func (h *nodeHealth) recomputeDown() bool {
	down := h.consProbeFail >= downProbeFailures || h.consDialFail >= downDialFailures
	changed := h.downFlag.Swap(down) != down
	if changed && down {
		h.downSince = h.clock()
	}
	if changed && !down {
		h.upSince.Store(h.clock().UnixNano())
	}
	return changed
}

// eventLocked describes the change recomputeDown just reported; the caller
// holds h.mu. A node that is back forgets when it went down.
func (h *nodeHealth) eventLocked() NodeEvent {
	now := h.clock()
	ev := NodeEvent{Label: h.label, Down: h.downFlag.Load()}
	if ev.Down {
		switch {
		case h.consProbeFail >= downProbeFailures && h.probeFail != "":
			ev.Reason = fmt.Sprintf("%d probe failures (%s)", h.consProbeFail, h.probeFail)
		case h.consProbeFail >= downProbeFailures:
			ev.Reason = fmt.Sprintf("%d probe failures", h.consProbeFail)
		default:
			ev.Reason = fmt.Sprintf("%d dial failures", h.consDialFail)
		}
		if h.lastOK.IsZero() {
			ev.NeverOK = true
		} else {
			ev.SinceOK = now.Sub(h.lastOK)
		}
		return ev
	}
	if !h.downSince.IsZero() {
		ev.Downtime = now.Sub(h.downSince)
	}
	h.downSince = time.Time{}
	return ev
}

// announce tells the owner of the table that the node went down or came back.
// It runs after h.mu is released, so a slow listener never holds the lock.
func (h *nodeHealth) announce(changed bool, ev NodeEvent) {
	if changed && h.notify != nil {
		h.notify(ev)
	}
}

// stableFor reports whether the node is not down and has been up for at least
// d at nowNs (unix nanoseconds). Lock-free: two atomic loads.
func (h *nodeHealth) stableFor(nowNs int64, d time.Duration) bool {
	return h != nil && !h.downFlag.Load() && nowNs-h.upSince.Load() >= int64(d)
}

// noteProbeError remembers the kind of the latest probe error, so the next
// down event can name it.
func (h *nodeHealth) noteProbeError(kind string) {
	h.mu.Lock()
	h.probeFail = kind
	h.mu.Unlock()
}

// probeFailKind names a probe error in a few words, without any address. A
// cause it does not know is "error: " and a scrubbed excerpt of the message.
func probeFailKind(err error) string {
	var netErr net.Error
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "did not properly respond") || strings.Contains(msg, "failed to respond"):
		return "timeout"
	case strings.Contains(msg, "refused"):
		return "connection refused"
	case strings.Contains(msg, "reset") || strings.Contains(msg, "forcibly closed") || strings.Contains(msg, "broken pipe"):
		return "connection reset"
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup"):
		return "dns failure"
	case strings.Contains(msg, "unreachable") || strings.Contains(msg, "no route to host") || strings.Contains(msg, "network is down"):
		return "network unreachable"
	case errors.Is(err, io.EOF) || strings.Contains(msg, "eof"):
		return "connection closed"
	case strings.Contains(msg, "tls") || strings.Contains(msg, "handshake") || strings.Contains(msg, "certificate"):
		return "handshake failed"
	}
	if text := scrubError(msg); text != "" {
		return "error: " + text
	}
	return "error"
}

const (
	scrubMaxWords = 12
	scrubMaxBytes = 80
	scrubAddr     = "<addr>"
)

// scrubError keeps the plain words of an error message and drops anything
// that could name a host, an address, a port, a path or a credential: a word
// made of anything but lowercase ASCII letters, a short number, "-" and "_" is
// replaced by "<addr>". It is best effort, for a log line, never for matching.
func scrubError(msg string) string {
	var words []string
	for _, tok := range strings.Fields(msg) {
		tok = strings.Trim(tok, ".,;:()\"'")
		if tok == "" {
			continue
		}
		if !plainWord(tok) {
			tok = scrubAddr
		}
		if tok == scrubAddr && len(words) > 0 && words[len(words)-1] == scrubAddr {
			continue
		}
		words = append(words, tok)
		if len(words) == scrubMaxWords {
			break
		}
	}
	out := strings.Join(words, " ")
	if len(out) > scrubMaxBytes {
		out = strings.TrimSpace(out[:scrubMaxBytes])
	}
	return out
}

// plainWord reports whether w is lowercase letters, "-" and "_", or a number of
// at most three digits.
func plainWord(w string) bool {
	digits := 0
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z', r == '-', r == '_':
		case r >= '0' && r <= '9':
			digits++
		default:
			return false
		}
	}
	return digits == 0 || (digits == len(w) && digits <= 3)
}

// Down reports whether the node is down: 2 consecutive probe failures or 3
// consecutive dial failures. A success of EITHER kind clears both counters.
func (h *nodeHealth) Down() bool { return h.downFlag.Load() }

func (h *nodeHealth) recordProbe(alive bool, delay time.Duration) {
	h.mu.Lock()
	changed := h.applyProbe(alive, delay)
	var ev NodeEvent
	if changed {
		ev = h.eventLocked()
	}
	h.mu.Unlock()
	h.announce(changed, ev)
}

// applyProbe is recordProbe under h.mu; it reports a change of the down state.
func (h *nodeHealth) applyProbe(alive bool, delay time.Duration) bool {
	if alive {
		h.consProbeFail = 0
		h.consDialFail = 0
		h.probeFail = ""
		h.lastOK = h.clock()
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
	var ev NodeEvent
	if changed {
		ev = h.eventLocked()
	}
	h.mu.Unlock()
	h.announce(changed, ev)
}

// applyDial is recordDial under h.mu; it reports a change of the down state.
func (h *nodeHealth) applyDial(ok bool, took time.Duration) bool {
	if ok {
		h.consDialFail = 0
		h.consProbeFail = 0
		h.lastOK = h.clock()
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
	notify func(NodeEvent)
	// now is the clock of the records; nil means time.Now.
	now func() time.Time
}

func newHealthTable() *healthTable {
	return &healthTable{entries: make(map[string]*nodeHealth)}
}

// register creates the records for the given labels before any traffic.
func (t *healthTable) register(labels []string) {
	for _, label := range labels {
		if _, ok := t.entries[label]; !ok {
			h := &nodeHealth{label: label, notify: t.notify, now: t.now}
			h.upSince.Store(h.clock().UnixNano())
			h.initHedgeBudget()
			t.entries[label] = h
		}
	}
}

// carryHealth builds the table of a new routing generation: a label that
// stays keeps its measurements, down state and hedge budget, the others start
// fresh. Nothing is announced, so a node that was down before stays down
// without a second report. notify is the table's new listener.
func carryHealth(old *healthTable, labels []string, notify func(NodeEvent)) *healthTable {
	t := newHealthTable()
	t.notify = notify
	if old != nil {
		t.now = old.now
	}
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
func (h *nodeHealth) cloneFor(notify func(NodeEvent)) *nodeHealth {
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
		lastOK:        h.lastOK,
		downSince:     h.downSince,
		probeFail:     h.probeFail,
		label:         h.label,
		notify:        notify,
		now:           h.now,
	}
	c.downFlag.Store(h.downFlag.Load())
	c.upSince.Store(h.upSince.Load())
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

// noteProbeError records the kind of a failed probe of a node before its
// result is recorded.
func (t *healthTable) noteProbeError(label string, err error) {
	if h := t.entries[label]; h != nil {
		h.noteProbeError(probeFailKind(err))
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
