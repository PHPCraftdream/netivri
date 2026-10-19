//go:build integration

package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

const subBody = "hello-subscription"

func newTarget(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func codeOf(t *testing.T, err error) *cli.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cli.Error, got %T: %v", err, err)
	}
	return ce
}

// (a) via HTTP proxy: success, proxy counter == 1, body intact.
func TestViaHTTPProxy(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=3; expire=4")
		fmt.Fprint(w, subBody)
	})
	p := newHTTPProxy(t)
	res, err := Get(context.Background(), srv.URL+"/sub", Options{Proxy: "http://" + p.addr()})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(res.Body) != subBody || res.Status != 200 {
		t.Errorf("bad result: status=%d body=%q", res.Status, res.Body)
	}
	if res.Header.Get("Subscription-Userinfo") == "" {
		t.Error("response headers must be exposed")
	}
	conns, _, dests, _ := p.snapshot()
	if conns != 1 {
		t.Errorf("proxy connections = %d, want 1", conns)
	}
	if len(dests) != 1 || dests[0] != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("proxy saw dests %v", dests)
	}
}

// (b) via SOCKS5: success; the proxy saw the target as a DOMAIN NAME and
// resolved it itself, proving the target hostname was never resolved
// locally.
func TestViaSocks5DomainName(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, subBody)
	})
	targetPort := portOf(t, srv.URL)
	p := newSocksProxy(t, "", "")
	p.setResolve("subscription.invalid", "127.0.0.1")
	res, err := Get(context.Background(),
		"http://subscription.invalid:"+targetPort+"/sub",
		Options{Proxy: "socks5h://" + p.addr()})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(res.Body) != subBody {
		t.Errorf("body = %q", res.Body)
	}
	_, dests, _ := p.snapshot()
	if len(dests) != 1 || dests[0] != "subscription.invalid" {
		t.Fatalf("socks proxy saw dests %v, want [subscription.invalid]", dests)
	}
}

// (c) proxy unreachable: FETCH_PROXY_FAILED and the target saw ZERO
// connections (no direct fallback).
func TestProxyUnreachableNoFallback(t *testing.T) {
	closed := testutil.FreeLoopbackAddr(t) // reserved then closed
	var targetHits atomic.Int32
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		fmt.Fprint(w, subBody)
	})
	cases := []struct{ name, proxy string }{
		{"socks5", "socks5://" + closed},
		{"socks5h", "socks5h://" + closed},
		{"http", "http://" + closed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Get(context.Background(), srv.URL+"/sub", Options{Proxy: tc.proxy, Timeout: 3 * time.Second})
			ce := codeOf(t, err)
			if ce.Code != "FETCH_PROXY_FAILED" || ce.Exit != cli.ExitNetwork {
				t.Errorf("code = %s exit = %d, want FETCH_PROXY_FAILED/%d", ce.Code, ce.Exit, cli.ExitNetwork)
			}
			if strings.Contains(ce.Message, closed) || strings.Contains(ce.Message, "127.0.0.1") {
				t.Errorf("error echoes proxy address: %q", ce.Message)
			}
		})
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("target saw %d connections, want 0", n)
	}
}

// (e) redirect chain goes through the proxy, one connection per hop; a
// redirect loop stops after exactly MaxRedirects server requests; a
// non-http redirect is refused.
func TestRedirects(t *testing.T) {
	var loopHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/r1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/r2", http.StatusFound)
	})
	mux.HandleFunc("/r2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/r3", http.StatusFound)
	})
	mux.HandleFunc("/r3", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, subBody)
	})
	mux.HandleFunc("/scheme", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://redirect.invalid/x", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		loopHits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := newTarget(t, mux.ServeHTTP)
	p := newHTTPProxy(t)

	res, err := Get(context.Background(), srv.URL+"/r1", Options{Proxy: "http://" + p.addr()})
	if err != nil {
		t.Fatalf("redirect chain: %v", err)
	}
	if string(res.Body) != subBody {
		t.Errorf("body = %q", res.Body)
	}
	conns, _, _, _ := p.snapshot()
	if conns < 3 {
		t.Errorf("proxy connections = %d, want >= 3 (one per hop)", conns)
	}

	_, err = Get(context.Background(), srv.URL+"/scheme", Options{Proxy: "http://" + p.addr()})
	ce := codeOf(t, err)
	if ce.Code != "FETCH_FAILED" {
		t.Errorf("non-http redirect: code = %s, want FETCH_FAILED", ce.Code)
	}
	if strings.Contains(ce.Message, "redirect.invalid") || strings.Contains(ce.Message, "ftp") {
		t.Errorf("error echoes redirect target: %q", ce.Message)
	}

	_, err = Get(context.Background(), srv.URL+"/loop", Options{Proxy: "http://" + p.addr()})
	ce = codeOf(t, err)
	if ce.Code != "FETCH_FAILED" {
		t.Errorf("redirect loop: code = %s, want FETCH_FAILED", ce.Code)
	}
	if strings.Contains(ce.Message, "/loop") {
		t.Errorf("error echoes URL path: %q", ce.Message)
	}
	if n := loopHits.Load(); n != MaxRedirects {
		t.Errorf("server saw %d /loop requests, want exactly %d", n, MaxRedirects)
	}
}

