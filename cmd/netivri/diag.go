package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

const (
	defaultDiagSamples  = 5
	maxDiagSamples      = 20
	defaultDiagMaxNodes = 6
	maxDiagMaxNodes     = 50
	// maxDiagIdles caps how many idle times the idle survival test uses.
	maxDiagIdles = 10
	// diagProbeURL is the probe target; it is the same URL the warm-up uses
	// (internal/core/warm.go defaultProbeURL, unexported there). Keep the
	// two in sync. The host is the only address the output ever shows.
	diagProbeURL = "http://www.gstatic.com/generate_204"
)

// diagDial is the production entry into internal/core; tests replace this
// package variable to run without a network.
var diagDial = core.DiagDial

// runDiag implements `diag dial`: a handful of small probe requests through
// the nodes of an account route or a single node, with per-phase timings.
// It runs without the daemon and never prints node addresses.
func runDiag(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("diag: missing subcommand (dial)")
	}
	if args[0] != "dial" {
		return cli.Usage("unknown diag command %q", args[0])
	}
	home, rest, err := parseHomeArgs("diag dial", args[1:])
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	account := fs.String("account", "", "diagnose the route nodes of this account login")
	node := fs.String("node", "", "diagnose this node label (<provider>/<node id>)")
	samples := fs.Int("samples", defaultDiagSamples, "samples per node (1..20)")
	idle := fs.String("idle", "", "comma-separated idle times for the survival test, or \"off\" to skip it")
	maxNodes := fs.Int("max-nodes", defaultDiagMaxNodes, "at most this many route nodes are diagnosed (1..50)")
	probeURL := fs.String("url", "", "probe URL (http only; default is the warm-up URL)")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("diag: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("diag: unexpected argument %q", pos[0])
	}
	if (*account == "") == (*node == "") {
		return cli.Usage("diag: exactly one of --account or --node is required")
	}
	if *samples < 1 || *samples > maxDiagSamples {
		return cli.Usage("diag: --samples must be between 1 and %d", maxDiagSamples)
	}
	if *maxNodes < 1 || *maxNodes > maxDiagMaxNodes {
		return cli.Usage("diag: --max-nodes must be between 1 and %d", maxDiagMaxNodes)
	}
	idles, err := parseDiagIdles(*idle)
	if err != nil {
		return err
	}
	target := diagProbeURL
	if *probeURL != "" {
		if !validDiagURL(*probeURL) {
			return cli.Usage("diag: --url must be an http:// URL with a host and a valid port")
		}
		target = *probeURL
	}

	var nodes []core.DiagNode
	if *node != "" {
		nd, err := findDiagNode(home, *node)
		if err != nil {
			return err
		}
		nodes = []core.DiagNode{nd}
	} else {
		nodes, err = accountDiagNodes(home, *account, *maxNodes)
		if err != nil {
			return err
		}
	}

	// No total deadline: every request carries its own sample timeout
	// inside DiagDial.
	results, err := diagDial(context.Background(), core.DiagOptions{
		Nodes:   nodes,
		URL:     target,
		Samples: *samples,
		Idle:    idles,
	})
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return ce
		}
		// DiagDial's messages are fixed texts (no mapping contents), safe to show.
		return &cli.Error{Code: "DIAG_FAILED", Message: err.Error(), Exit: cli.ExitNetwork}
	}

	host := ""
	if u, err := url.Parse(target); err == nil {
		host = u.Hostname()
	}
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		out = append(out, diagNodeOut(r))
	}
	cli.Success(stdout, map[string]any{
		"url_host": host,
		"samples":  *samples,
		"nodes":    out,
	})
	return nil
}

