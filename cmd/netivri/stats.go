package main

import (
	"flag"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const (
	defaultStatsWindow = 24 * time.Hour
	maxStatsWindow     = 365 * 24 * time.Hour
)

// runStats implements `stats`: the recorded health, delay and throughput of
// the exit nodes. The computation lives in StatsRead (ops_stats.go), shared
// with the Stats tab of the interactive shell. It reads the database
// read-only, so it works while the angel runs and after it stopped.
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
	res, err := StatsRead(home, StatsOptions{
		Window:  window,
		By:      *by,
		Sub:     *sub,
		Country: *country,
		Node:    *node,
		Account: *account,
	})
	if err != nil {
		return err
	}
	cli.Success(stdout, map[string]any{
		"since": res.Window.String(),
		"by":    res.By,
		"items": res.Items,
	})
	return nil
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
