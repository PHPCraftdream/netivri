package main

import (
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

const (
	defaultStatsWindow = 24 * time.Hour
	maxStatsWindow     = 365 * 24 * time.Hour
	unknownCountry     = "unknown"
)

// runStats implements `stats`: the recorded health, delay and throughput of
// the exit nodes. It reads the database read-only, so it works while the
// daemon runs and after it stopped.
func runStats(args []string, stdout io.Writer) error {
	home, rest, err := parseHomeArgs("stats", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sub := fs.String("sub", "", "only the nodes of this subscription")
	country := fs.String("country", "", "only the nodes of this country")
	node := fs.String("node", "", "only this node label")
	since := fs.String("since", "24h", "window, e.g. 90m, 24h or 7d")
	by := fs.String("by", "node", "group by node, country, provider or account")
	account := fs.String("account", "", "only this account (all --by modes)")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("stats: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("stats: unexpected argument %q", pos[0])
	}
	window, err := parseWindow(*since)
	if err != nil {
		return err
	}
	switch *by {
	case "node", "country", "provider", "account":
	default:
		return cli.Usage("stats: --by must be node, country, provider or account")
	}
	cc := ""
	if *country != "" {
		var ok bool
		cc, ok = config.NormalizeCountry(*country)
		if !ok {
			return cli.Usage("stats: --country must be two ASCII letters")
		}
	}

	store, err := stats.OpenReadOnly(statsPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return &cli.Error{
			Code:    "STATS_UNAVAILABLE",
			Message: "no node statistics have been recorded yet",
			Hint:    "they are recorded while netivri up serves accounts bound to a provider or country",
			Exit:    cli.ExitNotFound,
		}
	}
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "open statistics: " + err.Error(), Exit: cli.ExitInternal}
	}
	defer func() { _ = store.Close() }()

	var items []map[string]any
	q := stats.Query{Since: window, Account: *account, Provider: *sub, Country: cc, Label: *node}
	switch {
	case *by == "account":
		accounts, err := store.Accounts(q)
		if err != nil {
			return &cli.Error{Code: "INTERNAL", Message: "read statistics: " + err.Error(), Exit: cli.ExitInternal}
		}
		items = accountItems(accounts)
	case *account != "":
		// The rows come from the account samples of that login only: a
		// documented subset (dials and bytes, no probes or rates).
		nodes, err := store.AccountNodes(q)
		if err != nil {
			return &cli.Error{Code: "INTERNAL", Message: "read statistics: " + err.Error(), Exit: cli.ExitInternal}
		}
		items = statsItems(nodes, *by)
	default:
		nodes, err := store.Nodes(stats.Query{Since: window, Provider: *sub, Country: cc, Label: *node})
		if err != nil {
			return &cli.Error{Code: "INTERNAL", Message: "read statistics: " + err.Error(), Exit: cli.ExitInternal}
		}
		items = statsItems(nodes, *by)
	}
	cli.Success(stdout, map[string]any{
		"since": window.String(),
		"by":    *by,
		"items": items,
	})
	return nil
}

// accountItems renders the per-account rows: one per login, sorted by
// login (the store already does that).
func accountItems(accounts []stats.AccountStats) []map[string]any {
	items := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, map[string]any{
			"login":       a.Login,
			"bytes_up":    a.BytesUp,
			"bytes_down":  a.BytesDown,
			"dials":       a.Dials,
			"dial_ok_pct": percent(a.DialsOK, a.Dials),
			"nodes":       a.Nodes,
			"countries":   strings.Join(a.Countries, ", "),
			"providers":   strings.Join(a.Providers, ", "),
		})
	}
	return items
}

// parseWindow reads a duration with an extra "d" (days) suffix, bounded to
// 1 second .. 365 days.
func parseWindow(s string) (time.Duration, error) { return parseWindowFor("stats", s) }