// (f) a body larger than MaxBytes is an error, never a truncation.
func TestBodyTooLarge(t *testing.T) {
	tooBig := strings.Repeat("x", 100)
	setCL := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(tooBig)))
		fmt.Fprint(w, tooBig)
	})
	chunked := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, tooBig)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	for name, srv := range map[string]*httptest.Server{"content-length": setCL, "chunked": chunked} {
		t.Run(name, func(t *testing.T) {
			res, err := Get(context.Background(), srv.URL+"/big", Options{MaxBytes: 10})
			if err == nil {
				t.Fatalf("expected error, got body len %d", len(res.Body))
			}
			ce := codeOf(t, err)
			if ce.Code != "FETCH_FAILED" || ce.Exit != cli.ExitNetwork {
				t.Errorf("code = %s exit = %d", ce.Code, ce.Exit)
			}
			if strings.Contains(ce.Message, "xxxx") {
				t.Error("error must not contain body data")
			}
		})
	}
}

// (g) timeout: a server that never answers must fail fast with
// FETCH_FAILED.
func TestTimeout(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	start := time.Now()
	_, err := Get(context.Background(), srv.URL+"/slow", Options{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	ce := codeOf(t, err)
	if ce.Code != "FETCH_FAILED" || ce.Exit != cli.ExitNetwork {
		t.Errorf("code = %s exit = %d", ce.Code, ce.Exit)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("timeout took %v, want ~300ms", el)
	}
}

// (h) non-2xx maps to FETCH_HTTP_STATUS; the message carries only the
// numeric status. The status is reported even though the body is large,
// proving status is checked before the body.
func TestHTTPStatus(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, strings.Repeat("x", 4096))
	})
	_, err := Get(context.Background(), srv.URL+"/secret-token-abc/sub", Options{})
	ce := codeOf(t, err)
	if ce.Code != "FETCH_HTTP_STATUS" || ce.Exit != cli.ExitNetwork {
		t.Errorf("code = %s exit = %d", ce.Code, ce.Exit)
	}
	if !strings.Contains(ce.Message, "403") {
		t.Errorf("message must carry the numeric status: %q", ce.Message)
	}
	if strings.Contains(ce.Message, "secret-token-abc") || strings.Contains(ce.Message, "xxxx") {
		t.Errorf("message leaks URL path or body: %q", ce.Message)
	}
}

