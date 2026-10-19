// Package fetch implements a strict-proxy HTTP fetcher for subscription
// links: when a proxy is configured, every byte goes through it and a
// proxy failure never falls back to a direct connection.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const (
	// DefaultTimeout bounds the whole request including redirects.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxBytes caps the response body.
	DefaultMaxBytes = 16 << 20 // 16 MiB
	// DefaultUserAgent is sent when Options.UserAgent is empty.
	DefaultUserAgent = "netivri"
	// MaxRedirects bounds the redirect chain per request.
	MaxRedirects = 5
	// statusDrainLimit bounds how much of a non-2xx body is drained.
	statusDrainLimit = 1 << 16
)

// Options configures a single fetch.
type Options struct {
	// Proxy is "" for an explicit direct connection, or a socks5://,
	// socks5h://, http:// or https:// proxy URL with optional userinfo.
	Proxy string
	// Timeout bounds the whole request including redirects and body
	// read. DefaultTimeout when zero.
	Timeout time.Duration
	// MaxBytes caps the body; exceeding it is an error, never a silent
	// truncation. DefaultMaxBytes when zero.
	MaxBytes int64
	// UserAgent overrides DefaultUserAgent when non-empty.
	UserAgent string
}

// Result is a successful fetch. Body is at most MaxBytes long.
type Result struct {
	Body   []byte
	Status int
	Header http.Header
}

// Get fetches rawURL using o. Errors are *cli.Error with stable codes
// FETCH_URL_INVALID, FETCH_PROXY_FAILED, FETCH_FAILED and
// FETCH_HTTP_STATUS. Messages never contain the URL path/query, proxy
// credentials or proxy path; at most the target host is included.
func Get(ctx context.Context, rawURL string, o Options) (*Result, error) {
	return get(ctx, rawURL, o, nil)
}

// get is Get with an optional TLS config whose RootCAs may trust a
// custom certificate authority (used by tests); nil means the system
// roots with verification always on.
func get(ctx context.Context, rawURL string, o Options, tlsCfg *tls.Config) (*Result, error) {
	target, err := parseTarget(rawURL)
	if err != nil {
		return nil, err
	}
	proxyURL, err := parseProxy(o.Proxy)
	if err != nil {
		return nil, err
	}
	timeout, maxBytes, ua := effective(o)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tr := newTransport(proxyURL, tlsCfg)
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		// Client.Timeout covers redirects and the body read.
		Timeout:       timeout,
		CheckRedirect: redirectPolicy,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url(), nil)
	if err != nil {
		return nil, urlInvalid("request could not be built")
	}
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyErr(err, target)
	}
	defer resp.Body.Close()

	// Status first: a non-2xx answer is FETCH_HTTP_STATUS regardless of
	// its body; at most statusDrainLimit is drained before closing.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drain(resp.Body, statusDrainLimit)
		return nil, &cli.Error{
			Code:    "FETCH_HTTP_STATUS",
			Message: fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode),
			Hint:    "the subscription server rejected the request",
			Exit:    cli.ExitNetwork,
		}
	}
	// A declared body over the cap is rejected before reading.
	if resp.ContentLength > maxBytes {
		return nil, fetchFailed(target, "response exceeds the size limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, classifyErr(err, target)
	}
	if int64(len(body)) > maxBytes {
		return nil, fetchFailed(target, "response exceeds the size limit")
	}
	return &Result{Body: body, Status: resp.StatusCode, Header: resp.Header}, nil
}

// newTransport builds the one transport used for the whole request.
// proxyURL == nil means an explicit direct connection: environment
// proxies (HTTP_PROXY/HTTPS_PROXY/ALL_PROXY) are never consulted because
// ProxyFromEnvironment is never installed. tlsCfg == nil keeps net/http
// defaults with certificate verification always on.
func newTransport(proxyURL *url.URL, tlsCfg *tls.Config) *http.Transport {
	tr := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     tlsCfg,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	return tr
}

// targetURL is a validated, secrets-free view of the request URL: only
// the host is ever exposed in errors.
type targetURL struct {
	raw *url.URL
}

