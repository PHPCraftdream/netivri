package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

// The diag probe URL default mirrors the warm-up probe; warm.go's
// defaultProbeURL is unexported, so the value is repeated here.
const diagDefaultProbeURL = "http://www.gstatic.com/generate_204"

const (
	diagDefaultSamples       = 5
	diagMaxSamples           = 20
	diagDefaultSampleTimeout = 10 * time.Second
)

// diagDefaultIdle is the default list of idle-survival waits.
var diagDefaultIdle = []time.Duration{
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

// Failure bucket keys of NodeDiag.Failures.
const (
	diagFailDNS       = "dns"
	diagFailTCP       = "tcp"
	diagFailTLS       = "tls"
	diagFailFirstByte = "first_byte"
)

// DiagNode is one node to measure: a Clash proxy mapping as stored in a
// subscription snapshot.
type DiagNode struct {
	Label   string
	Mapping map[string]any
}

// DiagOptions configures DiagDial. Zero values mean the documented defaults.
// An Idle value of nil means the default waits; an empty non-nil slice skips
// the idle survival test.
type DiagOptions struct {
	Nodes         []DiagNode
	URL           string          // probe URL; "" = the warm-up default
	Samples       int             // requests per node; 0 = 5; max 20
	Idle          []time.Duration // idle-survival waits; nil = defaults; empty non-nil = skip
	SampleTimeout time.Duration   // per request; 0 = 10s
}

// PhaseStats summarizes the successful samples of one phase with
// nearest-rank percentiles.
type PhaseStats struct {
	N   int
	P50 time.Duration
	P90 time.Duration
	Max time.Duration
}

// IdleResult is the outcome of one idle-survival wait. Counters may grow if
// the test ever retries a value; today one attempt runs per value.
type IdleResult struct {
	Idle time.Duration
	OK   int
	Fail int
}

// NodeDiag is the measurement result of one node. It carries labels only:
// no host, port, uuid or key ever appears in it.
type NodeDiag struct {
	Label            string
	TransportPhases  bool
	Samples          int
	DNS              PhaseStats
	TCP              PhaseStats
	TLS              PhaseStats
	FirstByte        PhaseStats
	Failures         map[string]int // keys dns, tcp, tls, first_byte
	Idle             []IdleResult
	SuggestedPoolTTL time.Duration
}

// diagOneShotSource hands out a single prepared transport connection.
type diagOneShotSource struct {
	mu   sync.Mutex
	conn net.Conn
}

var _ outbound.TransportSource = (*diagOneShotSource)(nil)

// Take implements outbound.TransportSource: the first call returns the conn,
// later calls report a miss.
func (s *diagOneShotSource) Take(context.Context) (net.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conn
	s.conn = nil
	return c, c != nil
}

// diagPhaseSample is one observed phase duration and outcome.
type diagPhaseSample struct {
	seen bool
	took time.Duration
	err  error
}

// diagPhaseCollector collects the tcp and tls phases of one transport
// preparation.
type diagPhaseCollector struct {
	mu  sync.Mutex
	tcp diagPhaseSample
	tls diagPhaseSample
}

// ObservePhase implements outbound.PhaseObserver.
func (c *diagPhaseCollector) ObservePhase(phase outbound.Phase, took time.Duration, err error) {
	s := diagPhaseSample{seen: true, took: took, err: err}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch phase {
	case outbound.PhaseTCP:
		c.tcp = s
	case outbound.PhaseTLS:
		c.tls = s
	}
}

// failureKey reports which failure bucket a failed preparation ended in.
func (c *diagPhaseCollector) failureKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp.err == nil && c.tls.err != nil {
		return diagFailTLS
	}
	// A tcp error, or no phase observed at all (for example an immediate
	// context cancel): account the sample as a tcp failure.
	return diagFailTCP
}

// tcpDuration returns the tcp phase duration when that phase ran and
// succeeded, even if a later phase failed.
func (c *diagPhaseCollector) tcpDuration() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcp.took, c.tcp.seen && c.tcp.err == nil
}

// tlsDuration returns the tls phase duration when that phase succeeded.
func (c *diagPhaseCollector) tlsDuration() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tls.took, c.tls.seen && c.tls.err == nil
}

