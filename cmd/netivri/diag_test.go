package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// capturedDiagOptions holds what the fake diagDial received.
var capturedDiagOptions core.DiagOptions

// fakeDiagDial replaces diagDial with a fake that records the options and
// returns a fixture. Tests must call it through useDiagDial so the
// production var is restored.
func fakeDiagDial(ctx context.Context, o core.DiagOptions) ([]core.NodeDiag, error) {
	capturedDiagOptions = o
	return []core.NodeDiag{{
		Label:           "main/a1",
		TransportPhases: true,
		Samples:         o.Samples,
		TCP:             core.PhaseStats{N: 2, P50: 1500 * time.Microsecond, P90: 2500 * time.Microsecond, Max: 3 * time.Millisecond},
		FirstByte:       core.PhaseStats{N: 2, P50: 10 * time.Millisecond, P90: 20 * time.Millisecond, Max: 30 * time.Millisecond},
		Failures:        map[string]int{"first_byte": 1},
		Idle:            []core.IdleResult{{Idle: 500 * time.Millisecond, OK: 1}},
	}}, nil
}

// useDiagDial installs fake as diagDial for the test and restores it.
func useDiagDial(t *testing.T, fake func(context.Context, core.DiagOptions) ([]core.NodeDiag, error)) {
	t.Helper()
	old := diagDial
	diagDial = fake
	t.Cleanup(func() { diagDial = old })
}

// diagHome seeds a home with one provider "main" holding two nodes and an
// account "alice" bound to that provider.
func diagHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://secret.invalid/sub"}}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "main"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := subscription.NewStore(home + "/subscriptions")
	snap := subscription.Snapshot{Nodes: []subscription.Node{
		{ID: "a1", Country: "DE", Server: "srv1.secret.example", Port: 8388, Raw: map[string]any{"name": "n1", "uuid": "u-111", "server": "srv1.secret.example"}},
		{ID: "b2", Country: "NL", Server: "srv2.secret.example", Port: 8389, Raw: map[string]any{"name": "n2", "uuid": "u-222", "server": "srv2.secret.example"}},
	}}
	if err := store.Save("main", snap); err != nil {
		t.Fatal(err)
	}
	return home
}

// diagArgs builds the arguments of `diag dial ... --home home`.
func diagArgs(home string, args ...string) []string {
	out := append([]string{"dial"}, args...)
	return append(out, "--home", home)
}

// runDiagOK runs runDiag against home and returns the decoded data and the
// raw output bytes.
func runDiagOK(t *testing.T, home string, args ...string) (map[string]any, []byte) {
	t.Helper()
	var out bytes.Buffer
	if err := runDiag(diagArgs(home, args...), &out); err != nil {
		t.Fatalf("diag %v: %v", args, err)
	}
	return decodeData(t, &out), out.Bytes()
}

// runDiagErr runs runDiag and returns the *cli.Error failure.
func runDiagErr(t *testing.T, home string, args ...string) *cli.Error {
	t.Helper()
	var out bytes.Buffer
	err := runDiag(diagArgs(home, args...), &out)
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("diag %v: want *cli.Error, got %v", args, err)
	}
	return ce
}

func TestDiagFlagValidation(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	cases := []struct {
		name  string
		args  []string
		code  string
		plain bool
	}{
		{"both selectors", []string{"--account", "alice", "--node", "main/a1"}, "USAGE", false},
		{"neither selector", []string{}, "USAGE", false},
		{"samples zero", []string{"--account", "alice", "--samples", "0"}, "USAGE", false},
		{"samples too many", []string{"--account", "alice", "--samples", "21"}, "USAGE", false},
		{"samples negative", []string{"--account", "alice", "--samples", "-1"}, "USAGE", false},
		{"max-nodes zero", []string{"--account", "alice", "--max-nodes", "0"}, "USAGE", false},
		{"max-nodes too many", []string{"--account", "alice", "--max-nodes", "51"}, "USAGE", false},
		{"idle bogus", []string{"--account", "alice", "--idle", "bogus"}, "USAGE", false},
		{"idle mixed bad", []string{"--account", "alice", "--idle", "500ms,xyz"}, "USAGE", false},
		{"idle non positive", []string{"--account", "alice", "--idle", "0s"}, "USAGE", false},
		{"idle too many", []string{"--account", "alice", "--idle", "1s,2s,3s,4s,5s,6s,7s,8s,9s,10s,11s"}, "USAGE", false},
		{"url https", []string{"--account", "alice", "--url", "https://example.com/x"}, "USAGE", false},
		{"url broken", []string{"--account", "alice", "--url", "://bad"}, "USAGE", false},
		{"positional", []string{"--account", "alice", "extra"}, "USAGE", false},
		{"unknown account", []string{"--account", "nobody"}, "ACCOUNT_NOT_FOUND", false},
		{"account without route", []string{"--account", "plain"}, "NO_ROUTE", true},
		{"url without host", []string{"--account", "alice", "--url", "http:///x"}, "USAGE", false},
		{"url port zero", []string{"--account", "alice", "--url", "http://probe.example:0/x"}, "USAGE", false},
		{"url port too large", []string{"--account", "alice", "--url", "http://probe.example:70000/x"}, "USAGE", false},
		{"node not found", []string{"--node", "main/zzzz"}, "NODE_NOT_FOUND", false},
		{"node label without slash", []string{"--node", "a1"}, "NODE_NOT_FOUND", false},
	}
	plainHome := diagHomeWithPlainAccount(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := plainHome
			if !tc.plain {
				home = diagHome(t)
			}
			ce := runDiagErr(t, home, tc.args...)
			if ce.Code != tc.code {
				t.Fatalf("code = %q (%s), want %q", ce.Code, ce.Message, tc.code)
			}
		})
	}
}

