package pool

import "time"

const (
	initialBackoff    = 30 * time.Second
	maxBackoff        = 600 * time.Second
	probeLease        = 25 * time.Second
	cappedBackoffTrip = 6 // 30, 60, 120, 240, 480, then the 600-second cap.
)

type probeIdentity struct {
	owner *Pool
	key   string
}

// ProbeClaim is an opaque, single-generation identity for one leased probe.
type ProbeClaim struct {
	identity *probeIdentity
}

// Key returns the opaque hop key associated with the claim, or an empty string for a zero claim.
func (c ProbeClaim) Key() string {
	if c.identity == nil {
		return ""
	}
	return c.identity.key
}

// ProbeOutcome classifies a leased probe result.
type ProbeOutcome uint8

const (
	// ProbeSuccess revives the hop.
	ProbeSuccess ProbeOutcome = iota + 1
	// ProbeHardFailure immediately quarantines the hop for its next backoff.
	ProbeHardFailure
	// ProbeInconclusive leaves the lease active until it expires.
	ProbeInconclusive
)

// DueProbes leases one eligible quarantined hop probe for 25 seconds.
func (p *Pool) DueProbes(now time.Time) []ProbeClaim {
	p.mu.Lock()
	defer p.mu.Unlock()
	claims := make([]ProbeClaim, 0)
	for _, key := range p.order {
		h := p.members[key]
		if h == nil || !h.quarantined || now.Before(h.nextProbe) {
			continue
		}
		if h.lease != nil {
			if now.Before(h.leaseUntil) {
				continue
			}
			h.lease = nil
			h.leaseUntil = time.Time{}
		}
		identity := &probeIdentity{owner: p, key: key}
		h.lease = identity
		h.leaseUntil = now.Add(probeLease)
		claims = append(claims, ProbeClaim{identity: identity})
	}
	return claims
}

// ReportProbe applies an outcome only to its current, unexpired lease.
func (p *Pool) ReportProbe(claim ProbeClaim, outcome ProbeOutcome) bool {
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	identity := claim.identity
	if identity == nil || identity.owner != p {
		return false
	}
	h := p.members[identity.key]
	if h == nil || h.lease != identity || !h.quarantined || !now.Before(h.leaseUntil) {
		return false
	}
	switch outcome {
	case ProbeSuccess:
		p.revive(h, now)
		return true
	case ProbeHardFailure:
		h.lease = nil
		h.leaseUntil = time.Time{}
		p.trip(h, now)
		return true
	case ProbeInconclusive:
		return true
	default:
		return false
	}
}

func (p *Pool) trip(h *hop, now time.Time) {
	if h.trips < int(^uint(0)>>1) {
		h.trips++
	}
	h.hard = hardTripThreshold
	h.soft = 0
	h.quarantined = true
	h.lease = nil
	h.leaseUntil = time.Time{}
	h.nextProbe = now.Add(backoff(h.trips))
}

func backoff(trips int) time.Duration {
	if trips < 1 {
		trips = 1
	}
	if trips >= cappedBackoffTrip {
		return maxBackoff
	}
	return initialBackoff << uint(trips-1)
}

func (p *Pool) revive(h *hop, now time.Time) {
	p.advanceSand(h, now)
	h.sand *= 0.5
	h.hard = 0
	h.soft = 0
	h.trips = 0
	h.quarantined = false
	h.nextProbe = time.Time{}
	h.lease = nil
	h.leaseUntil = time.Time{}
}
