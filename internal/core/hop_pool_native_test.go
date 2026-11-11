package core

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hopdial "github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	"github.com/PHPCraftdream/netivri/pkg/hopkit/proxyclient"
	N "github.com/metacubex/mihomo/common/net"
	C "github.com/metacubex/mihomo/constant"
)

const (
	nativeProxyOK int32 = iota
	nativeProxyBadGreeting
	nativeProxyTruncatedTarget
	nativeProxyREP
	nativeProxyBlock
)

type nativeLoopbackProxy struct {
	uri      string
	listener net.Listener
	mode     atomic.Int32
	rep      atomic.Int32
	status   atomic.Int32
	requests atomic.Int32
	targets  atomic.Int32
	targetMu sync.Mutex
	target   string
	welcome  []byte
	echo     atomic.Bool
}

func newNativeLoopbackProxy(t *testing.T, scheme string) *nativeLoopbackProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &nativeLoopbackProxy{listener: listener}
	proxy.uri = scheme + "://" + listener.Addr().String()
	go proxy.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return proxy
}

func (p *nativeLoopbackProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *nativeLoopbackProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	p.requests.Add(1)
	if strings.HasPrefix(p.uri, "http:") {
		p.handleHTTP(conn)
		return
	}
	p.handleSOCKS(conn)
}

func (p *nativeLoopbackProxy) handleSOCKS(conn net.Conn) {
	mode := p.mode.Load()
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if mode == nativeProxyBlock {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	if mode == nativeProxyBadGreeting {
		_, _ = conn.Write([]byte{4, 0})
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return
	}
	var host string
	var port uint16
	switch request[3] {
	case 1:
		var rest [6]byte
		if _, err := io.ReadFull(conn, rest[:]); err != nil {
			return
		}
		host, port = net.IP(rest[:4]).String(), binary.BigEndian.Uint16(rest[4:])
	case 4:
		var rest [18]byte
		if _, err := io.ReadFull(conn, rest[:]); err != nil {
			return
		}
		host, port = net.IP(rest[:16]).String(), binary.BigEndian.Uint16(rest[16:])
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return
		}
		rest := make([]byte, int(n[0])+2)
		if _, err := io.ReadFull(conn, rest); err != nil {
			return
		}
		host, port = string(rest[:n[0]]), binary.BigEndian.Uint16(rest[n[0]:])
	default:
		return
	}
	p.recordTarget(net.JoinHostPort(host, strconv.Itoa(int(port))))
	if mode == nativeProxyTruncatedTarget {
		return
	}
	rep := byte(0)
	if mode == nativeProxyREP {
		rep = byte(p.rep.Load())
	}
	response := []byte{5, rep, 0, 1, 127, 0, 0, 1, 0, 1}
	response = append(response, p.welcome...)
	if _, err := conn.Write(response); err != nil {
		return
	}
	if rep == 0 {
		if p.echo.Load() {
			_, _ = io.Copy(conn, conn)
		} else {
			_, _ = io.Copy(io.Discard, conn)
		}
	}
}

func (p *nativeLoopbackProxy) handleHTTP(conn net.Conn) {
	reader := bufio.NewReader(conn)
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(requestLine)
	if len(fields) >= 2 {
		p.recordTarget(fields[1])
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" {
			break
		}
	}
	status := int(p.status.Load())
	if status == 0 {
		status = 200
	}
	phrase := map[int]string{200: "Connection established", 403: "Forbidden", 407: "Proxy Authentication Required", 502: "Bad Gateway", 503: "Service Unavailable", 504: "Gateway Timeout"}[status]
	if phrase == "" {
		phrase = "Server Error"
	}
	response := "HTTP/1.1 " + strconv.Itoa(status) + " " + phrase + "\r\n\r\n"
	if status == 200 {
		_, _ = conn.Write(append([]byte(response), p.welcome...))
		if p.echo.Load() {
			_, _ = io.Copy(conn, conn)
		} else {
			_, _ = io.Copy(io.Discard, conn)
		}
		return
	}
	_, _ = io.WriteString(conn, response)
}

