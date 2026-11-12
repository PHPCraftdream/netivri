package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/proxyclient"
	"github.com/metacubex/mihomo/adapter/outbound"
	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/dialer"
	C "github.com/metacubex/mihomo/constant"
)

type hopPoolDialError struct{ cause error }
type poolCarrierError struct{ cause error }

func (e *hopPoolDialError) Error() string { return "hop pool dial failed" }
func (e *hopPoolDialError) Unwrap() error { return e.cause }
func (e *poolCarrierError) Error() string { return "carrier dial failed" }
func (e *poolCarrierError) Unwrap() error { return e.cause }

func isHopPoolDialError(err error) bool {
	var target *hopPoolDialError
	if errors.As(err, &target) {
		return true
	}
	// Some Mihomo adapters format wrapped errors with %s; this marker is fixed and sanitized.
	return err != nil && strings.Contains(err.Error(), "hop pool dial failed")
}

type mihomoPoolHop struct {
	key           string
	role          hopPoolRole
	address       string
	protocol      string
	proxyConfig   proxyclient.Proxy
	proxyParseErr error
	carrierMember member
	directCarrier bool
	nodeAddress   string
	carrier       *nodeHealth
	now           func() time.Time
}

func (h *mihomoPoolHop) Key() string { return h.key }

func (h *mihomoPoolHop) DialContext(ctx context.Context, _, target string) (net.Conn, error) {
	if h == nil {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: errors.New("hop proxy unavailable")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	if h.proxyParseErr != nil {
		// A hop the native client cannot use is out of service: count it as a
		// connect failure so the pool quarantines it and tries another hop.
		return nil, &hopdial.StageError{Stage: hopdial.Connect, Err: errUnusableHop}
	}

	var conn net.Conn
	var err error
	var chain C.Chain
	var providerChain C.Chain
	negotiationEnds := time.Now().Add(hopNegotiationBudget)
	if h.role == postHopPool && !h.directCarrier {
		if h.carrierMember == nil {
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: &poolCarrierError{cause: errors.New("carrier unavailable")}}
		}
		metadata := &C.Metadata{Type: C.INNER, NetWork: C.TCP, Host: h.proxyConfig.Host, DstPort: uint16(h.proxyConfig.Port)}
		conn, err = h.carrierMember.DialContext(ctx, metadata)
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: &poolCarrierError{cause: err}}
		}
		if conn == nil {
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: &poolCarrierError{cause: errors.New("carrier connection unavailable")}}
		}
		carrierConn, ok := conn.(C.Conn)
		if !ok {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: &poolCarrierError{cause: errors.New("carrier connection metadata unavailable")}}
		}
		chain = append(C.Chain(nil), carrierConn.Chains()...)
		providerChain = append(C.Chain(nil), carrierConn.ProviderChains()...)
	} else if h.role == postHopPool && h.directCarrier {
		connectCtx, cancel := context.WithDeadline(ctx, negotiationEnds)
		directConn, directErr := directPostCarrier.DialContext(connectCtx, proxyCarrierMetadata(h.proxyConfig))
		expired := connectCtx.Err() != nil
		cancel()
		conn, err = directConn, directErr
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, connectFailure(ctx, expired, err)
		}
		if directConn == nil {
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: errors.New("proxy connection unavailable")}
		}
		chain = append(C.Chain(nil), directConn.Chains()...)
		providerChain = append(C.Chain(nil), directConn.ProviderChains()...)
	} else {
		// Pre-hop proxies intentionally use the bare dialer, without DIRECT policy.
		connectCtx, cancel := context.WithDeadline(ctx, negotiationEnds)
		conn, err = dialer.NewDialer().DialContext(connectCtx, "tcp", net.JoinHostPort(h.proxyConfig.Host, strconv.Itoa(h.proxyConfig.Port)))
		expired := connectCtx.Err() != nil
		cancel()
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, connectFailure(ctx, expired, err)
		}
	}
	if conn == nil {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: errors.New("proxy connection unavailable")}
	}
	// A connect that used the whole budget leaves the negotiation a token
	// moment, not an unbounded one.
	negotiation := max(time.Until(negotiationEnds), time.Millisecond)
	tunnel, err := proxyclient.Handshake(ctx, conn, h.proxyConfig, target,
		proxyclient.WithNegotiationTimeout(negotiation))
	if err != nil {
		_ = conn.Close()
		var proxyErr *proxyclient.Error
		if errors.As(err, &proxyErr) {
			stagedErr := stageProxyError(ctx, proxyErr.Stage, err, proxyErr.TargetSent)
			if h.role == postHopPool && !h.directCarrier && !recentNodeEvidence(h.carrier, h.clockNow()) {
				var staged *hopdial.StageError
				if errors.As(stagedErr, &staged) && staged != nil {
					staged.SuppressBlame = true
				}
			}
			return nil, stagedErr
		}
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	return &nativePoolConn{ExtendedConn: N.NewDeadlineConn(tunnel), chain: chain, providerChain: providerChain}, nil
}

