package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/metacubex/mihomo/adapter/outbound"
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
	// Mihomo's VLESS and VMess adapters format some outbound errors with %s,
	// discarding the typed wrapper. The marker is our fixed, sanitized error
	// vocabulary, so it safely preserves attribution across that boundary.
	return err != nil && strings.Contains(err.Error(), "hop pool dial failed")
}

type poolMetadataContextKey struct{}

type poolAttemptMetadata struct {
	metadata     *C.Metadata
	nextAddress  string
	nextProtocol string
}

type mihomoPoolHop struct {
	key           string
	member        member
	proxy         C.Proxy
	metadata      *C.Metadata
	role          hopPoolRole
	address       string
	protocol      string
	nextAddress   string
	nextProtocol  string
	carrier       *nodeHealth
	directCarrier bool
	now           func() time.Time
}

func (h *mihomoPoolHop) Key() string { return h.key }

func (h *mihomoPoolHop) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if h == nil || h.member == nil {
		return nil, errors.New("hop proxy unavailable")
	}
	metadata := h.metadata
	nextAddress, nextProtocol := h.nextAddress, h.nextProtocol
	if ctx != nil {
		if attempt, ok := ctx.Value(poolMetadataContextKey{}).(poolAttemptMetadata); ok {
			metadata = attempt.metadata
			if attempt.nextAddress != "" {
				nextAddress = attempt.nextAddress
			}
			if attempt.nextProtocol != "" {
				nextProtocol = attempt.nextProtocol
			}
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	conn, err := h.member.DialContext(ctx, clonePoolMetadata(metadata))
	if err == nil {
		if conn == nil {
			return nil, errors.New("hop proxy returned a nil connection")
		}
		return conn, nil
	}
	if conn != nil {
		_ = conn.Close()
	}
	if isHopPoolDialError(err) {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	if ctx.Err() != nil || isIgnoredPoolFailure(err) {
		return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
	}
	if h.role == postHopPool {
		if poolErrorNamesAddress(err, h.nextAddress) {
			return nil, &poolCarrierError{cause: err}
		}
		if !h.directCarrier && !recentNodeEvidence(h.carrier, h.clockNow()) {
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
		}
		if stage, carrierError, matched := postCarrierFailure(err, h.address, h.protocol, nextProtocol); matched {
			if carrierError {
				if stage == hopdial.Unknown {
					return nil, &poolCarrierError{cause: err}
				}
				return nil, &hopdial.StageError{Stage: stage, Err: err}
			}
			return nil, &hopdial.StageError{Stage: hopdial.Unknown, Err: err}
		}
	}
	stage, kind, known := classifyPoolFailureAt(err, nextAddress, h.address, h.protocol, nextProtocol)
	if !known {
		return nil, err
	}
	staged := &hopdial.StageError{Err: err}
	switch {
	case stage == hoppool.Connect && kind == hoppool.Unreachable:
		staged.Stage = hopdial.Connect
	case stage == hoppool.Handshake && kind == hoppool.Ambiguous:
		staged.Stage = hopdial.Handshake
	case (stage == hoppool.Handshake || stage == hoppool.Target) && kind == hoppool.NextUnreachable:
		staged.Stage, staged.NextUnreachable = hopdial.Target, true
	case stage == hoppool.Refusal && kind == hoppool.AuthPolicy:
		staged.Stage = hopdial.Refusal
	default:
		return nil, err
	}
	return nil, staged
}

func poolErrorNamesAddress(err error, address string) bool {
	if err == nil || address == "" {
		return false
	}
	msg := strings.ToLower(err.Error())
	marker := strings.LastIndex(msg, " connect error:")
	return marker >= 0 && strings.HasSuffix(strings.TrimSpace(msg[:marker]), strings.ToLower(address))
}

func postCarrierFailure(err error, address, protocol, carrierProtocol string) (stage hopdial.Stage, carrierError, matched bool) {
	if err == nil || address == "" {
		return hopdial.Unknown, false, false
	}
	msg := strings.ToLower(err.Error())
	marker := strings.LastIndex(msg, " connect error:")
	if marker < 0 || !strings.HasSuffix(strings.TrimSpace(msg[:marker]), strings.ToLower(address)) {
		return hopdial.Unknown, false, false
	}
	family, status := poolErrorFamily(msg)
	carrierFamily, hopFamily := protoFamily(carrierProtocol), protoFamily(protocol)
	if family != "" && family == carrierFamily && family == hopFamily && status != "" {
		// Both adjacent protocols can have emitted this refusal, so neither
		// the post hop nor the carrier can be blamed from this wrapper.
		return hopdial.Unknown, false, true
	}
	if family != "" && family == carrierFamily && family != hopFamily {
		if status == "unreachable" {
			return hopdial.Connect, true, true
		}
		return hopdial.Unknown, true, true
	}
	if isKnownHandshake(msg) && carrierFamily != "" && carrierFamily != hopFamily && strings.Contains(msg, carrierFamily) {
		return hopdial.Unknown, true, true
	}
	return hopdial.Unknown, false, false
}

func (h *mihomoPoolHop) clockNow() time.Time {
	if h != nil && h.now != nil {
		return h.now()
	}
	return time.Now()
}

type hopPoolAdapter struct {
	*outbound.Base
	role            hopPoolRole
	pool            *hoppool.Pool
	dialer          *hopdial.Dialer
	hops            []*mihomoPoolHop
	keys            []string
	members         map[string]member
	binder          siteBinder
	carrier         *nodeHealth
	now             func() time.Time
	mutate          func(key string, change func())
	nextProtocols   map[string]string
	preNextProtocol string
}

func newHopPoolAdapter(name string, role hopPoolRole, spec *hopPoolSpec, members map[string]member, pool *hoppool.Pool, binder siteBinder, carrier *nodeHealth, now func() time.Time) *hopPoolAdapter {
	if now == nil {
		now = time.Now
	}
	a := &hopPoolAdapter{
		Base: outbound.NewBase(outbound.BaseOption{Name: name, Type: C.Selector}), role: role,
		pool: pool, keys: append([]string(nil), spec.keys...), members: members,
		binder: binder, carrier: carrier, now: now,
		preNextProtocol: "",
		nextProtocols:   make(map[string]string),
	}
	for i, key := range spec.keys {
		proxy, _ := members[key].(C.Proxy)
		uri := ""
		if i < len(spec.uris) {
			uri = spec.uris[i]
		}
		u, _ := url.Parse(uri)
		protocol := strings.ToLower(u.Scheme)
		address := hopAddress(uri)
		a.hops = append(a.hops, &mihomoPoolHop{key: key, member: members[key], proxy: proxy, role: role, address: address, protocol: protocol, carrier: carrier, now: now})
	}
	a.dialer = newHopDialer(a, pool, binder, now, role == postHopPool)
	return a
}

func (a *hopPoolAdapter) hopInterfaces() []hopdial.Hop {
	hops := make([]hopdial.Hop, len(a.hops))
	for i, h := range a.hops {
		hops[i] = h
	}
	return hops
}

func newHopDialer(a *hopPoolAdapter, healthPool *hoppool.Pool, binder siteBinder, now func() time.Time, post bool) *hopdial.Dialer {
	return hopdial.New(healthPool, a.hopInterfaces(), hopdial.Options{Clock: now, Binder: binder, Post: post, Mutate: func(key string, change func()) { a.applyChange(key, change) }})
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
	request := poolAttemptMetadata{metadata: clonePoolMetadata(metadata)}
	if a.role == preHopPool {
		request.nextAddress = strings.ToLower(metadata.RemoteAddress())
		request.nextProtocol = a.nextProtocols[request.nextAddress]
		if request.nextProtocol == "" {
			request.nextProtocol = a.preNextProtocol
		}
	} else {
		request.nextAddress = a.hops[0].nextAddress
		request.nextProtocol = a.hops[0].nextProtocol
	}
	requestCtx := context.WithValue(ctx, poolMetadataContextKey{}, request)
	var conn net.Conn
	var err error
	if a.role == postHopPool {
		conn, err = a.dialer.DialSite(requestCtx, metadata.NetWork.String(), metadata.RemoteAddress(), metadata.InUser, siteKey(metadata.Host, metadata.DstIP))
	} else {
		conn, err = a.dialer.DialContext(requestCtx, metadata.NetWork.String(), metadata.RemoteAddress())
	}
	if err != nil {
		return nil, a.poolFailure(err)
	}
	mihomoConn, ok := conn.(C.Conn)
	if !ok {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, &hopPoolDialError{cause: errors.New("hop pool returned an incompatible connection")}
	}
	mihomoConn.AppendToChains(a)
	return mihomoConn, nil
}

func (a *hopPoolAdapter) poolFailure(err error) error {
	if err == nil {
		return nil
	}
	var carrier *poolCarrierError
	if errors.As(err, &carrier) {
		return carrier
	}
	var staged *hopdial.StageError
	if errors.As(err, &staged) && a.role == preHopPool && staged.Stage == hopdial.Target && staged.NextUnreachable {
		// A pre hop explicitly reports that the downstream node is unreachable;
		// let the node-health path account for that evidence.
		return err
	}
	return &hopPoolDialError{cause: err}
}

func (a *hopPoolAdapter) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	return nil, &hopPoolDialError{cause: errors.New("hop pool packet dialing is unsupported")}
}

func clonePoolMetadata(metadata *C.Metadata) *C.Metadata {
	if metadata == nil {
		return nil
	}
	clone := *metadata
	if metadata.SrcGeoIP != nil {
		clone.SrcGeoIP = append([]string(nil), metadata.SrcGeoIP...)
	}
	if metadata.DstGeoIP != nil {
		clone.DstGeoIP = append([]string(nil), metadata.DstGeoIP...)
	}
	return &clone
}

func recentNodeEvidence(h *nodeHealth, now time.Time) bool {
	if h == nil || h.Down() {
		return false
	}
	if last := h.lastOKAt(); !last.IsZero() && !now.Before(last) && now.Sub(last) <= trafficAliveFor {
		return true
	}
	lastTraffic := h.lastTraffic.Load()
	return lastTraffic != 0 && now.UnixNano() >= lastTraffic && now.Sub(time.Unix(0, lastTraffic)) <= trafficAliveFor
}

func classifyPoolFailureAt(err error, nextAddress, address, protocol, carrierProtocol string) (hoppool.Stage, hoppool.Kind, bool) {
	if err == nil || isIgnoredPoolFailure(err) {
		if err != nil && isCapacityFailure(strings.ToLower(err.Error())) {
			return hoppool.UnknownStage, hoppool.Capacity, false
		}
		return hoppool.UnknownStage, hoppool.UnknownKind, false
	}
	msg := strings.ToLower(err.Error())
	lastMarker := strings.LastIndex(msg, " connect error:")
	family, status := poolErrorFamily(msg)
	if lastMarker >= 0 {
		addressEvidence := address != "" && strings.HasSuffix(msg[:lastMarker], strings.ToLower(address))
		if !addressEvidence {
			return hoppool.UnknownStage, hoppool.UnknownKind, false
		}
		if status == "unreachable" {
			return hoppool.Target, hoppool.NextUnreachable, true
		}
		if status == "refusal" {
			return hoppool.Refusal, hoppool.AuthPolicy, true
		}
		if isKnownHandshake(msg) {
			return hoppool.Handshake, hoppool.Ambiguous, true
		}
		return hoppool.Connect, hoppool.Unreachable, true
	}
	if family != "" {
		if nextAddress != "" {
			currentFamily, nextFamily := protoFamily(protocol), protoFamily(carrierProtocol)
			currentMatch, nextMatch := currentFamily == family, nextFamily == family
			if currentMatch == nextMatch || nextMatch {
				return hoppool.UnknownStage, hoppool.UnknownKind, false
			}
		} else if sameHopProtocol(protocol, carrierProtocol) {
			return hoppool.UnknownStage, hoppool.UnknownKind, false
		}
		if status == "unreachable" {
			return hoppool.Target, hoppool.NextUnreachable, true
		}
		if status == "refusal" {
			return hoppool.Refusal, hoppool.AuthPolicy, true
		}
	}
	if isKnownHandshake(msg) {
		if nextAddress != "" || sameHopProtocol(protocol, carrierProtocol) {
			return hoppool.UnknownStage, hoppool.UnknownKind, false
		}
		return hoppool.Handshake, hoppool.Ambiguous, true
	}
	return hoppool.UnknownStage, hoppool.UnknownKind, false
}

func isIgnoredPoolFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return isCapacityFailure(strings.ToLower(err.Error()))
}