func (p *nativeLoopbackProxy) recordTarget(target string) {
	p.targets.Add(1)
	p.targetMu.Lock()
	p.target = target
	p.targetMu.Unlock()
}

func (p *nativeLoopbackProxy) requestedTarget() string {
	p.targetMu.Lock()
	defer p.targetMu.Unlock()
	return p.target
}

func nativeProxyConfig(t *testing.T, raw string) proxyclient.Proxy {
	t.Helper()
	config, err := proxyclient.Parse(raw)
	if err != nil {
		t.Fatal("native proxy URL was rejected")
	}
	return config
}

func setNativeCarrier(adapter *hopPoolAdapter, carrier member, direct bool) {
	for _, hop := range adapter.hops {
		hop.carrierMember = carrier
		hop.directCarrier = direct
	}
}

func nativeFirstIndex(spec *hopPoolSpec, clock time.Time, seed [2]uint64) int {
	key := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return clock }, RNG: rand.NewPCG(seed[0], seed[1])}).Order(clock)[0]
	if spec.keys[1] == key {
		return 1
	}
	return 0
}

func TestNativePrePoolFailoverAndMetadata(t *testing.T) {
	a, b := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(preHopPool, []string{a.uri, b.uri}, 0)
	now, seed := time.Unix(100, 0), [2]uint64{13, 21}
	first := nativeFirstIndex(spec, now, seed)
	servers := []*nativeLoopbackProxy{a, b}
	servers[first].mode.Store(nativeProxyBadGreeting)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	adapter := newHopPoolAdapter("native-pre", preHopPool, spec, p, nil, nil, func() time.Time { return now })
	conn, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	if err != nil || conn == nil {
		t.Fatalf("native pre failover failed: %v", err)
	}
	_ = conn.Close()
	if servers[0].requests.Load() != 1 || servers[1].requests.Load() != 1 {
		t.Fatalf("native proxy attempts=%d/%d, want one each", servers[0].requests.Load(), servers[1].requests.Load())
	}
	if servers[first].targets.Load() != 0 || servers[1-first].targets.Load() != 1 {
		t.Fatal("pre handshake failover sent an unexpected target request")
	}
}

