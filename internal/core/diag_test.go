package core

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
)

func TestDiagPercentile(t *testing.T) {
	cases := []struct {
		name string
		v    []int64
		p    int
		want int64
	}{
		{"empty", nil, 50, 0},
		{"single", []int64{7}, 50, 7},
		{"single-p100", []int64{7}, 100, 7},
		{"four-p50", []int64{1, 2, 3, 4}, 50, 2},
		{"four-p90", []int64{1, 2, 3, 4}, 90, 4},
		{"four-p100", []int64{1, 2, 3, 4}, 100, 4},
		{"ties", []int64{5, 5, 5}, 90, 5},
		{"unsorted-p50", []int64{5, 1, 3}, 50, 3},
		{"two-p50", []int64{10, 20}, 50, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := diagPercentile(c.v, c.p); got != c.want {
				t.Fatalf("diagPercentile(%v, %d) = %d, want %d", c.v, c.p, got, c.want)
			}
		})
	}
}

func TestDiagPhaseStats(t *testing.T) {
	if got := diagPhaseStats(nil); got != (PhaseStats{}) {
		t.Fatalf("empty: got %+v", got)
	}
	got := diagPhaseStats([]time.Duration{4 * time.Millisecond, time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond})
	if got.N != 4 || got.P50 != 2*time.Millisecond || got.P90 != 4*time.Millisecond || got.Max != 4*time.Millisecond {
		t.Fatalf("got %+v", got)
	}
}

func TestDiagSuggestTTL(t *testing.T) {
	cases := []struct {
		name string
		in   []IdleResult
		want time.Duration
	}{
		{"empty", nil, 0},
		{"all-ok", []IdleResult{{Idle: time.Second, OK: 1}, {Idle: 2 * time.Second, OK: 1}}, time.Second},
		{"fail-first", []IdleResult{{Idle: time.Second, Fail: 1}, {Idle: 2 * time.Second, OK: 1}}, 0},
		{"fail-middle", []IdleResult{{Idle: time.Second, OK: 1}, {Idle: 2 * time.Second, Fail: 1}, {Idle: 4 * time.Second, OK: 1}}, 500 * time.Millisecond},
		{"unsorted", []IdleResult{{Idle: 2 * time.Second, OK: 1}, {Idle: time.Second, OK: 1}}, time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := diagSuggestTTL(c.in); got != c.want {
				t.Fatalf("diagSuggestTTL(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestDiagSplitURL(t *testing.T) {
	host, port, path, err := diagSplitURL("http://example.com")
	if err != nil || host != "example.com" || port != 80 || path != "/" {
		t.Fatalf("default port: got %q %d %q err %v", host, port, path, err)
	}
	host, port, path, err = diagSplitURL("http://example.com:8080/p?q=1")
	if err != nil || host != "example.com" || port != 8080 || path != "/p?q=1" {
		t.Fatalf("explicit port: got %q %d %q err %v", host, port, path, err)
	}
	if _, _, _, err := diagSplitURL("https://example.com"); err == nil ||
		!strings.Contains(err.Error(), "https is not supported") {
		t.Fatalf("https: got err %v", err)
	}
	if _, _, _, err := diagSplitURL("ftp://example.com"); err == nil {
		t.Fatal("ftp scheme: want error")
	}
	if _, _, _, err := diagSplitURL("http://"); err == nil {
		t.Fatal("missing host: want error")
	}
	if _, _, _, err := diagSplitURL("ht tp://bad"); err == nil {
		t.Fatal("bad URL: want error")
	}
}

func TestNormalizeDiagOptions(t *testing.T) {
	base := DiagOptions{Nodes: []DiagNode{{Label: "n", Mapping: map[string]any{"type": "vless"}}}}
	if _, err := normalizeDiagOptions(DiagOptions{}); err == nil {
		t.Fatal("empty nodes: want error")
	}
	if _, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, Samples: -1}); err == nil {
		t.Fatal("negative samples: want error")
	}
	if _, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, Samples: diagMaxSamples + 1}); err == nil {
		t.Fatal("too many samples: want error")
	}
	if _, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, SampleTimeout: -time.Second}); err == nil {
		t.Fatal("negative timeout: want error")
	}
	if _, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, Idle: []time.Duration{-time.Second}}); err == nil {
		t.Fatal("negative idle: want error")
	}
	if _, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, URL: "https://example.com"}); err == nil {
		t.Fatal("https URL: want error")
	}

	o, err := normalizeDiagOptions(DiagOptions{Nodes: base.Nodes})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if o.Samples != diagDefaultSamples || o.SampleTimeout != diagDefaultSampleTimeout {
		t.Fatalf("defaults: samples %d timeout %v", o.Samples, o.SampleTimeout)
	}
	if len(o.Idle) != len(diagDefaultIdle) {
		t.Fatalf("default idle: got %v", o.Idle)
	}
	if o.URL != diagDefaultProbeURL {
		t.Fatalf("default URL: got %q", o.URL)
	}

	// An empty non-nil Idle slice must survive normalization (skip flag).
	o, err = normalizeDiagOptions(DiagOptions{Nodes: base.Nodes, Idle: []time.Duration{}})
	if err != nil {
		t.Fatalf("empty idle: %v", err)
	}
	if len(o.Idle) != 0 {
		t.Fatalf("empty idle must stay empty: got %v", o.Idle)
	}
}