func (t *targetURL) url() string { return t.raw.String() }

// host returns the hostname of the target; it never leaks path or query.
func (t *targetURL) host() string { return t.raw.Hostname() }

// effective applies the documented defaults for zero-valued options and
// clamps MaxBytes so maxBytes+1 cannot overflow.
func effective(o Options) (time.Duration, int64, string) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBytes := o.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes > math.MaxInt64-1 {
		maxBytes = math.MaxInt64 - 1
	}
	ua := o.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	return timeout, maxBytes, ua
}

func parseTarget(raw string) (*targetURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, urlInvalid("URL could not be parsed")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, urlInvalid("URL scheme must be http or https")
	}
	if u.Host == "" {
		return nil, urlInvalid("URL must have a host")
	}
	return &targetURL{raw: u}, nil
}

func parseProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, urlInvalid("proxy URL could not be parsed")
	}
	switch u.Scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return nil, urlInvalid("proxy scheme must be socks5, socks5h, http or https")
	}
	if u.Hostname() == "" {
		return nil, urlInvalid("proxy URL must have a host")
	}
	return u, nil
}

func urlInvalid(msg string) *cli.Error {
	return &cli.Error{
		Code:    "FETCH_URL_INVALID",
		Message: msg,
		Hint:    "use an absolute http(s) URL and a socks5(h)/http(s) proxy URL",
		Exit:    cli.ExitUsage,
	}
}

// fetchFailed builds a FETCH_FAILED error carrying at most the target
// host, defensively scrubbed of any secrets.
func fetchFailed(t *targetURL, reason string) *cli.Error {
	msg := redact(fmt.Sprintf("request to %s failed: %s", t.host(), reason), t.raw.String(), t.raw.RawQuery, t.raw.EscapedPath())
	return &cli.Error{
		Code:    "FETCH_FAILED",
		Message: msg,
		Hint:    "check connectivity to the subscription server",
		Exit:    cli.ExitNetwork,
	}
}

// isProxyFailure reports whether err is a failure to reach or negotiate
// with the proxy. GOROOT facts: net/http wraps proxy dial/TLS failures
// in a net.OpError with Op "proxyconnect" (net/http/transport.go,
// dialConn/wrapErr), and the bundled SOCKS5 client returns a net.OpError
// with Op "socks connect" for every handshake, negotiation and auth
// failure (net/http/socks_bundle.go, socksDialer.DialWithConn and
// socksCommand.String). A SOCKS refusal by the proxy therefore also maps
// here: the fetch could not be completed through the proxy.
func isProxyFailure(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "proxyconnect" || opErr.Op == "socks connect") {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if op, ok := e.(*net.OpError); ok && (op.Op == "proxyconnect" || op.Op == "socks connect") {
			return true
		}
	}
	return false
}

// classifyErr maps a transport-layer failure to FETCH_PROXY_FAILED (only
// when the proxy itself could not be reached or negotiated with) or
// FETCH_FAILED.
func classifyErr(err error, t *targetURL) *cli.Error {
	if isProxyFailure(err) {
		return &cli.Error{
			Code:    "FETCH_PROXY_FAILED",
			Message: "could not reach or negotiate with the configured proxy",
			Hint:    "check that the proxy is running and reachable",
			Exit:    cli.ExitNetwork,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fetchFailed(t, "request timed out or canceled")
	}
	return fetchFailed(t, "request failed")
}

// redirectPolicy enforces the redirect rules; errors are turned into
// FETCH_FAILED by classifyErr.
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return errors.New("redirect limit exceeded")
	}
	switch req.URL.Scheme {
	case "http", "https":
	default:
		return errors.New("redirect to a non-http scheme refused")
	}
	prev := via[len(via)-1].URL
	if prev.Scheme == "https" && req.URL.Scheme == "http" {
		return errors.New("https to http redirect downgrade refused")
	}
	return nil
}

// redact replaces every occurrence of each secret in s with "[redacted]".
func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "[redacted]")
	}
	return s
}

func drain(r io.Reader, limit int64) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, limit))
}