// hopNegotiationBudget bounds one hop attempt up to the moment the target
// request is sent: connecting to the hop, TLS setup and SOCKS5 negotiation. A
// silent hop then fails while the connection's own deadline (Mihomo's 5 s)
// still leaves time to try another hop, and the failure counts against the
// hop instead of being lost as a context timeout.
var hopNegotiationBudget = 3 * time.Second

// errUnusableHop reports a hop URL the native proxy client rejects.
var errUnusableHop = errors.New("hop URL is not usable by the native proxy client")

// errHopConnectTimeout replaces the context error of a connect that ran out of
// the negotiation budget, so the dialer does not mistake it for cancellation.
var errHopConnectTimeout = errors.New("hop connect timed out")

// connectFailure classifies a failed connect to the hop itself.
func connectFailure(ctx context.Context, budgetExpired bool, err error) error {
	if budgetExpired && ctx.Err() == nil {
		return &hopdial.StageError{Stage: hopdial.Connect, Err: errHopConnectTimeout}
	}
	return stageProxyError(ctx, proxyclient.StageConnect, err, false)
}

func proxyCarrierMetadata(proxy proxyclient.Proxy) *C.Metadata {
	metadata := &C.Metadata{Type: C.INNER, NetWork: C.TCP}
	_ = metadata.SetRemoteAddress(net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port)))
	return metadata
}

// A stable DIRECT adapter preserves its resolver and loopback-detector policy
// across direct-post dials, as with Mihomo's built-in shared DIRECT outbound.
var directPostCarrier = outbound.NewDirect()

func stageProxyError(ctx context.Context, stage proxyclient.Stage, err error, targetSent bool) error {
	if ctx != nil && ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isCapacityError(err) {
		return &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	staged := &hopdial.StageError{Err: err, TargetSent: targetSent}
	switch stage {
	case proxyclient.StageConnect:
		staged.Stage = hopdial.Connect
	case proxyclient.StageHandshake:
		staged.Stage = hopdial.Handshake
	case proxyclient.StageRefused:
		staged.Stage = hopdial.Refusal
	case proxyclient.StageTarget:
		staged.Stage, staged.NextUnreachable = hopdial.Target, true
	default:
		staged.Stage = hopdial.Unknown
	}
	return staged
}

type nativePoolConn struct {
	N.ExtendedConn
	chain         C.Chain
	providerChain C.Chain
}

func (c *nativePoolConn) poolChains() (C.Chain, C.Chain) {
	return append(C.Chain(nil), c.chain...), append(C.Chain(nil), c.providerChain...)
}
func (c *nativePoolConn) Chains() C.Chain         { return c.chain }
func (c *nativePoolConn) ProviderChains() C.Chain { return c.providerChain }
func (c *nativePoolConn) AppendToChains(adapter C.ProxyAdapter) {
	c.chain = append(c.chain, adapter.Name())
	c.providerChain = append(c.providerChain, adapter.ProxyInfo().ProviderName)
}
func (c *nativePoolConn) RemoteDestination() string {
	if remote := c.RemoteAddr(); remote != nil {
		metadata := C.Metadata{}
		if metadata.SetRemoteAddr(remote) == nil && metadata.Valid() {
			return metadata.String()
		}
	}
	return ""
}

type poolConnectionMetadata interface {
	poolChains() (C.Chain, C.Chain)
}

func newPoolConn(conn net.Conn, adapter C.ProxyAdapter) C.Conn {
	wrapped := &nativePoolConn{ExtendedConn: N.NewDeadlineConn(conn)}
	if metadata, ok := conn.(poolConnectionMetadata); ok {
		wrapped.chain, wrapped.providerChain = metadata.poolChains()
	}
	wrapped.AppendToChains(adapter)
	return wrapped
}

func (h *mihomoPoolHop) clockNow() time.Time {
	if h != nil && h.now != nil {
		return h.now()
	}
	return time.Now()
}

type hopPoolAdapter struct {
	*outbound.Base
	role    hopPoolRole
	pool    *hoppool.Pool
	dialer  *hopdial.Dialer
	hops    []*mihomoPoolHop
	keys    []string
	binder  siteBinder
	carrier *nodeHealth
	now     func() time.Time
	mutate  func(key string, change func())
}

func newHopPoolAdapter(name string, role hopPoolRole, spec *hopPoolSpec, healthPool *hoppool.Pool, binder siteBinder, carrier *nodeHealth, now func() time.Time) *hopPoolAdapter {
	if now == nil {
		now = time.Now
	}
	a := &hopPoolAdapter{
		Base: outbound.NewBase(outbound.BaseOption{Name: name, Type: C.Selector}), role: role,
		pool: healthPool, keys: append([]string(nil), spec.keys...),
		binder: binder, carrier: carrier, now: now,
	}
	for index, key := range spec.keys {
		uri := spec.uris[index]
		config, parseErr := proxyclient.Parse(uri)
		parsed, _ := url.Parse(uri)
		a.hops = append(a.hops, &mihomoPoolHop{
			key: key, role: role, address: hopAddress(uri), protocol: strings.ToLower(parsed.Scheme),
			proxyConfig: config, proxyParseErr: parseErr, carrier: carrier, now: now,
		})
	}
	a.dialer = hopdial.New(healthPool, a.hopInterfaces(), hopdial.Options{
		Clock: now, Binder: binder, Post: role == postHopPool,
		Mutate: func(key string, change func()) { a.applyChange(key, change) },
	})
	return a
}

func (a *hopPoolAdapter) hopInterfaces() []hopdial.Hop {
	hops := make([]hopdial.Hop, len(a.hops))
	for index, hop := range a.hops {
		hops[index] = hop
	}
	return hops
}

func (a *hopPoolAdapter) applyChange(key string, change func()) {
	if a.mutate != nil {
		a.mutate(key, change)
		return
	}
	change()
}

func (a *hopPoolAdapter) SupportUOT() bool                 { return false }
func (a *hopPoolAdapter) IsL3Protocol(*C.Metadata) bool    { return false }
func (a *hopPoolAdapter) Close() error                     { return a.Base.Close() }
func (a *hopPoolAdapter) Unwrap(*C.Metadata, bool) C.Proxy { return nil }
func (a *hopPoolAdapter) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": a.Type().String(), "all": append([]string(nil), a.keys...)})
}
func (a *hopPoolAdapter) SupportUDP() bool { return false }

