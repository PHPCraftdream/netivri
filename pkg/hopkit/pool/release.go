package pool

import "time"

// ReleaseProbe clears only the current, unexpired lease without changing hop health.
// It returns false for zero, foreign, stale, or expired claims.
func (p *Pool) ReleaseProbe(claim ProbeClaim) bool {
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
	h.lease = nil
	h.leaseUntil = time.Time{}
	return true
}