// parseWindowFor is parseWindow with the command named in the usage error.
func parseWindowFor(command, s string) (time.Duration, error) {
	bad := cli.Usage("%s: --since must be a duration like 90m, 24h or 7d, at most 365d", command)
	if s == "" {
		return 0, bad
	}
	var d time.Duration
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || math.IsNaN(days) || math.IsInf(days, 0) {
			return 0, bad
		}
		d = time.Duration(days * float64(24*time.Hour))
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, bad
		}
	}
	if d < time.Second || d > maxStatsWindow {
		return 0, bad
	}
	return d, nil
}

// statsItems renders the per-node rows, or groups them by country or
// provider. Grouped rows sum the counters and weight the averages by the
// number of successful samples; percentiles do not merge and are left out.
func statsItems(nodes []stats.NodeStats, by string) []map[string]any {
	if by == "node" {
		items := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
			items = append(items, nodeItem(n))
		}
		return items
	}
	type group struct {
		key                  string
		nodes                int
		probes, probesOK     int
		delaySum             int64
		dials, dialsOK       int
		dialSum              int64
		throughputMax, bytes int64
		lastSeen, lastOK     time.Time
	}
	groups := map[string]*group{}
	for _, n := range nodes {
		key := n.Provider
		if by == "country" {
			key = n.Country
			if key == "" {
				key = unknownCountry
			}
		}
		g := groups[key]
		if g == nil {
			g = &group{key: key}
			groups[key] = g
		}
		g.nodes++
		g.probes += n.Probes
		g.probesOK += n.ProbesOK
		g.delaySum += n.DelayAvgMS * int64(n.ProbesOK)
		g.dials += n.Dials
		g.dialsOK += n.DialsOK
		g.dialSum += n.DialAvgMS * int64(n.DialsOK)
		g.throughputMax = max(g.throughputMax, n.ThroughputMaxKBps)
		g.bytes += n.Bytes
		if n.LastSeen.After(g.lastSeen) {
			g.lastSeen = n.LastSeen
		}
		if n.LastOK.After(g.lastOK) {
			g.lastOK = n.LastOK
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		g := groups[key]
		items = append(items, map[string]any{
			by:                g.key,
			"nodes":           g.nodes,
			"probes":          g.probes,
			"uptime_pct":      percent(g.probesOK, g.probes),
			"delay_ms":        map[string]any{"avg": average(g.delaySum, g.probesOK)},
			"dials":           g.dials,
			"dial_ok_pct":     percent(g.dialsOK, g.dials),
			"dial_ms_avg":     average(g.dialSum, g.dialsOK),
			"throughput_kbps": map[string]any{"max": g.throughputMax},
			"bytes":           g.bytes,
			"last_seen":       timeOrNil(g.lastSeen),
			"last_ok":         timeOrNil(g.lastOK),
		})
	}
	return items
}

// nodeItem renders one node's statistics.
func nodeItem(n stats.NodeStats) map[string]any {
	item := map[string]any{
		"label":      n.Label,
		"provider":   n.Provider,
		"country":    n.Country,
		"probes":     n.Probes,
		"uptime_pct": percent(n.ProbesOK, n.Probes),
		"delay_ms": map[string]any{
			"avg": n.DelayAvgMS,
			"p50": n.DelayP50MS,
			"p95": n.DelayP95MS,
		},
		"dials":       n.Dials,
		"dial_ok_pct": percent(n.DialsOK, n.Dials),
		"dial_ms_avg": n.DialAvgMS,
		"throughput_kbps": map[string]any{
			"p50": n.ThroughputP50KBps,
			"max": n.ThroughputMaxKBps,
		},
		"bytes":     n.Bytes,
		"last_seen": timeOrNil(n.LastSeen),
		"last_ok":   timeOrNil(n.LastOK),
	}
	if n.Name != "" {
		item["name"] = n.Name
	}
	return item
}

// percent is part/total in percent with one decimal; nil when total is 0.
func percent(part, total int) any {
	if total == 0 {
		return nil
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}

func average(sum int64, count int) int64 {
	if count == 0 {
		return 0
	}
	return sum / int64(count)
}

// timeOrNil renders an RFC 3339 UTC time; nil for the zero time.
func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