// (i) proxy credentials are sent to the proxy and appear in no error
// text; a secret token in the target path appears in no error text.
func TestCredentialsAndSecrets(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, subBody)
	})
	targetPort := portOf(t, srv.URL)

	// SOCKS5 username/password: success proves auth was accepted.
	sp := newSocksProxy(t, "alice", "s3cret")
	sp.setResolve("subscription.invalid", "127.0.0.1")
	_, err := Get(context.Background(),
		"http://subscription.invalid:"+targetPort+"/sub",
		Options{Proxy: "socks5h://alice:s3cret@" + sp.addr()})
	if err != nil {
		t.Fatalf("socks5 with credentials: %v", err)
	}
	if _, _, rejects := sp.snapshot(); rejects != 0 {
		t.Errorf("socks proxy rejected %d auths", rejects)
	}

	// Wrong SOCKS5 password: the proxy refuses the fetch, so the code is
	// FETCH_PROXY_FAILED and the text must not contain credentials.
	_, err = Get(context.Background(),
		"http://subscription.invalid:"+targetPort+"/sub",
		Options{Proxy: "socks5h://alice:WRONG-pass@" + sp.addr(), Timeout: 3 * time.Second})
	ce := codeOf(t, err)
	if ce.Code != "FETCH_PROXY_FAILED" {
		t.Errorf("bad socks credentials: code = %s", ce.Code)
	}
	for _, sec := range []string{"alice", "WRONG-pass", "s3cret"} {
		if strings.Contains(ce.Error(), sec) {
			t.Errorf("error leaks proxy credential %q: %q", sec, ce.Error())
		}
	}

	// HTTP proxy userinfo becomes Proxy-Authorization.
	hp := newHTTPProxy(t)
	proxyAddr := hp.addr()
	res, err := Get(context.Background(), srv.URL+"/sub", Options{Proxy: "http://bob:p4ss@" + proxyAddr})
	if err != nil {
		t.Fatalf("http proxy with credentials: %v", err)
	}
	if string(res.Body) != subBody {
		t.Errorf("body = %q", res.Body)
	}
	_, _, _, auths := hp.snapshot()
	if len(auths) == 0 || auths[0] == "" {
		t.Errorf("proxy saw no Proxy-Authorization: %v", auths)
	}

	// HTTP proxy rejecting the credentials: the error text must not
	// contain the credentials or the proxy address.
	hp.setWantAuth("Basic gated")
	_, err = Get(context.Background(), srv.URL+"/sub", Options{Proxy: "http://bob:WRONG-pass@" + proxyAddr, Timeout: 3 * time.Second})
	ce = codeOf(t, err)
	for _, sec := range []string{"bob", "p4ss", "WRONG-pass"} {
		if strings.Contains(ce.Error(), sec) {
			t.Errorf("error leaks proxy credential %q: %q", sec, ce.Error())
		}
	}
	if strings.Contains(ce.Error(), proxyAddr) {
		t.Errorf("error echoes proxy address: %q", ce.Error())
	}
}

// (extra) HTTPS target through the HTTP CONNECT proxy, trusting the test
// CA via the internal tlsCfg hook.
func TestHTTPSTargetViaConnectProxy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, subBody)
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsCfg := &tls.Config{RootCAs: pool}

	p := newHTTPProxy(t)
	res, err := get(context.Background(), srv.URL+"/sub",
		Options{Proxy: "http://" + p.addr()}, tlsCfg)
	if err != nil {
		t.Fatalf("Get via CONNECT: %v", err)
	}
	if string(res.Body) != subBody || res.Status != 200 {
		t.Errorf("bad result: status=%d body=%q", res.Status, res.Body)
	}
	conns, connects, dests, _ := p.snapshot()
	if conns != 1 {
		t.Errorf("proxy connections = %d, want 1", conns)
	}
	if connects != 1 {
		t.Errorf("proxy saw %d CONNECT requests, want 1", connects)
	}
	if len(dests) != 1 || dests[0] != strings.TrimPrefix(srv.URL, "https://") {
		t.Errorf("proxy CONNECTed to %v", dests)
	}
}

// (extra) an HTTPS target with a certificate the fetcher does not trust
// must fail: TLS verification is always on.
func TestHTTPSUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, subBody)
	}))
	t.Cleanup(srv.Close)
	_, err := Get(context.Background(), srv.URL+"/sub", Options{Timeout: 5 * time.Second})
	ce := codeOf(t, err)
	if ce.Code != "FETCH_FAILED" {
		t.Errorf("code = %s, want FETCH_FAILED", ce.Code)
	}
}

// (extra) a proxy that accepts TCP but never answers the SOCKS greeting:
// the fetch must fail within its timeout and the fake proxy's handler
// must unblock afterwards (no goroutine leak).
func TestSilentSocksProxyTimesOut(t *testing.T) {
	p := newSilentSocksProxy(t)
	start := time.Now()
	_, err := Get(context.Background(), "http://subscription.invalid:80/sub",
		Options{Proxy: "socks5h://" + p.addr(), Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	ce := codeOf(t, err)
	if ce.Code != "FETCH_FAILED" && ce.Code != "FETCH_PROXY_FAILED" {
		t.Errorf("code = %s, want FETCH_FAILED or FETCH_PROXY_FAILED", ce.Code)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("fetch took %v, want ~300ms", el)
	}
	if !p.waitHandlers(3 * time.Second) {
		t.Error("silent proxy handler still blocked after the fetch failed")
	}
}

func portOf(t *testing.T, urlStr string) string {
	t.Helper()
	rest := strings.TrimPrefix(strings.TrimPrefix(urlStr, "http://"), "https://")
	_, port, err := net.SplitHostPort(rest)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	return port
}
