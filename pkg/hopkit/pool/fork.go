package pool

import (
	"math/rand/v2"
	"time"
)

// Fork creates an independent pool for keys, carrying forward health state for
// keys shared with p. Probe leases stay with the original pool and are not
// copied. A nil receiver creates a new pool with default options.
func (p *Pool) Fork(keys []string) *Pool {
	if p == nil {
		return New(keys, Options{})
	}

	var (
		clock  func() time.Time
		seed1  uint64
		seed2  uint64
		states = make(map[string]hop, len(keys))
	)

	p.mu.Lock()
	clock = p.clock
	seed1 = p.rng.Uint64()
	seed2 = p.rng.Uint64()
	for _, key := range keys {
		if _, copied := states[key]; copied {
			continue
		}
		if h := p.members[key]; h != nil {
			state := *h
			state.lease = nil
			state.leaseUntil = time.Time{}
			states[key] = state
		}
	}
	p.mu.Unlock()

	fork := New(keys, Options{Clock: clock, RNG: rand.NewPCG(seed1, seed2)})
	fork.mu.Lock()
	for key, state := range states {
		if h := fork.members[key]; h != nil {
			*h = state
		}
	}
	fork.mu.Unlock()
	return fork
}