func TestNativePostHandshakeRetryStopsOnceTargetSent(t *testing.T) {
	a, b := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(postHopPool, []string{a.uri, b.uri}, 0)
	now, seed := time.Unix(1700, 0), [2]uint64{731, 991}
	first := nativeFirstIndex(spec, now, seed)
	servers := []*nativeLoopbackProxy{a, b}
	servers[first].mode.Store(nativeProxyBadGreeting)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	adapter := newHopPoolAdapter("native-post-retry", postHopPool, spec, p, nil, nil, func() time.Time { return now })
	setNativeCarrier(adapter, nil, true)
	conn, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	if err != nil {
		t.Fatalf("pre-target malformed greeting was not retried: %v", err)
	}
	_ = conn.Close()
	if servers[0].requests.Load() != 1 || servers[1].requests.Load() != 1 {
		t.Fatal("native post retry did not attempt each eligible hop exactly once")
	}

	c, d := newNativeLoopbackProxy(t, "socks5"), newNativeLoopbackProxy(t, "socks5")
	spec2 := newHopPoolSpec(postHopPool, []string{c.uri, d.uri}, 0)
	first2 := nativeFirstIndex(spec2, now, seed)
	servers2 := []*nativeLoopbackProxy{c, d}
	servers2[first2].mode.Store(nativeProxyTruncatedTarget)
	p2 := pool.New(spec2.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	adapter2 := newHopPoolAdapter("native-post-no-retry", postHopPool, spec2, p2, nil, nil, func() time.Time { return now })
	setNativeCarrier(adapter2, nil, true)
	_, err = adapter2.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	var staged *hopdial.StageError
	if !errors.As(err, &staged) || staged.Stage != hopdial.Handshake || !staged.TargetSent {
		t.Fatal("post-target handshake failure lost typed target-sent evidence")
	}
	if servers2[first2].requests.Load() != 1 || servers2[1-first2].requests.Load() != 0 {
		t.Fatal("target-sent post failure was retried unsafely")
	}
}

func TestNativeProxyProtocolStatusStageMapping(t *testing.T) {
	for _, tc := range []struct {
		rep   byte
		stage hopdial.Stage
		next  bool
	}{
		{1, hopdial.Unknown, false}, {2, hopdial.Refusal, false},
		{3, hopdial.Target, true}, {4, hopdial.Target, true}, {5, hopdial.Target, true}, {6, hopdial.Target, true},
		{7, hopdial.Unknown, false}, {8, hopdial.Unknown, false},
	} {
		proxy := newNativeLoopbackProxy(t, "socks5")
		proxy.mode.Store(nativeProxyREP)
		proxy.rep.Store(int32(tc.rep))
		hop := &mihomoPoolHop{key: "pre", role: preHopPool, proxyConfig: nativeProxyConfig(t, proxy.uri), now: time.Now}
		_, err := hop.DialContext(context.Background(), "tcp", "target.invalid:443")
		var staged *hopdial.StageError
		if !errors.As(err, &staged) || staged.Stage != tc.stage || staged.NextUnreachable != tc.next || !staged.TargetSent {
			t.Fatalf("SOCKS5 status %d mapped to wrong stage", tc.rep)
		}
	}
	for _, tc := range []struct {
		status int32
		stage  hopdial.Stage
		next   bool
	}{
		{403, hopdial.Refusal, false}, {407, hopdial.Refusal, false},
		{502, hopdial.Target, true}, {503, hopdial.Target, true}, {504, hopdial.Target, true},
		{500, hopdial.Unknown, false},
	} {
		proxy := newNativeLoopbackProxy(t, "http")
		proxy.status.Store(tc.status)
		hop := &mihomoPoolHop{key: "pre", role: preHopPool, proxyConfig: nativeProxyConfig(t, proxy.uri), now: time.Now}
		_, err := hop.DialContext(context.Background(), "tcp", "target.invalid:443")
		var staged *hopdial.StageError
		if !errors.As(err, &staged) || staged.Stage != tc.stage || staged.NextUnreachable != tc.next || !staged.TargetSent {
			t.Fatalf("HTTP status %d mapped to wrong stage", tc.status)
		}
	}
}

type nativeFailingCarrier struct {
	name string
	err  error
}

func (m *nativeFailingCarrier) Name() string { return m.name }
func (m *nativeFailingCarrier) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, errors.New("node carrier failure")
}
func (m *nativeFailingCarrier) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	return nil, errors.New("unsupported")
}
func (m *nativeFailingCarrier) SupportUDP() bool              { return false }
func (m *nativeFailingCarrier) IsL3Protocol(*C.Metadata) bool { return false }

func TestNativeCarrierFailureBelongsToNodeHealth(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "socks5")
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri, "socks5://unused.invalid:1080"}, 0)
	health := newHealthTable()
	health.register([]string{"node"})
	carrierHealth := health.health("node")
	carrierHealth.recordProbe(true, time.Millisecond)
	p := pool.New(spec.keys, pool.Options{})
	adapter := newHopPoolAdapter("native-post-carrier", postHopPool, spec, p, nil, carrierHealth, time.Now)
	setNativeCarrier(adapter, &nativeFailingCarrier{name: "node"}, false)
	group := newStickyGroupMembers("route", []member{adapter}, []string{"node"}, newAffinityTable(nil, time.Minute), health, nil)
	_, err := group.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err == nil || isHopPoolDialError(err) || health.health("node").consDialFail == 0 {
		t.Fatal("carrier failure did not reach node health as raw transport evidence")
	}
	for _, key := range spec.keys {
		if state := poolState(p, key); state.HardFailures != 0 || state.Sand != 0 {
			t.Fatal("carrier failure changed post-hop health")
		}
	}
}

