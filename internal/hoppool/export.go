package hoppool

import "time"

// Entry is a persisted quarantine record for one opaque hop key.
type Entry struct {
	// Key is the opaque member key.
	Key string
	// Trips is the number of quarantine trips, saturating only at max int.
	Trips int
	// NextProbe is the exported probe eligibility time.
	NextProbe time.Time
}

// Export returns only quarantined members, including members with active leases.
func (p *Pool) Export() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]Entry, 0)
	for _, key := range p.order {
		h := p.members[key]
		if h != nil && h.quarantined {
			entries = append(entries, Entry{Key: key, Trips: h.trips, NextProbe: h.nextProbe})
		}
	}
	return entries
}

// Restore replaces quarantine state, marks known entries due now, and clears leases.
func (p *Pool) Restore(entries []Entry, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range p.members {
		h.quarantined = false
		h.hard = 0
		h.trips = 0
		h.nextProbe = time.Time{}
		h.lease = nil
		h.leaseUntil = time.Time{}
	}
	for _, entry := range entries {
		h := p.members[entry.Key]
		if h == nil {
			continue
		}
		h.quarantined = true
		h.trips = clampTrips(entry.Trips)
		h.hard = hardTripThreshold
		h.nextProbe = now
	}
}

func clampTrips(trips int) int {
	if trips < 1 {
		return 1
	}
	return trips
}

// Reconcile updates unique members while retaining surviving health state.
func (p *Pool) Reconcile(keys []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	nextMembers := make(map[string]*hop, len(keys))
	nextOrder := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, exists := nextMembers[key]; exists {
			continue
		}
		h := p.members[key]
		if h == nil {
			h = &hop{}
		}
		nextMembers[key] = h
		nextOrder = append(nextOrder, key)
	}
	p.members = nextMembers
	p.order = nextOrder
}