// diagHomeWithPlainAccount seeds the fixtures of diagHome plus an account
// with neither provider nor country.
func diagHomeWithPlainAccount(t *testing.T) string {
	t.Helper()
	home := diagHome(t)
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = append(cfg.Accounts, config.Account{Login: "plain", Password: config.InitPlaceholder})
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestDiagSubcommand(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	for _, args := range [][]string{nil, {"bogus"}, {"--account", "alice"}} {
		var out bytes.Buffer
		err := runDiag(args, &out)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "USAGE" {
			t.Fatalf("runDiag(%v) = %v, want a USAGE error", args, err)
		}
	}
	// dispatch routes `diag dial` to runDiag.
	home := diagHome(t)
	var out bytes.Buffer
	if err := dispatch([]string{"diag", "dial", "--node", "main/a1", "--home", home}, &out); err != nil {
		t.Fatalf("dispatch diag dial: %v", err)
	}
}

func TestDiagAcceptedFlagsReachOptions(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	home := diagHome(t)
	if err := runDiag([]string{"dial", "--account", "alice", "--samples", "1", "--idle", "off", "--max-nodes", "1", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if capturedDiagOptions.Samples != 1 {
		t.Fatalf("samples = %d, want 1", capturedDiagOptions.Samples)
	}
	if capturedDiagOptions.Idle == nil || len(capturedDiagOptions.Idle) != 0 {
		t.Fatalf("idle = %#v, want empty non-nil", capturedDiagOptions.Idle)
	}
	if capturedDiagOptions.URL != diagProbeURL {
		t.Fatalf("url = %q, want default", capturedDiagOptions.URL)
	}
	if len(capturedDiagOptions.Nodes) != 1 || capturedDiagOptions.Nodes[0].Label != "main/a1" {
		t.Fatalf("nodes = %#v", capturedDiagOptions.Nodes)
	}

	// --idle list is sorted ascending; explicit --url is kept.
	if err := runDiag([]string{"dial", "--node", "main/a1", "--idle", "2s,500ms", "--url", "http://probe.example/ping", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	idle := capturedDiagOptions.Idle
	if len(idle) != 2 || idle[0] != 500*time.Millisecond || idle[1] != 2*time.Second {
		t.Fatalf("idle = %v, want [500ms 2s]", idle)
	}
	if capturedDiagOptions.URL != "http://probe.example/ping" {
		t.Fatalf("url = %q", capturedDiagOptions.URL)
	}
}

func TestDiagJSONShape(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	home := diagHome(t)
	data, raw := runDiagOK(t, home, "--node", "main/a1", "--url", "http://probe.example/x")
	if data["url_host"] != "probe.example" {
		t.Fatalf("url_host = %v", data["url_host"])
	}
	if data["samples"] != float64(5) {
		t.Fatalf("samples = %v", data["samples"])
	}
	nodes, ok := data["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("nodes = %#v", data["nodes"])
	}
	n := nodes[0].(map[string]any)
	if n["label"] != "main/a1" || n["transport_phases"] != true || n["samples"] != float64(5) {
		t.Fatalf("node header = %v", n)
	}
	phases := n["phases_ms"].(map[string]any)
	// DNS has N=0 in the fixture and must be omitted.
	if _, there := phases["dns"]; there {
		t.Fatalf("dns phase present despite N=0: %v", phases)
	}
	tcp := phases["tcp"].(map[string]any)
	if tcp["n"] != float64(2) || tcp["p50"] != 1.5 || tcp["p90"] != 2.5 || tcp["max"] != float64(3) {
		t.Fatalf("tcp = %v", tcp)
	}
	fb := phases["first_byte"].(map[string]any)
	if fb["p50"] != float64(10) || fb["p90"] != float64(20) || fb["max"] != float64(30) {
		t.Fatalf("first_byte = %v", fb)
	}
	failures := n["failures"].(map[string]any)
	for _, key := range []string{"dns", "tcp", "tls", "first_byte"} {
		if _, there := failures[key]; !there {
			t.Fatalf("failures missing %q: %v", key, failures)
		}
	}
	if failures["first_byte"] != float64(1) || failures["dns"] != float64(0) {
		t.Fatalf("failures = %v", failures)
	}
	idle := n["idle_survival"].([]any)[0].(map[string]any)
	if idle["idle_ms"] != float64(500) || idle["ok"] != float64(1) || idle["fail"] != float64(0) {
		t.Fatalf("idle_survival = %v", idle)
	}
	// SuggestedPoolTTL is 0 in the fixture and must be omitted.
	if _, there := n["suggested_pool_ttl_ms"]; there {
		t.Fatalf("suggested_pool_ttl_ms present despite 0: %v", n)
	}
	// No secret strings anywhere in the output.
	for _, secret := range []string{"srv1.secret.example", "u-111", "secret.invalid"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("output leaks %q:\n%s", secret, raw)
		}
	}
}

func TestDiagSuggestedTTLAndNonHTTPNode(t *testing.T) {
	useDiagDial(t, func(ctx context.Context, o core.DiagOptions) ([]core.NodeDiag, error) {
		return []core.NodeDiag{{
			Label:            "main/b2",
			TransportPhases:  false,
			Samples:          3,
			SuggestedPoolTTL: 1500 * time.Millisecond,
		}}, nil
	})
	home := diagHome(t)
	data, _ := runDiagOK(t, home, "--node", "main/b2")
	n := data["nodes"].([]any)[0].(map[string]any)
	if n["suggested_pool_ttl_ms"] != float64(1500) {
		t.Fatalf("suggested_pool_ttl_ms = %v", n["suggested_pool_ttl_ms"])
	}
	phases := n["phases_ms"].(map[string]any)
	if len(phases) != 0 {
		t.Fatalf("phases_ms = %v, want empty", phases)
	}
	if _, there := n["idle_survival"]; there {
		t.Fatalf("idle_survival present despite none: %v", n)
	}
}

func TestDiagAccountRouteNodesCappedByMaxNodes(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	home := diagHome(t)
	if err := runDiag([]string{"dial", "--account", "alice", "--max-nodes", "1", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(capturedDiagOptions.Nodes) != 1 || capturedDiagOptions.Nodes[0].Label != "main/a1" {
		t.Fatalf("nodes = %#v, want capped to [main/a1]", capturedDiagOptions.Nodes)
	}
	if m := capturedDiagOptions.Nodes[0].Mapping; m["uuid"] != "u-111" {
		t.Fatalf("mapping not passed through: %v", m)
	}
	// Without the cap both route nodes are diagnosed.
	if err := runDiag([]string{"dial", "--account", "alice", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(capturedDiagOptions.Nodes) != 2 {
		t.Fatalf("nodes = %#v, want 2", capturedDiagOptions.Nodes)
	}
}

func TestDiagNodeResolution(t *testing.T) {
	useDiagDial(t, fakeDiagDial)
	home := diagHome(t)
	if err := runDiag([]string{"dial", "--node", "main/b2", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(capturedDiagOptions.Nodes) != 1 || capturedDiagOptions.Nodes[0].Label != "main/b2" {
		t.Fatalf("nodes = %#v", capturedDiagOptions.Nodes)
	}
	ce := runDiagErr(t, home, "--node", "unknown/a1")
	if ce.Code != "NODE_NOT_FOUND" || ce.Message != "node not found: unknown/a1" {
		t.Fatalf("error = %#v", ce)
	}
	if strings.Contains(strings.ToLower(ce.Message), "http") || strings.Contains(ce.Message, "://") {
		t.Fatalf("error message leaks an address: %q", ce.Message)
	}
}

func TestDiagDiagFailedWrapsCoreError(t *testing.T) {
	useDiagDial(t, func(ctx context.Context, o core.DiagOptions) ([]core.NodeDiag, error) {
		return nil, errors.New("no such node adapter")
	})
	home := diagHome(t)
	ce := runDiagErr(t, home, "--node", "main/a1")
	if ce.Code != "DIAG_FAILED" || ce.Exit != cli.ExitNetwork {
		t.Fatalf("error = %#v", ce)
	}
}

func TestDiagDiagFailedPassesCLIErrorThrough(t *testing.T) {
	useDiagDial(t, func(ctx context.Context, o core.DiagOptions) ([]core.NodeDiag, error) {
		return nil, cli.Usage("core-level usage")
	})
	home := diagHome(t)
	ce := runDiagErr(t, home, "--node", "main/a1")
	if ce.Code != "USAGE" {
		t.Fatalf("error = %#v", ce)
	}
}
