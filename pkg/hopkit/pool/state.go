package pool

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Strength classifies the contribution of an event to hop health.
type Strength uint8

const (
	// Inconclusive records no health change.
	Inconclusive Strength = iota
	// Soft increases the decaying sand score and the soft streak.
	Soft
	// Hard advances the consecutive-hard failure count.
	Hard
)

// Options configures the pool clock and ordering random source.
type Options struct {
	// Clock must be safe for concurrent calls; it is invoked outside the pool lock.
	// A nil Clock defaults to time.Now.
	Clock func() time.Time
	// RNG is copied by value; a nil RNG uses a new PCG.
	RNG *rand.PCG
}

// Pool tracks health and probe quarantine for opaque hop keys.
type Pool struct {
	mu      sync.Mutex
	members map[string]*hop
	order   []string
	clock   func() time.Time
	rng     *rand.Rand
}

const (
	hardTripThreshold = 3
	// softTripThreshold quarantines a hop whose failures are all ambiguous
	// (for example behind a lazy carrier that never reports a connect error).
	softTripThreshold = 5
)

type hop struct {
	sand        float64
	sandAt      time.Time
	sandSet     bool
	hard        int
	soft        int
	trips       int
	quarantined bool
	nextProbe   time.Time
	lease       *probeIdentity
	leaseUntil  time.Time
}

// New creates a pool with unique keys in their first-seen order.
func New(keys []string, options Options) *Pool {
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	var source *rand.PCG
	if options.RNG == nil {
		source = rand.NewPCG(rand.Uint64(), rand.Uint64())
	} else {
		copied := *options.RNG
		source = &copied
	}
	p := &Pool{
		members: make(map[string]*hop),
		clock:   clock,
		rng:     rand.New(source),
	}
	p.Reconcile(keys)
	return p
}

// Record applies a classified health event to a known hop.
func (p *Pool) Record(key string, strength Strength) {
	if strength == Inconclusive {
		return
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.members[key]
	if !ok {
		return
	}
	switch strength {
	case Soft:
		p.advanceSand(h, now)
		h.sand++
		if h.sand > maxSand {
			h.sand = maxSand
		}
		h.hard = 0
		if !h.quarantined {
			h.soft++
			if h.soft >= softTripThreshold {
				p.trip(h, now)
			}
		}
	case Hard:
		if h.quarantined {
			return
		}
		h.hard++
		if h.hard >= hardTripThreshold {
			p.trip(h, now)
		}
	}
}

// RecordHard records a hard failure for a known hop.
func (p *Pool) RecordHard(key string) { p.Record(key, Hard) }

// RecordSoft records a soft failure for a known hop.
func (p *Pool) RecordSoft(key string) { p.Record(key, Soft) }

// RecordSuccess revives a hop, halves its sand, and clears failure streaks.
func (p *Pool) RecordSuccess(key string) {
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if h := p.members[key]; h != nil {
		p.revive(h, now)
	}
}
