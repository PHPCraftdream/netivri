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
	// TargetSent is true if the target request may have been sent.
	TargetSent bool
	// SuppressBlame retains the known stage for retry decisions without
	// recording a health failure against the selected hop.
	SuppressBlame bool
	Err           error
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
// concurrent calls. Post enables site binding and retry of typed Connect,
// Handshake or Refusal failures only when the target request may not have
// been sent and the next address is not known to be unreachable.
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
// once in pool order and stops once maxNextUnreachable hops have reported the
// next address unreachable. Post mode retries typed Connect, Handshake or
// Refusal failures only when NextUnreachable and TargetSent are both false; it
// never bypasses the configured pool.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address, "", "")
}

// DialSite dials a site through the pool. With Post enabled, a healthy
// login/site binding is tried first and retained as a candidate for safe
// retries; unhealthy bindings are removed before drawing a replacement. Post
// mode retries typed Connect, Handshake or Refusal failures only when
// NextUnreachable and TargetSent are both false, and never bypasses the
// configured pool.
func (d *Dialer) DialSite(ctx context.Context, network, address, login, site string) (net.Conn, error) {
	return d.dial(ctx, network, address, login, site)
}

// nonNilContext returns ctx, or context.Background() when ctx is nil.
func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (d *Dialer) dial(ctx context.Context, network, address, login, site string) (net.Conn, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d == nil || d.pool == nil || len(d.hops) == 0 {
		return nil, ErrNoHops
	}
	order := d.pool.Order(d.clock())
	candidates := candidateHops(d.hops, order, d.boundKey(login, site))
	if len(candidates) == 0 {
		return nil, ErrNoHops
	}
	return d.tryCandidates(ctx, candidates, network, address, login, site)
}

// boundKey returns the hop bound to login at site while it is healthy and configured; a stale binding is removed.
func (d *Dialer) boundKey(login, site string) string {
	if !d.post || login == "" || site == "" || d.binder == nil {
		return ""
	}
	bound, ok := d.binder.Get(login, site)
	if !ok {
		return ""
	}
	if d.healthy(bound) {
		if _, exists := d.hops[bound]; exists {
			return bound
		}
	}
	d.binder.Unbind(login, site)
	return ""
}

// candidateHops lists the hops to try: the bound hop first, then the pool order, each hop once.
func candidateHops(hops map[string]Hop, order []string, boundKey string) []Hop {
	candidates := make([]Hop, 0, len(order))
	seen := make(map[string]struct{}, len(order)+1)
	if boundKey != "" {
		seen[boundKey] = struct{}{}
		candidates = append(candidates, hops[boundKey])
	}
	for _, key := range order {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		hop, exists := hops[key]
		if !exists {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, hop)
	}
	return candidates
}

// tryCandidates dials the candidates in order and returns the first connection that succeeds.
func (d *Dialer) tryCandidates(ctx context.Context, candidates []Hop, network, address, login, site string) (net.Conn, error) {
	var last error
	nextUnreachable := 0
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
			d.recordSuccess(hop, login, site)
			return conn, nil
		}
		last = err
		if conn != nil {
			_ = conn.Close()
		}
		d.recordFailure(ctx, hop.Key(), err)
		if d.post && !retryablePostFailure(err) {
			return nil, err
		}
		if !d.post && reportsNextUnreachable(err) {
			// The next address is likely down for everyone: do not multiply
			// the load by trying every pre hop against it.
			if nextUnreachable++; nextUnreachable >= maxNextUnreachable {
				return nil, err
			}
		}
	}
	return nil, last
}

// recordSuccess marks hop healthy and, for a post dial, binds login at site to it.
func (d *Dialer) recordSuccess(hop Hop, login, site string) {
	d.change(hop.Key(), func() { d.pool.RecordSuccess(hop.Key()) })
	if d.post && d.binder != nil && login != "" && site != "" {
		d.binder.Bind(login, site, hop.Key())
	}
}

// maxNextUnreachable bounds the pre hops tried once hops report the next
// address unreachable.
const maxNextUnreachable = 2

func reportsNextUnreachable(err error) bool {
	var staged *StageError
	return errors.As(err, &staged) && staged != nil && staged.NextUnreachable
}

// ErrNoHops is returned if the configured pool has no dialable candidates.
var ErrNoHops = errors.New("hop dial: no candidates")

// ErrNoConnection is returned when a Hop reports success without a connection.
var ErrNoConnection = errors.New("hop dial: no connection returned")

// retryablePostFailure permits retry only when a typed failure proves the
// selected hop failed or refused before the target request could have reached it.
func retryablePostFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var staged *StageError
	if !errors.As(err, &staged) || staged == nil || staged.NextUnreachable || staged.TargetSent {
		return false
	}
	return staged.Stage == Connect || staged.Stage == Handshake || staged.Stage == Refusal
}

func (d *Dialer) healthy(key string) bool {
	state, ok := d.pool.State(key)
	return ok && state.State == pool.Healthy
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
	if staged.SuppressBlame {
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