// diagServerHost reads the "server" field of the original mapping.
func diagServerHost(mapping map[string]any) (string, bool) {
	host, ok := mapping["server"].(string)
	return host, ok && host != ""
}

// isIPLiteral reports whether s is a literal IP address.
func isIPLiteral(s string) bool {
	return net.ParseIP(s) != nil
}

// diagSplitURL splits an http probe URL into host, port and the request
// path with query. https and other schemes are rejected.
func diagSplitURL(raw string) (host string, port int, pathQuery string, err error) {
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", 0, "", errors.New("core: diag: probe URL is invalid")
	}
	switch u.Scheme {
	case "http":
	case "https":
		return "", 0, "", errors.New("core: diag: https is not supported by the raw probe request")
	default:
		return "", 0, "", errors.New("core: diag: probe URL scheme must be http")
	}
	host = u.Hostname()
	if host == "" {
		return "", 0, "", errors.New("core: diag: probe URL has no host")
	}
	if ps := u.Port(); ps != "" {
		port, err = strconv.Atoi(ps)
		if err != nil || port < 1 || port > 65535 {
			return "", 0, "", errors.New("core: diag: probe URL has an invalid port")
		}
	} else {
		port = 80
	}
	pathQuery = u.RequestURI()
	if pathQuery == "" {
		pathQuery = "/"
	}
	return host, port, pathQuery, nil
}

// normalizeDiagOptions applies the documented defaults and validates the
// options. Error messages never contain mapping contents.
func normalizeDiagOptions(o DiagOptions) (DiagOptions, error) {
	if len(o.Nodes) == 0 {
		return o, errors.New("core: diag: no nodes to measure")
	}
	if o.Samples < 0 || o.Samples > diagMaxSamples {
		return o, fmt.Errorf("core: diag: samples must be between 1 and %d", diagMaxSamples)
	}
	if o.Samples == 0 {
		o.Samples = diagDefaultSamples
	}
	if o.SampleTimeout < 0 {
		return o, errors.New("core: diag: sample timeout must not be negative")
	}
	if o.SampleTimeout == 0 {
		o.SampleTimeout = diagDefaultSampleTimeout
	}
	if o.Idle == nil {
		o.Idle = append([]time.Duration(nil), diagDefaultIdle...)
	}
	for _, d := range o.Idle {
		if d < 0 {
			return o, errors.New("core: diag: idle wait must not be negative")
		}
	}
	if o.URL == "" {
		o.URL = diagDefaultProbeURL
	}
	if _, _, _, err := diagSplitURL(o.URL); err != nil {
		return o, err
	}
	return o, nil
}

// diagPercentile is the nearest-rank percentile on the sorted values: index
// ceil(p/100*n)-1, like internal/stats. Empty input yields 0.
func diagPercentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	sorted := make([]int64, len(v))
	copy(sorted, v)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// diagPhaseStats summarizes successful samples; empty input yields the zero
// PhaseStats.
func diagPhaseStats(samples []time.Duration) PhaseStats {
	if len(samples) == 0 {
		return PhaseStats{}
	}
	vs := make([]int64, len(samples))
	for i, d := range samples {
		vs[i] = int64(d)
	}
	return PhaseStats{
		N:   len(vs),
		P50: time.Duration(diagPercentile(vs, 50)),
		P90: time.Duration(diagPercentile(vs, 90)),
		Max: time.Duration(diagPercentile(vs, 100)),
	}
}

// diagSuggestTTL suggests a pool TTL as half of the longest idle wait whose
// whole prefix of (idle-sorted) results had no failures. Unknown → 0.
func diagSuggestTTL(idle []IdleResult) time.Duration {
	sorted := make([]IdleResult, len(idle))
	copy(sorted, idle)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Idle < sorted[j].Idle })
	best := time.Duration(0)
	for _, r := range sorted {
		if r.Fail > 0 || r.OK == 0 {
			break
		}
		best = r.Idle
	}
	return best / 2
}

