package revive

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/check"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

// Ticker is the clock's stoppable interval source.
type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

// Clock supplies time and tickers to a Reviver.
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}

// Registration binds an immutable set of hops and their probe policy to a pool.
type Registration struct {
	Pool     *pool.Pool
	Hops     []dial.Hop
	Prober   check.Prober
	Eligible func(key string) bool
	Mutate   func(key string, change func())
}

// EventKind is the only information exposed by reviver events.
type EventKind uint8

const (
	// Quarantined reports a newly tripped hop or a failed recovery probe.
	Quarantined EventKind = iota + 1
	// Revived reports a successful recovery probe.
	Revived
)

// Event reports a quarantine or revival transition using only an opaque pool key.
type Event struct {
	Kind  EventKind
	Key   string
	Trips int
}

// Options configures probe cadence, deadlines, concurrency, clock, and notifications.
type Options struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Clock       Clock
	// OnEvent may be called concurrently by probe workers and is never called
	// while the reviver's mutation lock is held.
	OnEvent func(Event)
}

// Reviver schedules leased recovery probes for registered pools.
type Reviver struct {
	registrations []Registration
	options       Options
	clock         Clock
	gate          chan struct{}
	mutationMu    sync.Mutex
	cursors       []int
}

var errInvalidOptions = errors.New("invalid reviver options")

// New validates and snapshots registrations and options.
func New(registrations []Registration, options Options) (*Reviver, error) {
	if options.Interval <= 0 || options.Timeout <= 0 || options.Timeout >= 25*time.Second || options.Concurrency <= 0 {
		return nil, fmt.Errorf("%w: interval and concurrency must be positive; timeout must be below the 25-second probe lease", errInvalidOptions)
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	seen := make(map[*pool.Pool]struct{}, len(registrations))
	copied := make([]Registration, len(registrations))
	for i, registration := range registrations {
		if registration.Pool == nil || registration.Prober == nil {
			return nil, fmt.Errorf("%w: registration %d needs a pool and prober", errInvalidOptions, i)
		}
		if _, exists := seen[registration.Pool]; exists {
			return nil, fmt.Errorf("%w: pool registered more than once", errInvalidOptions)
		}
		seen[registration.Pool] = struct{}{}
		byKey := make(map[string]dial.Hop, len(registration.Hops))
		for _, hop := range registration.Hops {
			if hop == nil || hop.Key() == "" {
				return nil, fmt.Errorf("%w: registration %d has a nil hop or empty key", errInvalidOptions, i)
			}
			key := hop.Key()
			if _, exists := byKey[key]; exists {
				return nil, fmt.Errorf("%w: registration %d has duplicate hop key", errInvalidOptions, i)
			}
			byKey[key] = hop
		}
		if len(byKey) == 0 {
			return nil, fmt.Errorf("%w: registration %d has no hops", errInvalidOptions, i)
		}
		members := registration.Pool.Snapshot()
		if len(members) != len(byKey) {
			return nil, fmt.Errorf("%w: registration %d hops do not match its pool members", errInvalidOptions, i)
		}
		for _, member := range members {
			if _, exists := byKey[member.Key]; !exists {
				return nil, fmt.Errorf("%w: registration %d hops do not match its pool members", errInvalidOptions, i)
			}
		}
		registration.Hops = append([]dial.Hop(nil), registration.Hops...)
		copied[i] = registration
	}
	return &Reviver{
		registrations: copied,
		options:       options,
		clock:         options.Clock,
		gate:          make(chan struct{}, 1),
		cursors:       make([]int, len(copied)),
	}, nil
}

// Round probes due eligible hops and waits for every started probe to finish.
// DueProbes leases all due pool keys at once; claims skipped before a probe starts are
// released, while an inconclusive probe that ran retains its lease until expiry.
// Dispatch order rotates between rounds.
func (r *Reviver) Round(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sem := make(chan struct{}, r.options.Concurrency)
	var workers sync.WaitGroup
	for registrationIndex, registration := range r.registrations {
		if err := ctx.Err(); err != nil {
			workers.Wait()
			return err
		}
		// Determine whether at least one due hop is eligible before leasing.
		eligible := false
		for _, snapshot := range registration.Pool.Snapshot() {
			if snapshot.State == pool.Quarantined && !snapshot.NextProbe.After(r.clock.Now()) &&
				(registration.Eligible == nil || registration.Eligible(snapshot.Key)) {
				eligible = true
				break
			}
		}
		if !eligible {
			continue
		}
		leasedAt := r.clock.Now()
		claims := registration.Pool.DueProbes(leasedAt)
		r.rotateClaims(registrationIndex, claims)
		for i, claim := range claims {
			if registration.Eligible != nil && !registration.Eligible(claim.Key()) {
				r.release(registration, claim)
				continue
			}
			if err := ctx.Err(); err != nil {
				r.release(registration, claim)
				for _, remaining := range claims[i+1:] {
					r.release(registration, remaining)
				}
				workers.Wait()
				return err
			}
			// Bound the start time by the lease: after acquiring capacity, decline
			// claims that cannot finish before their original lease expires.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				r.release(registration, claim)
				for _, remaining := range claims[i+1:] {
					r.release(registration, remaining)
				}
				workers.Wait()
				return ctx.Err()
			}
			hop := findHop(registration.Hops, claim.Key())
			if hop == nil {
				r.release(registration, claim)
				<-sem
				continue
			}
			if registration.Eligible != nil && !registration.Eligible(claim.Key()) {
				r.release(registration, claim)
				<-sem
				continue
			}
			if r.clock.Now().Sub(leasedAt)+r.options.Timeout >= probeLeaseDuration {
				r.release(registration, claim)
				<-sem
				continue
			}
			workers.Add(1)
			go func(reg Registration, claimed pool.ProbeClaim, target dial.Hop, claimTime time.Time) {
				defer workers.Done()
				defer func() { <-sem }()
				if ctx.Err() != nil || (reg.Eligible != nil && !reg.Eligible(claimed.Key())) ||
					r.clock.Now().Sub(claimTime)+r.options.Timeout >= probeLeaseDuration {
					r.release(reg, claimed)
					return
				}
				probeCtx, cancel := context.WithTimeout(ctx, r.options.Timeout)
				if probeCtx.Err() != nil {
					cancel()
					r.release(reg, claimed)
					return
				}
				outcome := reg.Prober.Probe(probeCtx, target)
				if probeCtx.Err() != nil {
					outcome = pool.ProbeInconclusive
				}
				cancel()
				r.report(reg, claimed, outcome)
			}(registration, claim, hop, leasedAt)
		}
		r.cursors[registrationIndex] = (r.cursors[registrationIndex] + 1) % len(registration.Hops)
	}
	workers.Wait()
	return ctx.Err()
}