func nativeCarrierForLoopback(t *testing.T, proxy *nativeLoopbackProxy) *nativeMemberCarrier {
	t.Helper()
	parsed, err := url.Parse(proxy.uri)
	if err != nil {
		t.Fatal("loopback proxy address did not parse")
	}
	hopAddress := net.JoinHostPort(parsed.Hostname(), parsed.Port())
	return &nativeMemberCarrier{
		name:   "node",
		routes: map[string]string{hopAddress: nativeProxyAddress(t, proxy)},
	}
}

func TestNativePostPoolSuccessThroughCarrier(t *testing.T) {
	for _, scheme := range []string{"socks5", "http"} {
		t.Run(scheme, func(t *testing.T) {
			proxy := newNativeLoopbackProxy(t, scheme)
			proxy.welcome = []byte("ready")
			proxy.echo.Store(true)
			parsed, err := url.Parse(proxy.uri)
			if err != nil {
				t.Fatal("loopback proxy address did not parse")
			}
			spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
			now := time.Now()
			p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
			health := &nodeHealth{now: func() time.Time { return now }}
			health.recordProbe(true, time.Millisecond)
			adapter := newHopPoolAdapter("post-carrier-success", postHopPool, spec, p, nil, health, func() time.Time { return now })
			carrier := nativeCarrierForLoopback(t, proxy)
			setNativeCarrier(adapter, carrier, false)
			metadata := &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "site.invalid", DstPort: 8443}
			conn, err := adapter.DialContext(context.Background(), metadata)
			if err != nil || conn == nil {
				t.Fatal("native post pool failed through carrier")
			}
			defer func() { _ = conn.Close() }()
			if proxy.requestedTarget() != "site.invalid:8443" {
				t.Fatal("native post request did not preserve the site target")
			}
			host, port, user, metadataType, network := carrier.lastMetadata()
			if host != parsed.Hostname() || port != uint16(mustNativePort(t, parsed.Port())) || user != "" || metadataType != C.INNER || network != C.TCP {
				t.Fatal("carrier metadata contained values beyond the requested hop address")
			}
			welcome := make([]byte, len(proxy.welcome))
			if _, err := io.ReadFull(conn, welcome); err != nil || string(welcome) != string(proxy.welcome) {
				t.Fatal("buffered tunnel payload was not preserved")
			}
			payload := []byte("tunnel-check")
			if _, err := conn.Write(payload); err != nil {
				t.Fatal("tunnel payload write failed")
			}
			echo := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != string(payload) {
				t.Fatal("tunnel payload was not preserved")
			}
			if state := poolState(p, spec.keys[0]); state.HardFailures != 0 || state.Sand != 0 || state.State != pool.Healthy {
				t.Fatal("successful post dial changed hop health")
			}
		})
	}
}

func mustNativePort(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal("loopback proxy port did not parse")
	}
	return port
}