// DiagDial measures the connection phases of every node: DNS, TCP, TLS and
// time to first byte of an HTTP probe request, plus an idle-survival test
// for nodes with transport phases. Nodes are measured one after another,
// samples sequentially. On failure of the whole run it returns the partial
// results and the error.
func DiagDial(ctx context.Context, o DiagOptions) ([]NodeDiag, error) {
	o, err := normalizeDiagOptions(o)
	if err != nil {
		return nil, err
	}
	results := make([]NodeDiag, 0, len(o.Nodes))
	for i, dn := range o.Nodes {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		nd, err := diagNode(ctx, o, i, dn)
		if err != nil {
			return results, err
		}
		// A cancel during the last sample is counted as a failure there; the
		// run still ends with the cancellation, without the cut-off node.
		if err := ctx.Err(); err != nil {
			return results, err
		}
		results = append(results, *nd)
	}
	return results, nil
}

// diagNode measures a single node. It never mutates the caller's mapping and
// closes the parsed proxy before returning.
func diagNode(ctx context.Context, o DiagOptions, index int, dn DiagNode) (*NodeDiag, error) {
	// Shallow copy like prepareNode: never touch the caller's map, use a
	// placeholder name when missing and strip dialer-proxy chaining.
	entry := make(map[string]any, len(dn.Mapping)+1)
	for k, v := range dn.Mapping {
		entry[k] = v
	}
	if _, ok := entry["name"]; !ok {
		entry["name"] = "netivri-diag"
	}
	delete(entry, "dialer-proxy")

	// The parser's own error can quote mapping values: report only the index.
	p, err := adapter.ParseProxy(entry)
	if err != nil {
		return nil, fmt.Errorf("core: diag: node %d: invalid proxy mapping", index)
	}
	defer func() { _ = p.Close() }()

	host, port, path, err := diagSplitURL(o.URL)
	if err != nil {
		return nil, err
	}
	md := &C.Metadata{NetWork: C.TCP, Host: host, DstPort: uint16(port)}

	run := &diagNodeRun{
		o:    o,
		md:   md,
		host: host,
		port: port,
		path: path,
		nd: NodeDiag{
			Label:    dn.Label,
			Samples:  o.Samples,
			Failures: map[string]int{diagFailDNS: 0, diagFailTCP: 0, diagFailTLS: 0, diagFailFirstByte: 0},
		},
		serverHost: func() string {
			h, _ := diagServerHost(dn.Mapping)
			return h
		}(),
	}

	// Transport phases are a capability of the node (VLESS over TCP with
	// TLS), independent of whether a given sample reaches the server.
	v, isVless := vlessAdapter(p)
	run.nd.TransportPhases = isVless && v.TransportSupported()
	if isVless {
		defer func() {
			v.SetPhaseObserver(nil)
			v.SetTransportSource(nil)
		}()
	}

	for s := 0; s < o.Samples; s++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sampleCtx, cancel := context.WithTimeout(ctx, o.SampleTimeout)
		run.dnsSample(sampleCtx)
		if run.nd.TransportPhases {
			run.transportSample(sampleCtx, v)
		} else {
			run.plainSample(sampleCtx, p)
		}
		cancel()
	}

	if run.nd.TransportPhases && len(o.Idle) > 0 {
		idle := make([]time.Duration, len(o.Idle))
		copy(idle, o.Idle)
		sort.Slice(idle, func(i, j int) bool { return idle[i] < idle[j] })
		for _, d := range idle {
			ir, rerr := run.idleSample(ctx, v, d)
			if rerr != nil {
				return nil, rerr
			}
			run.nd.Idle = append(run.nd.Idle, ir)
		}
	}
	run.nd.SuggestedPoolTTL = diagSuggestTTL(run.nd.Idle)

	run.nd.DNS = diagPhaseStats(run.dns)
	run.nd.TCP = diagPhaseStats(run.tcp)
	run.nd.TLS = diagPhaseStats(run.tls)
	run.nd.FirstByte = diagPhaseStats(run.firstByte)
	return &run.nd, nil
}

// diagNodeRun carries the per-node measurement state.
type diagNodeRun struct {
	o          DiagOptions
	md         *C.Metadata
	host       string
	port       int
	path       string
	serverHost string // node mapping "server" for the DNS phase
	nd         NodeDiag
	dns        []time.Duration
	tcp        []time.Duration
	tls        []time.Duration
	firstByte  []time.Duration
}

