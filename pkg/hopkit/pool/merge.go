package pool

import (
	"math/rand/v2"
	"time"
)

// ForkFrom creates an independent pool for keys, carrying forward health for
// shared keys. When sources disagree about a key, the first source containing
// it wins; health histories are not combined. Probe leases stay with their
// original pools. Sources are inspected one at a time.
func ForkFrom(keys []string, sources ...*Pool) *Pool {
	var (
		clock   func() time.Time
		seed1   uint64
		seed2   uint64
		haveRNG bool
		states  = make(map[string]hop, len(keys))
	)

	for _, source := range sources {
		if source == nil {
			continue
		}
		source.mu.Lock()
		if clock == nil && source.clock != nil {
			clock = source.clock
		}
		if !haveRNG && source.rng != nil {
			seed1 = source.rng.Uint64()
			seed2 = source.rng.Uint64()
			haveRNG = true
		}
		for _, key := range keys {
			if _, copied := states[key]; copied {
				continue
			}
			if h := source.members[key]; h != nil {
				state := *h
				state.lease = nil
				state.leaseUntil = time.Time{}
				states[key] = state
			}
		}
		source.mu.Unlock()
	}

	options := Options{Clock: clock}
	if haveRNG {
		options.RNG = rand.NewPCG(seed1, seed2)
	}
	fork := New(keys, options)
	fork.mu.Lock()
	for key, state := range states {
		if h := fork.members[key]; h != nil {
			*h = state
		}
	}
	fork.mu.Unlock()
	return fork
}