func TestNativePostPoolStatusBlameAndHealth(t *testing.T) {
	cases := []struct {
		name   string
		scheme string
		rep    byte
		status int32
		stage  hopdial.Stage
		next   bool
		soft   bool
	}{
		{"SOCKS reply 1", "socks5", 1, 0, hopdial.Unknown, false, false},
		{"SOCKS refusal", "socks5", 2, 0, hopdial.Refusal, false, true},
		{"SOCKS reply 3", "socks5", 3, 0, hopdial.Target, true, false},
		{"SOCKS reply 4", "socks5", 4, 0, hopdial.Target, true, false},
		{"SOCKS reply 5", "socks5", 5, 0, hopdial.Target, true, false},
		{"SOCKS reply 6", "socks5", 6, 0, hopdial.Target, true, false},
		{"SOCKS reply 7", "socks5", 7, 0, hopdial.Unknown, false, false},
		{"SOCKS reply 8", "socks5", 8, 0, hopdial.Unknown, false, false},
		{"HTTP policy", "http", 0, 403, hopdial.Refusal, false, true},
		{"HTTP authentication", "http", 0, 407, hopdial.Refusal, false, true},
		{"HTTP bad gateway", "http", 0, 502, hopdial.Target, true, false},
		{"HTTP unavailable", "http", 0, 503, hopdial.Target, true, false},
		{"HTTP gateway timeout", "http", 0, 504, hopdial.Target, true, false},
		{"HTTP other status", "http", 0, 500, hopdial.Unknown, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newNativeLoopbackProxy(t, tc.scheme)
			if tc.scheme == "socks5" {
				proxy.mode.Store(nativeProxyREP)
				proxy.rep.Store(int32(tc.rep))
			} else {
				proxy.status.Store(tc.status)
			}
			spec := newHopPoolSpec(postHopPool, []string{proxy.uri}, 0)
			now := time.Now()
			p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
			carrierHealth := &nodeHealth{now: func() time.Time { return now }}
			carrierHealth.recordProbe(true, time.Millisecond)
			adapter := newHopPoolAdapter("post-status-blame", postHopPool, spec, p, nil, carrierHealth, func() time.Time { return now })
			setNativeCarrier(adapter, nativeCarrierForLoopback(t, proxy), false)
			_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "site.invalid", DstPort: 8443})
			var staged *hopdial.StageError
			if err == nil || !errors.As(err, &staged) || staged.Stage != tc.stage || staged.NextUnreachable != tc.next || !staged.TargetSent {
				t.Fatal("post proxy failure lost its typed stage or target evidence")
			}
			state := poolState(p, spec.keys[0])
			if tc.soft {
				if state.HardFailures != 0 || state.Sand <= 0 || state.State != pool.Healthy {
					t.Fatal("post refusal did not record only a soft hop failure")
				}
			} else if state.HardFailures != 0 || state.Sand != 0 || state.Trips != 0 || state.State != pool.Healthy {
				t.Fatal("unknown or next-hop failure changed current-hop health")
			}
		})
	}
}

func TestNativePostTLSSetupFailureBlamesPostHop(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("TLS fixture listener failed")
	}
	defer func() { _ = listener.Close() }()
	var accepted atomic.Int32
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		accepted.Add(1)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("not tls"))
		_ = conn.Close()
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	uri := "https://localhost:" + strconv.Itoa(port)
	spec := newHopPoolSpec(postHopPool, []string{uri}, 0)
	now := time.Now()
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	carrierHealth := &nodeHealth{now: func() time.Time { return now }}
	carrierHealth.recordProbe(true, time.Millisecond)
	adapter := newHopPoolAdapter("post-tls-stage", postHopPool, spec, p, nil, carrierHealth, func() time.Time { return now })
	carrier := &nativeMemberCarrier{
		name:   "node",
		routes: map[string]string{net.JoinHostPort("localhost", strconv.Itoa(port)): listener.Addr().String()},
	}
	setNativeCarrier(adapter, carrier, false)
	_, err = adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "site.invalid", DstPort: 8443})
	var staged *hopdial.StageError
	if err == nil || !errors.As(err, &staged) || staged.Stage != hopdial.Connect || staged.TargetSent || staged.NextUnreachable {
		t.Fatal("proxy TLS setup failure was not attributed to the post hop")
	}
	var carrierError *poolCarrierError
	if errors.As(err, &carrierError) || !isHopPoolDialError(err) || accepted.Load() != 1 {
		t.Fatal("proxy TLS setup failure was classified as a carrier failure")
	}
	state := poolState(p, spec.keys[0])
	if state.HardFailures != 1 || state.Sand != 0 {
		t.Fatal("proxy TLS setup failure did not increment post-hop hard health")
	}
	if carrierHealth.consDialFail != 0 || carrierHealth.lastTraffic.Load() != 0 {
		t.Fatal("proxy TLS setup failure changed carrier health")
	}
}