// TestDiagDialParseErrorNoLeaks runs DiagDial against unparseable mappings
// holding secret-looking values: the error must not repeat any of them,
// including values the parser itself would quote (an invalid encryption
// string, a non-numeric port).
func TestDiagDialParseErrorNoLeaks(t *testing.T) {
	const (
		uuid = "leak-uuid-1111-2222-3333-444444444444"
		pw   = "leak-password-value"
		srv  = "leak-host.example"
		enc  = "leak-encryption-value"
		port = "leak-port-value"
	)
	mappings := map[string]map[string]any{
		"unknown type": {
			"type":     "nosuchtype",
			"server":   srv,
			"port":     1,
			"uuid":     uuid,
			"password": pw,
		},
		"bad encryption": {
			"type":       "vless",
			"server":     srv,
			"port":       1,
			"uuid":       uuid,
			"encryption": enc,
		},
		"bad port": {
			"type":     "vless",
			"server":   srv,
			"port":     port,
			"uuid":     uuid,
			"password": pw,
		},
	}
	for name, mapping := range mappings {
		t.Run(name, func(t *testing.T) {
			_, err := DiagDial(t.Context(), DiagOptions{
				Nodes: []DiagNode{{Label: "bad", Mapping: mapping}},
				URL:   "http://127.0.0.1:1/x",
			})
			if err == nil {
				t.Fatal("parse failure: want error")
			}
			msg := err.Error()
			for _, secret := range []string{uuid, pw, srv, enc, port} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error leaks %q: %s", secret, msg)
				}
			}
			if msg != "core: diag: node 0: invalid proxy mapping" {
				t.Fatalf("unexpected error: %s", msg)
			}
		})
	}
}

func TestDiagPhaseCollector(t *testing.T) {
	// tcp ok, tls failed: the tcp duration still counts, the failure is tls.
	c := &diagPhaseCollector{}
	c.ObservePhase(outbound.PhaseTCP, 5*time.Millisecond, nil)
	c.ObservePhase(outbound.PhaseTLS, 7*time.Millisecond, errors.New("handshake"))
	if d, ok := c.tcpDuration(); !ok || d != 5*time.Millisecond {
		t.Fatalf("tcpDuration = %v, %v", d, ok)
	}
	if _, ok := c.tlsDuration(); ok {
		t.Fatal("a failed tls phase must not report a duration")
	}
	if got := c.failureKey(); got != diagFailTLS {
		t.Fatalf("failureKey = %q, want tls", got)
	}

	// tcp failed: tcp failure, no durations.
	c = &diagPhaseCollector{}
	c.ObservePhase(outbound.PhaseTCP, time.Millisecond, errors.New("refused"))
	if _, ok := c.tcpDuration(); ok {
		t.Fatal("a failed tcp phase must not report a duration")
	}
	if got := c.failureKey(); got != diagFailTCP {
		t.Fatalf("failureKey = %q, want tcp", got)
	}

	// Nothing observed (for example a cancelled context): a tcp failure.
	c = &diagPhaseCollector{}
	if _, ok := c.tcpDuration(); ok {
		t.Fatal("an unobserved phase must not report a duration")
	}
	if got := c.failureKey(); got != diagFailTCP {
		t.Fatalf("failureKey = %q, want tcp", got)
	}
}

func TestDiagOneShotSource(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	s := &diagOneShotSource{conn: a}
	if got, ok := s.Take(t.Context()); !ok || got != a {
		t.Fatalf("first Take = %v, %v", got, ok)
	}
	if got, ok := s.Take(t.Context()); ok || got != nil {
		t.Fatalf("second Take = %v, %v, want a miss", got, ok)
	}
}
