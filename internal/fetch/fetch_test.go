package fetch

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		raw    string
		wantOK bool
	}{
		{"http://example.com/sub", true},
		{"https://example.com/sub", true},
		{"https://example.com", true},
		{"ftp://example.com/sub", false},
		{"/only/a/path", false},
		{"example.com/sub", false}, // parsed as opaque/path, no scheme
		{"http:///nohost", false},
		{"", false},
		{"://bad", false},
	}
	for _, tc := range cases {
		_, err := parseTarget(tc.raw)
		if tc.wantOK {
			if err != nil {
				t.Errorf("parseTarget(%q): unexpected error %v", redactForTest(tc.raw), err)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseTarget(%q): expected error", redactForTest(tc.raw))
			continue
		}
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "FETCH_URL_INVALID" || ce.Exit != cli.ExitUsage {
			t.Errorf("parseTarget(%q): got %v, want FETCH_URL_INVALID/ExitUsage", redactForTest(tc.raw), err)
		}
		// The offending URL must not be echoed in the message.
		if tc.raw != "" && containsStr(ce.Message, tc.raw) {
			t.Errorf("parseTarget(%q): message echoes URL: %q", redactForTest(tc.raw), ce.Message)
		}
	}
}

func TestParseProxy(t *testing.T) {
	cases := []struct {
		raw    string
		wantOK bool
	}{
		{"", true},
		{"socks5://127.0.0.1:1080", true},
		{"socks5h://user:pass@proxy.example:1080", true},
		{"http://proxy.example:8080", true},
		{"https://proxy.example", true},
		{"ftp://proxy.example", false},
		{"socks5://", false}, // no host
		{"socks5://host:1080", true},
		{"notaurl", false},
	}
	for _, tc := range cases {
		u, err := parseProxy(tc.raw)
		if tc.wantOK {
			if err != nil {
				t.Errorf("parseProxy(%q): unexpected error %v", redactForTest(tc.raw), err)
			}
			if tc.raw == "" && u != nil {
				t.Error("parseProxy(\"\"): expected nil URL")
			}
			continue
		}
		if err == nil {
			t.Errorf("parseProxy(%q): expected error", redactForTest(tc.raw))
			continue
		}
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "FETCH_URL_INVALID" || ce.Exit != cli.ExitUsage {
			t.Errorf("parseProxy(%q): got %v, want FETCH_URL_INVALID/ExitUsage", redactForTest(tc.raw), err)
		}
		if containsStr(ce.Message, tc.raw) && tc.raw != "" {
			t.Errorf("parseProxy(%q): message echoes proxy URL: %q", redactForTest(tc.raw), ce.Message)
		}
	}
}

func TestEffectiveDefaults(t *testing.T) {
	timeout, maxBytes, ua := effective(Options{})
	if timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", timeout)
	}
	if maxBytes != int64(16<<20) {
		t.Errorf("default maxBytes = %d, want %d", maxBytes, int64(16<<20))
	}
	if ua != "netivri" {
		t.Errorf("default user agent = %q", ua)
	}
	timeout, maxBytes, ua = effective(Options{Timeout: time.Second, MaxBytes: 5, UserAgent: "x"})
	if timeout != time.Second || maxBytes != 5 || ua != "x" {
		t.Error("explicit options must be preserved")
	}
}

func TestEffectiveClampsMaxBytes(t *testing.T) {
	_, maxBytes, _ := effective(Options{MaxBytes: math.MaxInt64})
	if maxBytes != math.MaxInt64-1 {
		t.Errorf("maxBytes = %d, want clamped to %d", maxBytes, math.MaxInt64-1)
	}
}

func TestRedact(t *testing.T) {
	got := redact("token=secret123&x=1 secret123 password", "secret123", "password")
	for _, bad := range []string{"secret123", "password"} {
		if containsStr(got, bad) {
			t.Errorf("redact output contains %q: %q", bad, got)
		}
	}
	if got != "token=[redacted]&x=1 [redacted] [redacted]" {
		t.Errorf("redact output = %q", got)
	}
	if redact("unchanged", "") != "unchanged" {
		t.Error("empty secret must be ignored")
	}
}