func isCapacityFailure(msg string) bool {
	for _, marker := range []string{"capacity", "too many open files", "connection limit", "max connections", "maximum connections"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

var poolHTTPUnavailable = regexp.MustCompile(`\b(?:502\s+bad gateway|503\s+service unavailable|504\s+gateway timeout)\b`)
var poolHTTPErrorCode = regexp.MustCompile(`\berr code:\s*(?:502|503|504)\b`)
var poolSOCKSFailure = regexp.MustCompile(`\bsocks error:\s*[3-6]\b`)

// The vendored SOCKS client does not check or surface REP 2–6; this classifier recognizes explicit error text, including synthetic fixtures. Native proxyclient replacement is deferred.
var poolSOCKSRefusal = regexp.MustCompile(`\bsocks error:\s*2\b`)

func poolErrorFamily(msg string) (family, status string) {
	if strings.Contains(msg, "http need auth") || strings.Contains(msg, "proxy authentication") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "not allowed by proxy") || strings.Contains(msg, "connect method not allowed") || strings.Contains(msg, "err code: 403") || strings.Contains(msg, "err code: 407") || strings.Contains(msg, "err code:403") || strings.Contains(msg, "err code:407") {
		return "http", "refusal"
	}
	if strings.Contains(msg, "rejected username") || strings.Contains(msg, "socks need auth") || poolSOCKSRefusal.MatchString(msg) {
		return "socks", "refusal"
	}
	if poolHTTPUnavailable.MatchString(msg) || poolHTTPErrorCode.MatchString(msg) {
		return "http", "unreachable"
	}
	if poolSOCKSFailure.MatchString(msg) {
		return "socks", "unreachable"
	}
	return "", ""
}
func isKnownHandshake(msg string) bool {
	return strings.Contains(msg, "tls") || strings.Contains(msg, "certificate") || strings.Contains(msg, "handshake")
}
func sameHopProtocol(a, b string) bool {
	if a == "https" {
		a = "http"
	}
	if b == "https" {
		b = "http"
	}
	return a != "" && a == b
}

func poolProxyMap(spec *hopPoolSpec, proxies map[string]C.Proxy) map[string]member {
	out := make(map[string]member, len(spec.keys))
	for i, key := range spec.keys {
		if i >= len(spec.entryNames) {
			continue
		}
		if proxy := proxies[spec.entryNames[i]]; proxy != nil {
			out[key] = proxy
		}
	}
	return out
}

var _ C.ProxyAdapter = (*hopPoolAdapter)(nil)
var _ hopdial.Hop = (*mihomoPoolHop)(nil)
