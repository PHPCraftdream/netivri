package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func accountCountryPool(home string, cfg *config.Config) (map[string][]map[string]any, map[string]string, error) {
	wanted := map[string]bool{}
	routes := map[string]string{}
	for _, a := range cfg.Accounts {
		if a.ExitCountry != "" {
			routes[a.Login] = a.ExitCountry
			wanted[a.ExitCountry] = true
		}
	}
	if len(wanted) == 0 {
		return map[string][]map[string]any{}, routes, nil
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	snapshots := make([]*subscription.Snapshot, 0, len(cfg.Subscriptions))
	for _, sub := range cfg.Subscriptions {
		snap, err := store.Load(sub.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, snapshotError(sub.Name)
		}
		snapshots = append(snapshots, snap)
	}
	countries, err := buildAccountCountryPool(cfg.Accounts, snapshots)
	if err != nil {
		return nil, nil, err
	}
	return countries, routes, nil
}

func buildAccountCountryPool(accounts []config.Account, snapshots []*subscription.Snapshot) (map[string][]map[string]any, error) {
	wanted := map[string]bool{}
	for _, a := range accounts {
		if a.ExitCountry != "" {
			wanted[a.ExitCountry] = true
		}
	}
	countries := map[string][]map[string]any{}
	if len(wanted) == 0 {
		return countries, nil
	}
	type poolNode struct {
		id  string
		raw map[string]any
	}
	ordered := make(map[string][]poolNode, len(wanted))
	codes := make([]string, 0, len(wanted))
	for c := range wanted {
		codes = append(codes, c)
		ordered[c] = nil
	}
	sort.Strings(codes)
	for _, snap := range snapshots {
		if snap == nil {
			continue
		}
		for _, n := range snap.Nodes {
			if wanted[n.Country] {
				ordered[n.Country] = append(ordered[n.Country], poolNode{id: n.ID, raw: n.Raw})
			}
		}
	}
	for _, c := range codes {
		nodes := ordered[c]
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].id < nodes[j].id })
		if len(nodes) == 0 {
			return nil, &cli.Error{Code: "NO_NODES_FOR_COUNTRY", Message: fmt.Sprintf("no nodes available for country %s", c), Hint: "run netivri sub update before netivri up", Exit: cli.ExitNotFound}
		}
		for _, n := range nodes {
			countries[c] = append(countries[c], n.raw)
		}
	}
	return countries, nil
}

// warmOptions turns background probing on, with the default interval and no
// pool, whenever there are country nodes to keep warm.
func warmOptions(countries map[string][]map[string]any) *core.WarmOptions {
	if len(countries) == 0 {
		return nil
	}
	return &core.WarmOptions{}
}

// warmInfo converts the warm-up counters of the instance for the status
// answer; nil when the instance runs without warm-up.
func warmInfo(inst *core.Instance) *daemon.WarmInfo {
	stats, ok := inst.WarmStats()
	if !ok {
		return nil
	}
	return warmInfoOf(stats)
}

func warmInfoOf(stats core.WarmStats) *daemon.WarmInfo {
	info := &daemon.WarmInfo{
		Probes:        stats.Probes,
		ProbeFailures: stats.ProbeFailures,
		PoolHits:      stats.PoolHits,
		PoolMisses:    stats.PoolMisses,
		PoolDiscards:  stats.PoolDiscards,
		Nodes:         make([]daemon.NodeHealth, 0, len(stats.Nodes)),
	}
	for _, n := range stats.Nodes {
		info.Nodes = append(info.Nodes, daemon.NodeHealth{
			Name:    n.Name,
			Alive:   n.Alive,
			DelayMS: n.Delay.Milliseconds(),
		})
	}
	return info
}

func accountStatus(cfg *config.Config, routes map[string]string) []daemon.AccountInfo {
	out := make([]daemon.AccountInfo, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		country, routed := routes[a.Login]
		if !routed {
			country = ""
		}
		out = append(out, daemon.AccountInfo{Login: a.Login, ExitCountry: country})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}