// dnsSample times a resolver lookup of the node's server host. IP literals
// are skipped entirely; a lookup failure records a dns failure and the
// sample continues (the adapter resolves on its own).
func (r *diagNodeRun) dnsSample(ctx context.Context) {
	if r.serverHost == "" || isIPLiteral(r.serverHost) {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, r.o.SampleTimeout)
	start := time.Now()
	_, err := net.DefaultResolver.LookupIPAddr(dctx, r.serverHost)
	cancel()
	if err != nil {
		r.nd.Failures[diagFailDNS]++
		return
	}
	r.dns = append(r.dns, time.Since(start))
}

// plainSample measures first_byte over a plain dial through the proxy.
func (r *diagNodeRun) plainSample(ctx context.Context, p C.Proxy) {
	start := time.Now()
	conn, err := p.DialContext(ctx, r.md)
	if err == nil {
		err = diagHTTPRequest(ctx, conn, r.host, r.port, r.path)
		_ = conn.Close()
	}
	if err != nil {
		r.nd.Failures[diagFailFirstByte]++
		return
	}
	r.firstByte = append(r.firstByte, time.Since(start))
}

// transportSample prepares a transport (TCP + TLS), records the phases that
// ran (a successful tcp phase counts even when tls then fails) and measures
// first_byte over it.
func (r *diagNodeRun) transportSample(ctx context.Context, v *outbound.Vless) {
	col := &diagPhaseCollector{}
	v.SetPhaseObserver(col)
	conn, err := v.PrepareTransport(ctx)
	v.SetPhaseObserver(nil)
	if d, ok := col.tcpDuration(); ok {
		r.tcp = append(r.tcp, d)
	}
	if d, ok := col.tlsDuration(); ok {
		r.tls = append(r.tls, d)
	}
	if err != nil {
		r.nd.Failures[col.failureKey()]++
		return
	}
	took, err := r.attachRequest(ctx, v, conn)
	if err != nil {
		r.nd.Failures[diagFailFirstByte]++
		return
	}
	r.firstByte = append(r.firstByte, took)
}

// attachRequest hands the prepared transport to the next dial through a
// one-shot source and measures the time to the first response byte. The
// transport is closed on every path.
func (r *diagNodeRun) attachRequest(ctx context.Context, v *outbound.Vless, conn net.Conn) (time.Duration, error) {
	v.SetTransportSource(&diagOneShotSource{conn: conn})
	defer v.SetTransportSource(nil)
	defer func() { _ = conn.Close() }()
	start := time.Now()
	rc, err := v.DialContext(ctx, r.md)
	if err == nil {
		err = diagHTTPRequest(ctx, rc, r.host, r.port, r.path)
		_ = rc.Close()
	}
	if err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// idleSample runs one idle-survival attempt: prepare a fresh transport, wait
// the idle duration, attach and request. The wait is not part of any
// timeout: preparation and the request each get SampleTimeout. A cancel of
// the parent context aborts the whole run with its error.
func (r *diagNodeRun) idleSample(ctx context.Context, v *outbound.Vless, d time.Duration) (IdleResult, error) {
	ir := IdleResult{Idle: d}
	prepCtx, cancelPrep := context.WithTimeout(ctx, r.o.SampleTimeout)
	conn, err := v.PrepareTransport(prepCtx)
	cancelPrep()
	if err != nil {
		if ctx.Err() != nil {
			return ir, ctx.Err()
		}
		ir.Fail++
		return ir, nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		_ = conn.Close()
		return ir, ctx.Err()
	}

	reqCtx, cancelReq := context.WithTimeout(ctx, r.o.SampleTimeout)
	defer cancelReq()
	if _, err := r.attachRequest(reqCtx, v, conn); err != nil {
		if ctx.Err() != nil {
			return ir, ctx.Err()
		}
		ir.Fail++
		return ir, nil
	}
	ir.OK++
	return ir, nil
}

// diagHTTPRequest writes a minimal HTTP/1.1 GET and reads the first byte of
// the response. The connection deadline comes from the context and a
// cancelled context closes the connection, which unblocks a pending read.
func diagHTTPRequest(ctx context.Context, conn net.Conn, host string, port int, pathQuery string) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	req := "GET " + pathQuery + " HTTP/1.1\r\n" +
		"Host: " + net.JoinHostPort(host, strconv.Itoa(port)) + "\r\n" +
		"User-Agent: netivri-diag\r\n" +
		"Connection: close\r\n" +
		"\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return err
	}
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		return err
	}
	return nil
}
