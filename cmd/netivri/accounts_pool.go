package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// routeLists returns the canonical provider and country lists of an account:
// blanks dropped, codes upper-cased, a repeated item kept once (the first).
func routeLists(a config.Account) (providers, countries []string) {
	providers, _ = config.ParseProviderList(a.Provider)
	countries, _ = config.ParseCountryList(a.ExitCountry)
	return providers, countries
}

// routeKey builds the opaque route key of a (providers, countries) pair, each
// a canonical comma-separated list that may be empty.
func routeKey(providers, countries string) string {
	return providers + "~" + countries
}

// credentialRouteKey keeps credentials out of core route keys and Mihomo rule names.
func credentialRouteKey(providers, countries, pre, post string) string {
	payload := providers + "\x00" + countries + "\x00" + pre + "\x00" + post
	sum := sha256.Sum256([]byte(payload))
	return routeKey(providers, countries) + "~" + hex.EncodeToString(sum[:])
}

// snapshotDemand tells which stored snapshots the account routes read: all of them
// when a non-direct route has countries but no provider, else only named ones.
func snapshotDemand(accounts []config.Account) (all bool, named map[string]bool) {
	named = make(map[string]bool)
	for _, a := range accounts {
		providers, countries := routeLists(a)
		if len(providers) == 0 && len(countries) == 0 {
			continue
		}
		if len(providers) > 0 {
			for _, p := range providers {
				named[p] = true
			}
		} else if len(countries) > 0 {
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
	return buildAccountRoutePool(cfg.Accounts, snapshots, cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5)
}

// routePool is the outcome of buildAccountRoutePool: the nodes of every
// route, a stable label per node and a priority tier per node (same order), and
// each account's route key. infos describes every distinct node for the
// statistics store.
type routePool struct {
	nodes         map[string][]map[string]any
	labels        map[string][]string
	tiers         map[string][]int
	infos         []stats.NodeInfo
	accountRoutes map[string]string
	routePre      map[string]string
	routePost     map[string]string
}

// nodeLabel is the stable identity of a node in statistics and bindings.
func nodeLabel(provider, id string) string {
	return provider + "/" + id
}

// buildAccountRoutePool is a pure function over the stored snapshots: for
// every account with providers and/or exit countries it collects the route's
// nodes per docs/design/accounts.md. A route is a list of tiers, tried in
// order: the countries in the order listed, and within a country the
// providers in the order listed; a tier holds the nodes of one (country,
// provider) pair by ascending node id. Without a provider list every
// provider shares one tier per country (ascending node id, ties by provider
// name); without a country list the tiers are the providers alone. An account
// with neither has no route. A route without any node is a hard error: `up`
// fails closed.
func inheritChain(account, global string) string {
	if strings.EqualFold(account, "none") {
		return "none"
	}
	if account != "" {
		return account
	}
	return global
}

// firstTierLabels returns the labels of the nodes of the first tier of any
// route: the nodes an account uses while it is healthy, so the ones whose
// trouble is worth a warning.
func (p routePool) firstTierLabels() map[string]bool {
	first := map[string]bool{}
	for key, tiers := range p.tiers {
		if len(tiers) == 0 {
			continue
		}
		lowest := tiers[0]
		for _, t := range tiers {
			lowest = min(lowest, t)
		}
		for i, t := range tiers {
			if t == lowest && i < len(p.labels[key]) {
				first[p.labels[key][i]] = true
			}
		}
	}
	return first
}

func buildAccountRoutePool(accounts []config.Account, snapshots map[string]*subscription.Snapshot, globalPre, globalPost string) (routePool, error) {
	pool := routePool{
		nodes:         map[string][]map[string]any{},
		labels:        map[string][]string{},
		tiers:         map[string][]int{},
		accountRoutes: map[string]string{},
		routePre:      map[string]string{},
		routePost:     map[string]string{},
	}
	infoSeen := make(map[string]bool)
	type demand struct {
		providers []string
		countries []string
		key       string
		pre       string
		post      string
		direct    bool
	}
	seen := make(map[string]bool)
	var wanted []demand // in account order, so the first failure is stable
	for _, a := range accounts {
		providers, countries := routeLists(a)
		pre, post := inheritChain(a.PreSOCKS5, globalPre), inheritChain(a.PostSOCKS5, globalPost)
		direct := len(providers) == 0 && len(countries) == 0
		key := credentialRouteKey(config.JoinList(providers), config.JoinList(countries), pre, post)
		if direct {
			// Preserve the implicit plain-direct default. An explicit post
			// chain, including an account-level "none" overriding a global post,
			// needs a route to bypass MATCH.
			if post == "" || (strings.EqualFold(post, "none") && !(strings.EqualFold(a.PostSOCKS5, "none") && globalPost != "" && !strings.EqualFold(globalPost, "none"))) {
				continue
			}
			pre = "none"
			key = credentialRouteKey("", "", pre, post)
		}
		pool.accountRoutes[a.Login] = key
		if !seen[key] {
			seen[key] = true
			wanted = append(wanted, demand{providers: providers, countries: countries, key: key, pre: pre, post: post, direct: direct})
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
		pool.routePre[d.key], pool.routePost[d.key] = d.pre, d.post
		if d.direct {
			continue
		}
		// Each group is the set of providers that share a tier.
		var groups [][]string
		if len(d.providers) > 0 {
			for _, name := range d.providers {
				groups = append(groups, []string{name})
			}
		} else {
			groups = [][]string{allProviders}
		}
		countries := d.countries
		if len(countries) == 0 {
			countries = []string{config.AnyCountry}
		}
		// "*" takes the nodes of every country the list does not name.
		named := make(map[string]bool, len(countries))
		for _, c := range countries {
			named[c] = true
		}
		type poolNode struct {
			provider string
			id       string
			country  string
			name     string
			raw      map[string]any
		}
		key := d.key
		tier := 0
		for _, country := range countries {
			for _, names := range groups {
				var nodes []poolNode
				for _, name := range names {
					snap := snapshots[name]
					if snap == nil {
						continue
					}
					for _, n := range snap.Nodes {
						if country == config.AnyCountry {
							if named[n.Country] {
								continue
							}
						} else if n.Country != country {
							continue
						}
						nodes = append(nodes, poolNode{provider: name, id: n.ID, country: n.Country, name: stats.SanitizeName(n.Name), raw: n.Raw})
					}
				}
				if len(nodes) == 0 {
					continue
				}
				sort.Slice(nodes, func(i, j int) bool {
					if nodes[i].id != nodes[j].id {
						return nodes[i].id < nodes[j].id
					}
					return nodes[i].provider < nodes[j].provider
				})
				for _, n := range nodes {
					label := nodeLabel(n.provider, n.id)
					pool.nodes[key] = append(pool.nodes[key], n.raw)
					pool.labels[key] = append(pool.labels[key], label)
					pool.tiers[key] = append(pool.tiers[key], tier)
					if !infoSeen[label] {
						infoSeen[label] = true
						pool.infos = append(pool.infos, stats.NodeInfo{Label: label, Provider: n.provider, Country: n.country, Name: n.name})
					}
				}
				tier++
			}
		}
		if len(pool.nodes[key]) == 0 {
			return routePool{}, noNodesError(d.providers, d.countries)
		}
	}
	return pool, nil
}

// noNodesError reports a route without nodes. The message names the
// country codes and/or the provider names, never a URL.
func noNodesError(providers, countries []string) error {
	var msg string
	switch {
	case len(providers) > 0 && len(countries) > 0:
		msg = "no nodes available for " + countryPhrase(countries) + " of " + providerPhrase(providers)
	case len(providers) > 0:
		msg = "no nodes available for " + providerPhrase(providers)
	default:
		msg = "no nodes available for " + countryPhrase(countries)
	}
	return &cli.Error{
		Code:    "NO_NODES_FOR_COUNTRY",
		Message: msg,
		Hint:    "run netivri sub update before netivri up",
		Exit:    cli.ExitNotFound,
	}
}

// countryPhrase reads "country DE" or "countries DE,NL".
func countryPhrase(codes []string) string {
	if len(codes) == 0 {
		return "countries"
	}
	if len(codes) > 1 {
		return "countries " + strings.Join(codes, ",")
	}
	return "country " + codes[0]
}

// providerPhrase reads `provider "main"` or `providers "main,backup"`.
func providerPhrase(names []string) string {
	if len(names) > 1 {
		return fmt.Sprintf("providers %q", strings.Join(names, ","))
	}
	return fmt.Sprintf("provider %q", names[0])
}

// warmOptions turns background probing on, with the default interval and no
// pool, whenever there are route nodes to keep warm.
func warmOptions(routes map[string][]map[string]any) *core.WarmOptions {
	if len(routes) == 0 {
		return nil
	}
	return &core.WarmOptions{ConfirmProbe: true}
}

// warmInfo converts the warm-up counters of the instance for the status
// answer; nil when the instance runs without warm-up.
func warmInfo(inst coreInstance, infos []stats.NodeInfo) *angel.WarmInfo {
	ws, ok := inst.WarmStats()
	if !ok {
		return nil
	}
	return warmInfoOf(ws, inst.MemberLabels(), infos)
}

func warmInfoOf(ws core.WarmStats, labels map[string]string, infos []stats.NodeInfo) *angel.WarmInfo {
	info := &angel.WarmInfo{
		Probes:        ws.Probes,
		ProbeFailures: ws.ProbeFailures,
		PoolHits:      ws.PoolHits,
		PoolMisses:    ws.PoolMisses,
		PoolDiscards:  ws.PoolDiscards,
		Nodes:         make([]angel.NodeHealth, 0, len(ws.Nodes)),
	}
	byLabel := make(map[string]stats.NodeInfo, len(infos))
	for _, n := range infos {
		byLabel[n.Label] = n
	}
	for _, n := range ws.Nodes {
		node := angel.NodeHealth{
			Name:    n.Name,
			Alive:   n.Alive,
			DelayMS: n.Delay.Milliseconds(),
			Label:   labels[n.Name],
		}
		if ni, ok := byLabel[node.Label]; ok {
			node.Provider = ni.Provider
			node.Country = ni.Country
			node.NodeName = ni.Name
		}
		if n.Alive {
			info.Alive++
		}
		info.Nodes = append(info.Nodes, node)
	}
	info.Total = len(ws.Nodes)
	return info
}

type resolvedChain struct {
	value, source string
	auth          bool
}

func resolveChain(account, global string) resolvedChain {
	source, effective := "account", account
	if effective == "" {
		source, effective = "global", global
	}
	if effective == "" || strings.EqualFold(effective, "none") {
		return resolvedChain{value: "none", source: "none"}
	}
	u, err := url.Parse(effective)
	if err != nil {
		return resolvedChain{value: "configured", source: source}
	}
	auth := u.User != nil
	u.User = nil
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return resolvedChain{value: u.String(), source: source, auth: auth}
}

func statusAccountChain(accountPre, accountPost, globalPre, globalPost string) *angel.AccountChain {
	pre, post := resolveChain(accountPre, globalPre), resolveChain(accountPost, globalPost)
	return &angel.AccountChain{PreSOCKS5: pre.value, PreSOCKS5Source: pre.source, PreSOCKS5Auth: pre.auth,
		PostSOCKS5: post.value, PostSOCKS5Source: post.source, PostSOCKS5Auth: post.auth}
}

// accountStatus lists the configured accounts for the status answer,
// sorted by login. The country comes from the live route key's second field;
// a credential hash, when present, is a separate third field.
func accountStatus(cfg *config.Config, routes map[string]string) []angel.AccountInfo {
	out := make([]angel.AccountInfo, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		country := ""
		if key, routed := routes[a.Login]; routed {
			fields := strings.SplitN(key, "~", 3)
			if len(fields) > 1 {
				country = fields[1]
			}
		}
		// No normalization: an account without its own rule reports "" here, like
		// in `account list`.
		out = append(out, angel.AccountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: country, AllowedClients: a.AllowedClients,
			Chain: statusAccountChain(a.PreSOCKS5, a.PostSOCKS5, cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}
