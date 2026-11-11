package pool

import (
	"math"
	"sort"
	"time"
)

// Order returns a weighted random permutation, preferring healthy hops when any exist.
func (p *Pool) Order(now time.Time) []string {
	p.mu.Lock()
	healthy := false
	for _, key := range p.order {
		if h := p.members[key]; h != nil && !h.quarantined {
			healthy = true
			break
		}
	}
	candidates := make([]weightedKey, 0, len(p.order))
	for _, key := range p.order {
		h := p.members[key]
		if h == nil || healthy && h.quarantined {
			continue
		}
		score := -math.Log(1-p.rng.Float64()) / weight(effectiveSand(h, now))
		candidates = append(candidates, weightedKey{key: key, score: score})
	}
	p.mu.Unlock()

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score < candidates[j].score
	})
	result := make([]string, len(candidates))
	for i, candidate := range candidates {
		result[i] = candidate.key
	}
	return result
}

type weightedKey struct {
	key   string
	score float64
}

// State describes a hop's routing and recovery state.
type State string

const (
	// Healthy means the hop is not quarantined.
	Healthy State = "healthy"
	// Quarantined means the hop is quarantined and has no active probe lease.
	Quarantined State = "quarantined"
	// Probing means the hop is quarantined with an unexpired probe lease.
	Probing State = "probing"
)

// HopSnapshot reports the observable health state of one member.
type HopSnapshot struct {
	// Key is the opaque member key.
	Key string
	// State is healthy, quarantined, or probing.
	State State
	// Sand is the effective exponentially decayed soft-failure score.
	Sand float64
	// Weight is the routing weight derived from Sand.
	Weight float64
	// HardFailures is the current consecutive-hard count.
	HardFailures int
	// Trips is the quarantine-trip count, saturating only at max int.
	Trips int
	// NextProbe is the earliest time a probe may be leased.
	NextProbe time.Time
}

// Snapshot returns member health in stable insertion order.
func (p *Pool) Snapshot() []HopSnapshot {
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]HopSnapshot, 0, len(p.order))
	for _, key := range p.order {
		h := p.members[key]
		if h == nil {
			continue
		}
		sand := effectiveSand(h, now)
		state := Healthy
		if h.quarantined {
			state = Quarantined
			if h.lease != nil && now.Before(h.leaseUntil) {
				state = Probing
			}
		}
		result = append(result, HopSnapshot{
			Key: key, State: state,
			Sand: sand, Weight: weight(sand),
			HardFailures: h.hard, Trips: h.trips, NextProbe: h.nextProbe,
		})
	}
	return result
}