// anonymousBindLogin keys the site bindings of connections without a login
// (the global default route): one shared namespace by site only. Account
// logins cannot contain NUL, so it never collides with a real one.
const anonymousBindLogin = "\x00anonymous"

func bindLogin(login string) string {
	if login == "" {
		return anonymousBindLogin
	}
	return login
}

func (a *hopPoolAdapter) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &hopPoolDialError{cause: err}
	}
	if metadata == nil {
		return nil, &hopPoolDialError{cause: errors.New("hop pool metadata unavailable")}
	}
	var conn net.Conn
	var err error
	if a.role == postHopPool {
		conn, err = a.dialer.DialSite(ctx, metadata.NetWork.String(), metadata.RemoteAddress(), bindLogin(metadata.InUser), siteKey(metadata.Host, metadata.DstIP))
	} else {
		conn, err = a.dialer.DialContext(ctx, metadata.NetWork.String(), metadata.RemoteAddress())
	}
	if err != nil {
		return nil, a.poolFailure(err)
	}
	if conn == nil {
		return nil, &hopPoolDialError{cause: errors.New("hop pool returned a nil connection")}
	}
	return newPoolConn(conn, a), nil
}

func (a *hopPoolAdapter) poolFailure(err error) error {
	if err == nil {
		return nil
	}
	var carrier *poolCarrierError
	if errors.As(err, &carrier) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isCapacityError(err) {
			return &hopPoolDialError{cause: err}
		}
		return carrier
	}
	var staged *hopdial.StageError
	if errors.As(err, &staged) && a.role == preHopPool && staged.Stage == hopdial.Target && staged.NextUnreachable {
		return err
	}
	return &hopPoolDialError{cause: err}
}

func (a *hopPoolAdapter) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	return nil, &hopPoolDialError{cause: errors.New("hop pool packet dialing is unsupported")}
}

func recentNodeEvidence(health *nodeHealth, now time.Time) bool {
	if health == nil || health.Down() {
		return false
	}
	if last := health.lastOKAt(); !last.IsZero() && !now.Before(last) && now.Sub(last) <= trafficAliveFor {
		return true
	}
	lastTraffic := health.lastTraffic.Load()
	return lastTraffic != 0 && now.UnixNano() >= lastTraffic && now.Sub(time.Unix(0, lastTraffic)) <= trafficAliveFor
}

// isCapacityError recognizes local resource exhaustion: typed errnos first,
// then the texts of Mihomo errors that carry no type.
func isCapacityError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOBUFS) {
		return true
	}
	if isCapacityFailure(strings.ToLower(err.Error())) {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if isCapacityError(cause) {
				return true
			}
		}
		return false
	}
	return isCapacityError(errors.Unwrap(err))
}

func isCapacityFailure(message string) bool {
	for _, marker := range []string{"capacity", "too many open files", "connection limit", "max connections", "maximum connections"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

var _ C.ProxyAdapter = (*hopPoolAdapter)(nil)
var _ hopdial.Hop = (*mihomoPoolHop)(nil)
var _ C.Conn = (*nativePoolConn)(nil)
var _ net.Conn = (*nativePoolConn)(nil)
