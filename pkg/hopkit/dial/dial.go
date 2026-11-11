// Package dial provides generic, health-aware dialing across pool-managed hops.
package dial

import (
	"context"
	"errors"
	"net"
	"time"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

// Hop is one opaque outbound hop capable of dialing its next address.
type Hop interface {
	Key() string
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Stage identifies the boundary where a hop dial failed.
type Stage int

const (
	// Connect means the selected hop itself could not be reached.
	Connect Stage = iota + 1
	// Handshake means a hop handshake failed ambiguously unless NextUnreachable is set.
	Handshake
	// Target means the hop completed its handshake and the next address failed.
	Target
	// Refusal means the selected hop explicitly refused authentication or policy.
	Refusal
	// Unknown means the error cannot be safely attributed.
	Unknown
)

// StageError carries structured, secret-free hop-failure attribution.
// Err is retained for errors.Is/errors.As by callers; it is never rendered.
type StageError struct {
	Stage           Stage
	NextUnreachable bool
	Err             error
}

// Error returns a fixed vocabulary and never exposes the underlying error text.
func (e *StageError) Error() string {
	if e == nil {
		return "hop dial: unknown failure"
	}
	switch e.Stage {
	case Connect:
		return "hop dial: connect failed"
	case Handshake:
		if e.NextUnreachable {
			return "hop dial: next hop unreachable"
		}
		return "hop dial: handshake failed"
	case Target:
		if e.NextUnreachable {
			return "hop dial: target unreachable"
		}
		return "hop dial: target failed"
	case Refusal:
		return "hop dial: refused"
	default:
		return "hop dial: unknown failure"
	}
}

// Unwrap exposes the original cause without including it in Error.
func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Binder is the site-affinity API used by post-hop dialers.
type Binder interface {
	Get(login, site string) (string, bool)
	Bind(login, site, key string)
	Unbind(login, site string)
	Forget(key string) int
}

// Options configures an immutable Dialer. Clock and Mutate must be safe for
// concurrent calls. Post enables site binding and one-hop/no-retry dialing.
type Options struct {
	Clock  func() time.Time
	Binder Binder
	Post   bool
	Mutate func(key string, change func())
}

// Dialer chooses from a health pool and records only structured hop failures.
type Dialer struct {
	pool   *pool.Pool
	hops   map[string]Hop
	clock  func() time.Time
	binder Binder
	post   bool
	mutate func(string, func())
}

// New constructs a dialer, copying hops and discarding duplicate keys after
// their first occurrence. Keys must be present in p. A nil clock defaults to time.Now.
func New(p *pool.Pool, hops []Hop, options Options) *Dialer {
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	d := &Dialer{
		pool: p, hops: make(map[string]Hop, len(hops)),
		clock: clock, binder: options.Binder, post: options.Post, mutate: options.Mutate,
	}
	for _, hop := range hops {
		if hop == nil {
			continue
		}
		key := hop.Key()
		if _, exists := d.hops[key]; exists {
			continue
		}
		d.hops[key] = hop
	}
	return d
}

// DialContext dials through the pool. Pre mode tries each unique hop at most
// once in pool order; Post mode makes a single choice and never bypasses it.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address, "", "")
}

// DialSite dials a site through the pool. With Post enabled, a healthy
// login/site binding wins; unhealthy bindings are removed before drawing a
// replacement. Post mode tries only the selected hop and never bypasses it.
func (d *Dialer) DialSite(ctx context.Context, network, address, login, site string) (net.Conn, error) {
	return d.dial(ctx, network, address, login, site)
}

func (d *Dialer) dial(ctx context.Context, network, address, login, site string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d == nil || d.pool == nil || len(d.hops) == 0 {
		return nil, ErrNoHops
	}
	order := d.pool.Order(d.clock())
	if d.post && login != "" && site != "" && d.binder != nil {
		if bound, ok := d.binder.Get(login, site); ok {
			if d.healthy(bound) {
				if _, exists := d.hops[bound]; exists {
					order = []string{bound}
				} else {
					d.binder.Unbind(login, site)
				}
			} else {
				d.binder.Unbind(login, site)
			}
		}
	}
	candidates := make([]Hop, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, key := range order {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		hop, exists := d.hops[key]
		if !exists {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, hop)
	}
	if len(candidates) == 0 {
		return nil, ErrNoHops
	}
	if d.post && len(candidates) > 1 {
		candidates = candidates[:1]
	}
	var last error
	for _, hop := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := hop.DialContext(ctx, network, address)
		if ctx.Err() != nil {
			if conn != nil {
				_ = conn.Close()
			}
			if err != nil {
				return nil, err
			}
			return nil, ctx.Err()
		}
		if err == nil && conn == nil {
			err = ErrNoConnection
		}
		if err == nil {
			d.change(hop.Key(), func() { d.pool.RecordSuccess(hop.Key()) })
			if d.post && d.binder != nil && login != "" && site != "" {
				d.binder.Bind(login, site, hop.Key())
			}
			return conn, nil
		}
		last = err
		if conn != nil {
			_ = conn.Close()
		}
		d.recordFailure(ctx, hop.Key(), err)
		if d.post {
			return nil, err
		}
	}
	return nil, last
}

// ErrNoHops is returned if the configured pool has no dialable candidates.
var ErrNoHops = errors.New("hop dial: no candidates")

// ErrNoConnection is returned when a Hop reports success without a connection.
var ErrNoConnection = errors.New("hop dial: no connection returned")

func (d *Dialer) healthy(key string) bool {
	for _, snapshot := range d.pool.Snapshot() {
		if snapshot.Key == key {
			return snapshot.State == pool.Healthy
		}
	}
	return false
}

func (d *Dialer) change(key string, change func()) {
	if d.mutate != nil {
		d.mutate(key, change)
		return
	}
	change()
}

func (d *Dialer) recordFailure(ctx context.Context, key string, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	var staged *StageError
	if !errors.As(err, &staged) || staged == nil {
		return
	}
	if staged.Err != nil && (errors.Is(staged, context.Canceled) || errors.Is(staged, context.DeadlineExceeded)) {
		return
	}
	stage, kind := poolStage(staged.Stage), pool.UnknownKind
	switch staged.Stage {
	case Connect:
		kind = pool.Unreachable
	case Handshake:
		if staged.NextUnreachable {
			kind = pool.NextUnreachable
		} else {
			kind = pool.Ambiguous
		}
	case Target:
		if staged.NextUnreachable {
			kind = pool.NextUnreachable
		}
	case Refusal:
		kind = pool.AuthPolicy
	case Unknown:
		return
	default:
		return
	}
	for _, assessment := range pool.Blame(stage, kind) {
		if assessment.Role != pool.CurrentHop || assessment.Strength == pool.Inconclusive {
			continue
		}
		strength := assessment.Strength
		d.change(key, func() { d.pool.Record(key, strength) })
	}
}

func poolStage(stage Stage) pool.Stage {
	switch stage {
	case Connect:
		return pool.Connect
	case Handshake:
		return pool.Handshake
	case Target:
		return pool.Target
	case Refusal:
		return pool.Refusal
	default:
		return pool.UnknownStage
	}
}