// validDiagURL accepts an http URL with a host and, if given, a port in
// 1..65535 (the checks internal/core makes, so a bad value is a usage error).
func validDiagURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return false
	}
	if ps := u.Port(); ps != "" {
		port, err := strconv.Atoi(ps)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

// parseDiagIdles reads the --idle value: "" means the DiagDial default,
// "off" skips the idle test, anything else is a comma-separated list of
// positive durations, sorted ascending, at most maxDiagIdles of them.
func parseDiagIdles(s string) ([]time.Duration, error) {
	if s == "" {
		return nil, nil
	}
	if s == "off" {
		return []time.Duration{}, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > maxDiagIdles {
		return nil, cli.Usage("diag: --idle takes at most %d values", maxDiagIdles)
	}
	idles := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		d, err := time.ParseDuration(p)
		if err != nil || d <= 0 {
			return nil, cli.Usage("diag: --idle must be positive durations like 500ms,2s, or \"off\"")
		}
		idles = append(idles, d)
	}
	sort.Slice(idles, func(i, j int) bool { return idles[i] < idles[j] })
	return idles, nil
}

// findDiagNode resolves a node label (<provider>/<node id>) against every
// configured subscription's stored snapshot. The error message carries the
// label only, never a server address.
func findDiagNode(home, label string) (core.DiagNode, error) {
	notFound := &cli.Error{Code: "NODE_NOT_FOUND", Message: "node not found: " + label, Exit: cli.ExitNotFound}
	provider, id, ok := strings.Cut(label, "/")
	if !ok || provider == "" || id == "" {
		return core.DiagNode{}, notFound
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return core.DiagNode{}, err
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	for _, sub := range cfg.Subscriptions {
		snap, err := store.Load(sub.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return core.DiagNode{}, snapshotError(sub.Name)
		}
		for _, n := range snap.Nodes {
			if nodeLabel(sub.Name, n.ID) == label {
				return core.DiagNode{Label: label, Mapping: n.Raw}, nil
			}
		}
	}
	return core.DiagNode{}, notFound
}

// accountDiagNodes resolves an account's route and takes the first
// maxNodes nodes of it, in the route order `up` uses.
func accountDiagNodes(home, login string, maxNodes int) ([]core.DiagNode, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	a, _ := cfg.Account(login)
	if a == nil {
		return nil, accountNotFound()
	}
	if a.Provider == "" && a.ExitCountry == "" {
		return nil, &cli.Error{
			Code:    "NO_ROUTE",
			Message: "account has no route (no provider and no exit country)",
			Exit:    cli.ExitNotFound,
		}
	}
	pool, err := accountRoutePool(home, cfg)
	if err != nil {
		return nil, err
	}
	key := pool.accountRoutes[login]
	raws := pool.nodes[key]
	labels := pool.labels[key]
	n := maxNodes
	if len(raws) < n {
		n = len(raws)
	}
	nodes := make([]core.DiagNode, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, core.DiagNode{Label: labels[i], Mapping: raws[i]})
	}
	return nodes, nil
}

// diagMS renders a duration in milliseconds with at most one decimal.
func diagMS(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Millisecond)*10) / 10
}

// diagPhase renders one phase's statistics.
func diagPhase(p core.PhaseStats) map[string]any {
	return map[string]any{
		"n":   p.N,
		"p50": diagMS(p.P50),
		"p90": diagMS(p.P90),
		"max": diagMS(p.Max),
	}
}

// diagNodeOut renders one node's diagnosis. Labels and the probe host are
// the only addresses in the output: no server, port, uuid or key.
func diagNodeOut(n core.NodeDiag) map[string]any {
	phases := map[string]any{}
	for name, p := range map[string]core.PhaseStats{
		"dns":        n.DNS,
		"tcp":        n.TCP,
		"tls":        n.TLS,
		"first_byte": n.FirstByte,
	} {
		if p.N > 0 {
			phases[name] = diagPhase(p)
		}
	}
	out := map[string]any{
		"label":            n.Label,
		"transport_phases": n.TransportPhases,
		"samples":          n.Samples,
		"phases_ms":        phases,
		// Always all four keys, 0 included.
		"failures": map[string]any{
			"dns":        n.Failures["dns"],
			"tcp":        n.Failures["tcp"],
			"tls":        n.Failures["tls"],
			"first_byte": n.Failures["first_byte"],
		},
	}
	if len(n.Idle) > 0 {
		idle := make([]map[string]any, 0, len(n.Idle))
		for _, r := range n.Idle {
			idle = append(idle, map[string]any{
				"idle_ms": diagMS(r.Idle),
				"ok":      r.OK,
				"fail":    r.Fail,
			})
		}
		out["idle_survival"] = idle
	}
	if n.SuggestedPoolTTL > 0 {
		out["suggested_pool_ttl_ms"] = diagMS(n.SuggestedPoolTTL)
	}
	return out
}