// Run blocks until cancellation, running one round immediately and then at each clock tick.
func (r *Reviver) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ticker := r.clock.NewTicker(r.options.Interval)
	defer ticker.Stop()
	if err := r.Round(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticker.Chan():
			if !ok {
				return nil
			}
			if err := r.Round(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

// Observe applies a caller-owned pool mutation and emits opaque transition events.
func (r *Reviver) Observe(p *pool.Pool, key string, change func()) {
	if p == nil || change == nil {
		return
	}
	for _, registration := range r.registrations {
		if registration.Pool != p {
			continue
		}
		var event Event
		mutate := func() {
			r.mutationMu.Lock()
			defer r.mutationMu.Unlock()
			before := snapshotState(p, key)
			change()
			after := snapshotState(p, key)
			if before == pool.Healthy && after != pool.Healthy {
				event = Event{Kind: Quarantined, Key: key, Trips: snapshotTrips(p, key)}
			} else if before != pool.Healthy && after == pool.Healthy {
				event = Event{Kind: Revived, Key: key, Trips: snapshotTrips(p, key)}
			}
		}
		if registration.Mutate != nil {
			registration.Mutate(key, mutate)
		} else {
			mutate()
		}
		if event.Kind != 0 {
			r.emit(event)
		}
		return
	}
	r.mutationMu.Lock()
	func() {
		defer r.mutationMu.Unlock()
		change()
	}()
}

const probeLeaseDuration = 25 * time.Second

func (r *Reviver) rotateClaims(registrationIndex int, claims []pool.ProbeClaim) {
	if len(claims) < 2 {
		return
	}
	hops := r.registrations[registrationIndex].Hops
	positions := make(map[string]int, len(hops))
	for i, hop := range hops {
		positions[hop.Key()] = i
	}
	start := r.cursors[registrationIndex]
	sort.SliceStable(claims, func(i, j int) bool {
		left := (positions[claims[i].Key()] - start + len(hops)) % len(hops)
		right := (positions[claims[j].Key()] - start + len(hops)) % len(hops)
		return left < right
	})
}

func (r *Reviver) release(reg Registration, claim pool.ProbeClaim) {
	mutate := func() {
		r.mutationMu.Lock()
		defer r.mutationMu.Unlock()
		reg.Pool.ReleaseProbe(claim)
	}
	if reg.Mutate != nil {
		reg.Mutate(claim.Key(), mutate)
	} else {
		mutate()
	}
}

func (r *Reviver) report(reg Registration, claim pool.ProbeClaim, outcome pool.ProbeOutcome) {
	var accepted bool
	var event Event
	mutate := func() {
		r.mutationMu.Lock()
		defer r.mutationMu.Unlock()
		accepted = reg.Pool.ReportProbe(claim, outcome)
		trips := snapshotTrips(reg.Pool, claim.Key())
		if !accepted {
			return
		}
		switch outcome {
		case pool.ProbeSuccess:
			event = Event{Kind: Revived, Key: claim.Key(), Trips: trips}
		case pool.ProbeHardFailure:
			event = Event{Kind: Quarantined, Key: claim.Key(), Trips: trips}
		}
	}
	if reg.Mutate != nil {
		reg.Mutate(claim.Key(), mutate)
	} else {
		mutate()
	}
	if accepted && event.Kind != 0 {
		r.emit(event)
	}
}

func (r *Reviver) emit(event Event) {
	if r.options.OnEvent != nil {
		r.options.OnEvent(event)
	}
}

func findHop(hops []dial.Hop, key string) dial.Hop {
	for _, hop := range hops {
		if hop.Key() == key {
			return hop
		}
	}
	return nil
}

func snapshotState(p *pool.Pool, key string) pool.State {
	state, _ := p.State(key)
	return state.State
}

func snapshotTrips(p *pool.Pool, key string) int {
	state, _ := p.State(key)
	return state.Trips
}

type realClock struct{}
type realTicker struct{ ticker *time.Ticker }

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(interval time.Duration) Ticker {
	return realTicker{ticker: time.NewTicker(interval)}
}
func (t realTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t realTicker) Stop()                  { t.ticker.Stop() }