func nativeProxyAddress(t *testing.T, proxy *nativeLoopbackProxy) string {
	t.Helper()
	parsed, err := url.Parse(proxy.uri)
	if err != nil {
		t.Fatal("loopback proxy address did not parse")
	}
	return net.JoinHostPort(parsed.Hostname(), parsed.Port())
}

func TestNativePostTrafficAllowedWhenCarrierEvidenceIsStale(t *testing.T) {
	proxy := newNativeLoopbackProxy(t, "socks5")
	proxy.mode.Store(nativeProxyBadGreeting)
	spec := newHopPoolSpec(postHopPool, []string{proxy.uri, "socks5://unused.invalid:1080"}, 0)
	now := time.Now()
	health := &nodeHealth{now: func() time.Time { return now }}
	health.recordProbe(true, time.Millisecond)
	now = now.Add(trafficAliveFor + time.Second)
	carrier := nativeCarrierForLoopback(t, proxy)
	binder := &testSiteBinder{values: map[string]string{"fixture-user\x00example.test": spec.keys[0]}}
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }})
	adapter := newHopPoolAdapter("native-post-stale", postHopPool, spec, p, binder, health, func() time.Time { return now })
	setNativeCarrier(adapter, carrier, false)
	before := make([]HopSnapshot, len(spec.keys))
	for i, key := range spec.keys {
		before[i] = poolState(p, key)
	}
	conn, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "fixture-user", Host: "example.test", DstPort: 443})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("stale carrier evidence unexpectedly produced a successful post dial")
	}
	if requests := proxy.requests.Load(); requests != 1 {
		t.Fatalf("bound post hop request count = %d, want 1", requests)
	}
	fallbackHost, fallbackPort, _, _, _ := carrier.lastMetadata()
	if fallbackHost != "unused.invalid" || fallbackPort != 1080 {
		t.Fatal("safe post retry did not reach the configured fallback hop")
	}
	var carrierErr *poolCarrierError
	if !errors.As(err, &carrierErr) {
		t.Fatal("fallback carrier failure was not preserved")
	}
	for i, key := range spec.keys {
		after := poolState(p, key)
		if after.HardFailures != before[i].HardFailures {
			t.Fatalf("post hop %d hard failures changed", i)
		}
		if after.Sand != before[i].Sand {
			t.Fatalf("post hop %d sand changed", i)
		}
		if after.Trips != before[i].Trips {
			t.Fatalf("post hop %d trips changed", i)
		}
		if after.State != before[i].State || after.State != pool.Healthy {
			t.Fatalf("post hop %d state changed from healthy", i)
		}
	}
}

type nativeMemberCarrier struct {
	name     string
	routes   map[string]string
	mu       sync.Mutex
	lastHost string
	lastPort uint16
	lastUser string
	lastType C.Type
	lastNet  C.NetWork
}

func (m *nativeMemberCarrier) Name() string { return m.name }
func (m *nativeMemberCarrier) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	endpoint := ""
	if metadata != nil {
		m.mu.Lock()
		m.lastHost, m.lastPort, m.lastUser = metadata.Host, metadata.DstPort, metadata.InUser
		m.lastType, m.lastNet = metadata.Type, metadata.NetWork
		m.mu.Unlock()
		key := net.JoinHostPort(metadata.Host, strconv.Itoa(int(metadata.DstPort)))
		var ok bool
		endpoint, ok = m.routes[key]
		if !ok {
			return nil, errors.New("carrier destination unavailable")
		}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return nil, err
	}
	return &nativePoolConn{ExtendedConn: N.NewDeadlineConn(conn)}, nil
}
func (m *nativeMemberCarrier) lastMetadata() (string, uint16, string, C.Type, C.NetWork) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHost, m.lastPort, m.lastUser, m.lastType, m.lastNet
}
func (m *nativeMemberCarrier) ListenPacketContext(context.Context, *C.Metadata) (C.PacketConn, error) {
	return nil, errors.New("unsupported")
}
func (m *nativeMemberCarrier) SupportUDP() bool              { return false }
func (m *nativeMemberCarrier) IsL3Protocol(*C.Metadata) bool { return false }