func TestErrorMapping(t *testing.T) {
	tu, err := parseTarget("https://example.com/secret/token123?tok=abc")
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{"token123", "abc", "example.com/secret"}

	e := fetchFailed(tu, "request failed")
	if e.Code != "FETCH_FAILED" || e.Exit != cli.ExitNetwork {
		t.Errorf("fetchFailed: got %s/%d", e.Code, e.Exit)
	}
	for _, sec := range secrets {
		if containsStr(e.Error(), sec) {
			t.Errorf("FETCH_FAILED leaks %q: %q", sec, e.Error())
		}
	}

	proxyDial := &net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("dial tcp 10.0.0.1:1080: connect: connection refused")}
	ce := classifyErr(proxyDial, tu)
	if ce.Code != "FETCH_PROXY_FAILED" || ce.Exit != cli.ExitNetwork {
		t.Errorf("proxyconnect mapping: got %s/%d", ce.Code, ce.Exit)
	}

	socksRefused := fmt.Errorf("Get %q: %w", "[url]", &net.OpError{Op: "socks connect", Net: "tcp", Err: errors.New("authentication failed")})
	ce = classifyErr(socksRefused, tu)
	if ce.Code != "FETCH_PROXY_FAILED" || ce.Exit != cli.ExitNetwork {
		t.Errorf("socks connect mapping: got %s/%d", ce.Code, ce.Exit)
	}
	for _, sec := range []string{"user", "pass", "10.0.0.1", "token123"} {
		if containsStr(ce.Error(), sec) {
			t.Errorf("FETCH_PROXY_FAILED leaks %q: %q", sec, ce.Error())
		}
	}
}

func TestIsProxyFailure(t *testing.T) {
	if !isProxyFailure(&net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("refused")}) {
		t.Error("proxyconnect OpError must be a proxy failure")
	}
	wrapped := fmt.Errorf("Get http://x: %w", &net.OpError{Op: "socks connect", Net: "tcp", Err: errors.New("no acceptable method")})
	if !isProxyFailure(wrapped) {
		t.Error("wrapped socks connect OpError must be a proxy failure")
	}
	if isProxyFailure(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}) {
		t.Error("plain dial OpError must not be a proxy failure")
	}
	if isProxyFailure(errors.New("plain")) {
		t.Error("non-OpError must not be a proxy failure")
	}
	if isProxyFailure(nil) {
		t.Error("nil must not be a proxy failure")
	}
}

func TestTransportIgnoresEnvironment(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")

	direct := newTransport(nil, nil)
	if direct.Proxy != nil {
		t.Fatal("newTransport(nil) must keep Proxy nil so env proxies are never consulted")
	}

	proxyURL, err := url.Parse("socks5h://proxy.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	tr := newTransport(proxyURL, nil)
	if tr.Proxy == nil {
		t.Fatal("newTransport(proxyURL) must install http.ProxyURL(proxyURL)")
	}
	for _, raw := range []string{"http://example.com/", "https://example.com/"} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("Proxy(%s): %v", raw, err)
		}
		if got != proxyURL {
			t.Errorf("Proxy(%s) = %v, want exactly the configured proxy URL", raw, got)
		}
	}
}

func TestTargetURLSecretBoundaries(t *testing.T) {
	tu, err := parseTarget("https://host.example/very/secret/path?token=topsecret")
	if err != nil {
		t.Fatal(err)
	}
	if tu.host() != "host.example" {
		t.Errorf("host() = %q, want host.example", tu.host())
	}
	// The request URL must keep the query (the server needs the token);
	// error paths only ever use host().
	full, err := url.Parse(tu.url())
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(full.RawQuery, "topsecret") {
		t.Fatalf("request URL must preserve the query, got %q", redactForTest(full.RawQuery))
	}
}

func TestRedirectPolicy(t *testing.T) {
	mk := func(scheme, host string) *url.URL {
		u, _ := url.Parse(scheme + "://" + host + "/")
		return u
	}
	build := func(chain ...*url.URL) ([]*http.Request, *http.Request) {
		var via []*http.Request
		for _, u := range chain {
			via = append(via, &http.Request{URL: u})
		}
		return via, &http.Request{URL: mk("http", "next.example")}
	}
	via, next := build(mk("http", "a.example"))
	if err := redirectPolicy(next, via); err != nil {
		t.Errorf("plain http redirect: %v", err)
	}

	via, next = build(mk("https", "a.example"))
	if err := redirectPolicy(&http.Request{URL: mk("http", "b.example")}, via); err == nil {
		t.Error("https->http downgrade must be refused")
	}

	via, next = build(mk("https", "a.example"))
	if err := redirectPolicy(&http.Request{URL: mk("ftp", "b.example")}, via); err == nil {
		t.Error("non-http redirect scheme must be refused")
	}

	chain := []*http.Request{}
	for i := 0; i < MaxRedirects; i++ {
		chain = append(chain, &http.Request{URL: mk("http", "a.example")})
	}
	if err := redirectPolicy(next, chain); err == nil {
		t.Error("a sixth hop (via length MaxRedirects) must be refused")
	}
	if err := redirectPolicy(next, chain[:MaxRedirects-1]); err != nil {
		t.Errorf("MaxRedirects hops must be allowed: %v", err)
	}
}

func containsStr(s, sub string) bool {
	return sub != "" && strings.Contains(s, sub)
}

func redactForTest(s string) string { return "[url]" }
