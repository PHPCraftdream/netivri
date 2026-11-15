//go:build integration

package fetch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// invalidHost never resolves on this machine, so a direct fetch of it fails
// while a socks5h proxy that resolves it itself reaches the target.
const invalidHost = "subscription.invalid"

func reserveSocks(t *testing.T) (*socksProxy, string) {
	t.Helper()
	p := newSocksProxy(t, "", "")
	p.setResolve(invalidHost, "127.0.0.1")
	return p, "socks5h://" + p.addr()
}

// The direct attempt fails (the name does not resolve), the reserve proxy
// resolves it and brings the body: one connection to the proxy.
func TestReserveTakenWhenDirectFails(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, subBody) })
	p, reserve := reserveSocks(t)
	res, err := Get(context.Background(), "http://"+invalidHost+":"+portOf(t, srv.URL)+"/sub", Options{Reserve: reserve, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(res.Body) != subBody {
		t.Errorf("body = %q", res.Body)
	}
	conns, dests, _ := p.snapshot()
	if conns != 1 || len(dests) != 1 || dests[0] != invalidHost {
		t.Errorf("reserve proxy saw conns=%d dests=%v, want one connection to %s", conns, dests, invalidHost)
	}
}

// A direct fetch that works never touches the reserve proxy.
func TestReserveUntouchedWhenDirectWorks(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, subBody) })
	p := newHTTPProxy(t)
	res, err := Get(context.Background(), srv.URL+"/sub", Options{Reserve: "http://" + p.addr()})
	if err != nil || string(res.Body) != subBody {
		t.Fatalf("Get: %v", err)
	}
	if conns, _, _, _ := p.snapshot(); conns != 0 {
		t.Errorf("reserve proxy connections = %d, want 0", conns)
	}
}

// A server that refuses the address (403, 429, 451) is retried through the
// reserve; the second request carries the answer.
func TestReserveTakenWhenAddressRefused(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnavailableForLegalReasons} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var hits atomic.Int32
			srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				_, _ = fmt.Fprint(w, subBody)
			})
			p := newHTTPProxy(t)
			res, err := Get(context.Background(), srv.URL+"/sub", Options{Reserve: "http://" + p.addr()})
			if err != nil || string(res.Body) != subBody {
				t.Fatalf("Get: %v", err)
			}
			if conns, _, _, _ := p.snapshot(); conns != 1 {
				t.Errorf("reserve proxy connections = %d, want 1", conns)
			}
			if n := hits.Load(); n != 2 {
				t.Errorf("target requests = %d, want 2", n)
			}
		})
	}
}

// An answer a proxy would not change is not retried: a 404, a 401, a body over
// the limit.
func TestReserveNotTakenForAnswersAProxyWouldNotChange(t *testing.T) {
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		opts     Options
		wantCode string
	}{
		{"404", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }, Options{}, "FETCH_HTTP_STATUS"},
		{"401", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, Options{}, "FETCH_HTTP_STATUS"},
		{"too large", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, strings.Repeat("x", 100)) }, Options{MaxBytes: 10}, "FETCH_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				tc.handler(w, r)
			})
			p := newHTTPProxy(t)
			o := tc.opts
			o.Reserve = "http://" + p.addr()
			_, err := Get(context.Background(), srv.URL+"/sub", o)
			if ce := codeOf(t, err); ce.Code != tc.wantCode {
				t.Errorf("code = %s, want %s", ce.Code, tc.wantCode)
			}
			if strings.Contains(err.Error(), "reserve") {
				t.Errorf("error names the reserve proxy though it was not tried: %v", err)
			}
			if conns, _, _, _ := p.snapshot(); conns != 0 {
				t.Errorf("reserve proxy connections = %d, want 0", conns)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("target requests = %d, want 1", n)
			}
		})
	}
}

// When the reserve fails too, the error keeps the direct code and says how
// both ended, without the proxy address, the URL path or any credentials.
func TestReserveBothFail(t *testing.T) {
	closed := testutil.FreeLoopbackAddr(t)
	_, err := Get(context.Background(), "http://"+invalidHost+"/secret-token-abc/sub", Options{Reserve: "socks5h://user:hunter2@" + closed, Timeout: 3 * time.Second})
	ce := codeOf(t, err)
	if ce.Code != "FETCH_FAILED" || ce.Exit != cli.ExitNetwork {
		t.Errorf("code = %s exit = %d, want the direct FETCH_FAILED/%d", ce.Code, ce.Exit, cli.ExitNetwork)
	}
	for _, want := range []string{"(direct, ", "; then through the reserve proxy: could not reach or negotiate with the configured proxy"} {
		if !strings.Contains(ce.Message, want) {
			t.Errorf("message lacks %q: %q", want, ce.Message)
		}
	}
	for _, secret := range []string{closed, "127.0.0.1", "hunter2", "secret-token-abc"} {
		if strings.Contains(ce.Message, secret) {
			t.Errorf("message leaks %q: %q", secret, ce.Message)
		}
	}
}

// A set Proxy stays strict: the reserve is ignored, the target is not tried
// directly and the reserve proxy sees nothing.
func TestStrictProxyIgnoresReserve(t *testing.T) {
	closed := testutil.FreeLoopbackAddr(t)
	var hits atomic.Int32
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, subBody)
	})
	p := newHTTPProxy(t)
	_, err := Get(context.Background(), srv.URL+"/sub", Options{Proxy: "socks5://" + closed, Reserve: "http://" + p.addr(), Timeout: 3 * time.Second})
	if ce := codeOf(t, err); ce.Code != "FETCH_PROXY_FAILED" {
		t.Errorf("code = %s, want FETCH_PROXY_FAILED", ce.Code)
	}
	if conns, _, _, _ := p.snapshot(); conns != 0 || hits.Load() != 0 {
		t.Errorf("reserve proxy connections = %d, target requests = %d, want 0 and 0", conns, hits.Load())
	}
}

// A caller that gave up (a canceled context) does not get a second attempt.
func TestReserveSkippedWhenCallerCanceled(t *testing.T) {
	srv := newTarget(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, subBody) })
	p := newHTTPProxy(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Get(ctx, srv.URL+"/sub", Options{Reserve: "http://" + p.addr()})
	if ce := codeOf(t, err); ce.Code != "FETCH_FAILED" || strings.Contains(ce.Message, "reserve") {
		t.Errorf("got %s %q, want the plain direct failure", ce.Code, ce.Message)
	}
	if conns, _, _, _ := p.snapshot(); conns != 0 {
		t.Errorf("reserve proxy connections = %d, want 0", conns)
	}
}
