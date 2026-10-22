package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/stats"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// routeKey builds the opaque route key of a (provider, country) pair; both
// parts are already normalised (upper-case country) and either may be empty.
func routeKey(provider, country string) string {
	return provider + "~" + country
}

// snapshotDemand tells which stored snapshots the routes read: all of them
// when some route has a country but no provider, else only the named ones.
func snapshotDemand(accounts []config.Account) (all bool, named map[string]bool) {
	named = make(map[string]bool)
	for _, a := range accounts {
		switch {
		case a.Provider != "":
			named[a.Provider] = true
		case a.ExitCountry != "":
			all = true
		}
	}
	return all, named
}

// accountRoutePool loads the stored snapshots the accounts' routes need and
// builds the per-account route pool for `up`. Snapshots no route reads are
// not touched, so a broken one cannot block unrelated accounts.
func accountRoutePool(home string, cfg *config.Config) (routePool, error) {
	return accountRoutePoolWith(home, cfg, nil)
}

// accountRoutePoolWith is accountRoutePool with some snapshots taken from
// override instead of the store: the way to ask whether a fetched snapshot
// would still leave every route with nodes before it is stored.
func accountRoutePoolWith(home string, cfg *config.Config, override map[string]*subscription.Snapshot) (routePool, error) {
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	all, named := snapshotDemand(cfg.Accounts)
	snapshots := make(map[string]*subscription.Snapshot, len(cfg.Subscriptions))
	for _, sub := range cfg.Subscriptions {
		if !all && !named[sub.Name] {
			continue
		}
		if snap, ok := override[sub.Name]; ok {
			snapshots[sub.Name] = snap
			continue
		}
		snap, err := store.Load(sub.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return routePool{}, snapshotError(sub.Name)
		}
		snapshots[sub.Name] = snap
	}
	return buildAccountRoutePool(cfg.Accounts, snapshots)
}

// routePool is the outcome of buildAccountRoutePool: the nodes of every
// route, a stable label per node (same order) and each account's route key.
// infos describes every distinct node for the statistics store.
type routePool struct {
	nodes         map[string][]map[string]any
	labels        map[string][]string
	infos         []stats.NodeInfo
	accountRoutes map[string]string
}

// nodeLabel is the stable identity of a node in statistics and bindings.
func nodeLabel(provider, id string) string {
	return provider + "/" + id
}

// buildAccountRoutePool is a pure function over the stored snapshots: for
// every account with a provider and/or an exit country it collects the
// route's nodes per docs/design/accounts.md, ordered by ascending node id
// (ties broken by provider name). An account with neither has no route.
// A route without nodes is a hard error: `up` fails closed.
func buildAccountRoutePool(accounts []config.Account, snapshots map[string]*subscription.Snapshot) (routePool, error) {
	pool := routePool{
		nodes:         map[string][]map[string]any{},
		labels:        map[string][]string{},
		accountRoutes: map[string]string{},
	}
	infoSeen := make(map[string]bool)
	type demand struct {
		provider string
		country  string
	}
	seen := make(map[demand]bool)
	var wanted []demand // in account order, so the first failure is stable
	for _, a := range accounts {
		if a.Provider == "" && a.ExitCountry == "" {
			continue
		}
		pool.accountRoutes[a.Login] = routeKey(a.Provider, a.ExitCountry)
		d := demand{a.Provider, a.ExitCountry}
		if !seen[d] {
			seen[d] = true
			wanted = append(wanted, d)
		}
	}
	if len(wanted) == 0 {
		return pool, nil
	}
	// Deterministic provider order: sorted provider names.
	allProviders := make([]string, 0, len(snapshots))
	for name := range snapshots {
		allProviders = append(allProviders, name)
	}
	sort.Strings(allProviders)
	for _, d := range wanted {
		names := allProviders
		if d.provider != "" {
			names = []string{d.provider}
		}
		type poolNode struct {
			provider string
			id       string
			country  string
			raw      map[string]any
		}
		var nodes []poolNode
		for _, name := range names {
			snap := snapshots[name]
			if snap == nil {
				continue
			}
			for _, n := range snap.Nodes {
				if d.country != "" && n.Country != d.country {
					continue
				}
				nodes = append(nodes, poolNode{provider: name, id: n.ID, country: n.Country, raw: n.Raw})
			}
		}
		if len(nodes) == 0 {
			return routePool{}, noNodesError(d.provider, d.country)
		}
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].id != nodes[j].id {
				return nodes[i].id < nodes[j].id
			}
			return nodes[i].provider < nodes[j].provider
		})
		key := routeKey(d.provider, d.country)
		for _, n := range nodes {
			label := nodeLabel(n.provider, n.id)
			pool.nodes[key] = append(pool.nodes[key], n.raw)
			pool.labels[key] = append(pool.labels[key], label)
			if !infoSeen[label] {
				infoSeen[label] = true
				pool.infos = append(pool.infos, stats.NodeInfo{Label: label, Provider: n.provider, Country: n.country})
			}
		}
	}
	return pool, nil
}

// noNodesError reports a route without nodes. The message names the
// country code and/or the provider name, never a URL.
func noNodesError(provider, country string) error {
	var msg string
	switch {
	case provider != "" && country != "":
		msg = fmt.Sprintf("no nodes available for country %s of provider %q", country, provider)
	case provider != "":
		msg = fmt.Sprintf("no nodes available for provider %q", provider)
	default:
		msg = fmt.Sprintf("no nodes available for country %s", country)
	}
	return &cli.Error{
		Code:    "NO_NODES_FOR_COUNTRY",
		Message: msg,
		Hint:    "run netivri sub update before netivri up",
		Exit:    cli.ExitNotFound,
	}
}

// warmOptions turns background probing on, with the default interval and no
// pool, whenever there are route nodes to keep warm.
func warmOptions(routes map[string][]map[string]any) *core.WarmOptions {
	if len(routes) == 0 {
		return nil
	}
	return &core.WarmOptions{}
}

// warmInfo converts the warm-up counters of the instance for the status
// answer; nil when the instance runs without warm-up.
func warmInfo(inst coreInstance) *daemon.WarmInfo {
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

// accountStatus lists the configured accounts for the status answer,
// sorted by login. The country comes from the live route key (the part
// after "~"); a routed account always carries the route it was built for.
func accountStatus(cfg *config.Config, routes map[string]string) []daemon.AccountInfo {
	out := make([]daemon.AccountInfo, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		country := ""
		if key, routed := routes[a.Login]; routed {
			if i := strings.Index(key, "~"); i >= 0 {
				country = key[i+1:]
			}
		}
		out = append(out, daemon.AccountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: country})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}
