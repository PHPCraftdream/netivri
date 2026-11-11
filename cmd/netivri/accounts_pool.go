package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
func credentialRouteKey(providers, countries, pre, post string, extra ...string) string {
	payload := providers + "\x00" + countries + "\x00" + pre + "\x00" + post
	for _, value := range extra {
		payload += "\x00" + value
	}
	sum := sha256.Sum256([]byte(payload))
	return routeKey(providers, countries) + "~" + hex.EncodeToString(sum[:])
}

func credentialRouteKeyHops(providers, countries string, pre, post config.HopList, extra ...string) string {
	if len(pre) <= 1 && len(post) <= 1 {
		var scalarPre, scalarPost string
		if len(pre) == 1 {
			scalarPre = pre[0]
		}
		if len(post) == 1 {
			scalarPost = post[0]
		}
		return credentialRouteKey(providers, countries, scalarPre, scalarPost, extra...)
	}
	payload := appendRouteIdentity(nil, providers)
	payload = appendRouteIdentity(payload, countries)
	payload = appendRouteHopIdentity(payload, pre)
	payload = appendRouteHopIdentity(payload, post)
	for _, value := range extra {
		payload = appendRouteIdentity(payload, value)
	}
	sum := sha256.Sum256(payload)
	return routeKey(providers, countries) + "~" + hex.EncodeToString(sum[:])
}

func routeIdentityKey(login, providers, countries string, pre, post config.HopList, multiPool bool, extra ...string) string {
	if !multiPool {
		return credentialRouteKeyHops(providers, countries, pre, post, extra...)
	}
	// Pools can be edited or reordered without changing the logical route that
	// core uses to find predecessor pool health and bindings. Include which
	// providers supply inherited pools, but not their mutable pool contents.
	payload := providers + "\x00" + countries + "\x00pool\x00" + login
	for _, tag := range extra {
		payload += "\x00" + tag
	}
	sum := sha256.Sum256([]byte(payload))
	return routeKey(providers, countries) + "~" + hex.EncodeToString(sum[:])
}

func appendRouteIdentity(dst []byte, value string) []byte {
	var size [8]byte
	for i := 0; i < len(size); i++ {
		size[len(size)-1-i] = byte(uint64(len(value)) >> (8 * i))
	}
	dst = append(dst, size[:]...)
	return append(dst, value...)
}

func appendRouteHopIdentity(dst []byte, hops config.HopList) []byte {
	var size [8]byte
	for i := 0; i < len(size); i++ {
		size[len(size)-1-i] = byte(uint64(len(hops)) >> (8 * i))
	}
	dst = append(dst, size[:]...)
	for _, hop := range hops {
		dst = appendRouteIdentity(dst, hop)
	}
	return dst
}

func tagArgs(tag string) []string {
	if tag == "" {
		return nil
	}
	return []string{tag}
}

// snapshotDemand tells which stored snapshots the account routes read: all of them
// when a non-direct route has countries but no provider, else only named ones.
func snapshotDemand(accounts []config.Account) (all bool, named map[string]bool) {
	named = make(map[string]bool)
	for _, a := range accounts {
		if a.Exit == "direct" {
			continue
		}
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
	return buildAccountRoutePoolHops(cfg.Accounts, snapshots, cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5, hopsOf(cfg.Subscriptions))
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
	routePre      map[string]config.HopList
	routePost     map[string]config.HopList
	// nodePre and nodePost hold, for the routes whose nodes take provider
	// hops, one effective hop list per node (same order as nodes).
	nodePre  map[string][]config.HopList
	nodePost map[string][]config.HopList
}

// providerHops are the provider-level hops by provider name: the middle level
// of the cascade global -> provider -> account.
type providerHopValidation struct {
	preField, postField string
	pre, post           config.HopList
}

type providerHops struct {
	pre, post  map[string]config.HopList
	validation []providerHopValidation
}

func hopsOf(subs []config.Subscription) providerHops {
	h := providerHops{pre: map[string]config.HopList{}, post: map[string]config.HopList{}}
	for i, s := range subs {
		if len(s.PreSOCKS5) > 0 {
			h.pre[s.Name] = s.PreSOCKS5
		}
		if len(s.PostSOCKS5) > 0 {
			h.post[s.Name] = s.PostSOCKS5
		}
		h.validation = append(h.validation, providerHopValidation{
			preField:  fmt.Sprintf("subscriptions[%d].pre_socks5", i),
			postField: fmt.Sprintf("subscriptions[%d].post_socks5", i),
			pre:       s.PreSOCKS5,
			post:      s.PostSOCKS5,
		})
	}
	return h
}

// tag fingerprints provider hop lists that reach nodes of a route whose
// account leaves a side to inherit; "" when none do.
func (h providerHops) tag(providers []string, rawPre, rawPost config.HopList) string {
	type providerHopIdentity struct {
		Name string
		Pre  config.HopList
		Post config.HopList
	}
	var parts []providerHopIdentity
	var singletonParts []string
	allSingleton := true
	for _, name := range providers {
		pre, post := config.HopList(nil), config.HopList(nil)
		if len(rawPre) == 0 {
			pre = h.pre[name]
		}
		if len(rawPost) == 0 {
			post = h.post[name]
		}
		if len(pre) > 0 || len(post) > 0 {
			parts = append(parts, providerHopIdentity{Name: name, Pre: pre, Post: post})
			if len(pre) > 1 || len(post) > 1 {
				allSingleton = false
			}
		}
		if len(pre) <= 1 && len(post) <= 1 && (len(pre) > 0 || len(post) > 0) {
			singletonParts = append(singletonParts, name+"\x01"+singleHopValue(pre)+"\x01"+singleHopValue(post))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if allSingleton {
		return strings.Join(singletonParts, "\x02")
	}
	data, _ := json.Marshal(parts)
	return string(data)
}

func (h providerHops) identityTag(providers []string, rawPre, rawPost config.HopList) string {
	var names []string
	for _, name := range providers {
		if len(rawPre) == 0 && len(h.pre[name]) > 0 || len(rawPost) == 0 && len(h.post[name]) > 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, "\x02")
}
func (h providerHops) hasMulti(providers []string, rawPre, rawPost config.HopList) bool {
	for _, name := range providers {
		if len(rawPre) == 0 && len(h.pre[name]) > 1 || len(rawPost) == 0 && len(h.post[name]) > 1 {
			return true
		}
	}
	return false
}
func singleHopValue(hops config.HopList) string {
	if len(hops) == 0 {
		return ""
	}
	return hops[0]
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
func inheritChain(account, global config.HopList) config.HopList {
	return config.InheritHops(account, global)
}

func isNoneHops(hops config.HopList) bool {
	return len(hops) == 1 && strings.EqualFold(hops[0], "none")
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

func buildAccountRoutePool(accounts []config.Account, snapshots map[string]*subscription.Snapshot, globalPre, globalPost config.HopList) (routePool, error) {
	return buildAccountRoutePoolHops(accounts, snapshots, globalPre, globalPost, providerHops{})
}

func buildAccountRoutePoolHops(accounts []config.Account, snapshots map[string]*subscription.Snapshot, globalPre, globalPost config.HopList, hops providerHops) (routePool, error) {
	return buildAccountRoutePoolHopsWithValidation(accounts, snapshots, globalPre, globalPost, hops, config.ValidateHopList)
}

func buildAccountRoutePoolHopsWithValidation(accounts []config.Account, snapshots map[string]*subscription.Snapshot, globalPre, globalPost config.HopList, hops providerHops, validate func(field string, hops config.HopList) error) (routePool, error) {
	for _, item := range []struct {
		field string
		hops  config.HopList
	}{{"chain.pre_socks5", globalPre}, {"chain.post_socks5", globalPost}} {
		if err := validate(item.field, item.hops); err != nil {
			return routePool{}, err
		}
	}
	for _, provider := range hops.validation {
		for _, item := range []struct {
			field string
			hops  config.HopList
		}{{provider.preField, provider.pre}, {provider.postField, provider.post}} {
			if err := validate(item.field, item.hops); err != nil {
				return routePool{}, err
			}
		}
	}
	for i, account := range accounts {
		for _, item := range []struct {
			field string
			hops  config.HopList
		}{{fmt.Sprintf("accounts[%d].pre_socks5", i), account.PreSOCKS5}, {fmt.Sprintf("accounts[%d].post_socks5", i), account.PostSOCKS5}} {
			if err := validate(item.field, item.hops); err != nil {
				return routePool{}, err
			}
		}
	}
	pool := routePool{
		nodePre:       map[string][]config.HopList{},
		nodePost:      map[string][]config.HopList{},
		nodes:         map[string][]map[string]any{},
		labels:        map[string][]string{},
		tiers:         map[string][]int{},
		accountRoutes: map[string]string{},
		routePre:      map[string]config.HopList{},
		routePost:     map[string]config.HopList{},
	}
	infoSeen := make(map[string]bool)
	type demand struct {
		providers []string
		countries []string
		key       string
		pre       config.HopList
		post      config.HopList
		rawPre    config.HopList
		rawPost   config.HopList
		perNode   bool
		direct    bool
	}
	sortedProviders := make([]string, 0, len(snapshots))
	for name := range snapshots {
		sortedProviders = append(sortedProviders, name)
	}
	sort.Strings(sortedProviders)
	seen := make(map[string]bool)
	var wanted []demand // in account order, so the first failure is stable
	for _, a := range accounts {
		providers, countries := routeLists(a)
		pre, post := inheritChain(a.PreSOCKS5, globalPre), inheritChain(a.PostSOCKS5, globalPost)
		direct := a.Exit == "direct" || (len(providers) == 0 && len(countries) == 0)
		if a.Exit == "direct" {
			pre = config.HopList{"none"}
		}
		relevant := providers
		if len(relevant) == 0 {
			relevant = sortedProviders
		}
		tag := ""
		if !direct {
			tag = hops.tag(relevant, a.PreSOCKS5, a.PostSOCKS5)
		}
		multiPool := len(pre) > 1 || len(post) > 1 || hops.hasMulti(relevant, a.PreSOCKS5, a.PostSOCKS5)
		routeTag := tag
		if multiPool {
			routeTag = hops.identityTag(relevant, a.PreSOCKS5, a.PostSOCKS5)
		}
		key := routeIdentityKey(a.Login, config.JoinList(providers), config.JoinList(countries), pre, post, multiPool, tagArgs(routeTag)...)
		if direct {
			// Keep implicit default-direct behavior unchanged. Explicit direct
			// accounts get distinct keys based on their effective post hop.
			if a.Exit != "direct" && (len(post) == 0 || (isNoneHops(post) && (!isNoneHops(a.PostSOCKS5) || len(globalPost) == 0 || isNoneHops(globalPost)))) {
				continue
			}
			pre = config.HopList{"none"}
			if a.Exit == "direct" && len(post) == 0 {
				post = config.HopList{"none"}
			}
			key = routeIdentityKey(a.Login, "", "", pre, post, len(pre) > 1 || len(post) > 1)
		}
		pool.accountRoutes[a.Login] = key
		if !seen[key] {
			seen[key] = true
			wanted = append(wanted, demand{providers: providers, countries: countries, key: key, pre: pre, post: post, rawPre: a.PreSOCKS5, rawPost: a.PostSOCKS5, perNode: tag != "", direct: direct})
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
			pool.routePre[d.key] = config.HopList{"none"}
			pool.routePost[d.key] = d.post
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
					if d.perNode {
						pre, post := d.pre, d.post
						if len(d.rawPre) == 0 && len(hops.pre[n.provider]) > 0 {
							pre = hops.pre[n.provider]
						}
						if len(d.rawPost) == 0 && len(hops.post[n.provider]) > 0 {
							post = hops.post[n.provider]
						}
						pool.nodePre[key] = append(pool.nodePre[key], pre)
						pool.nodePost[key] = append(pool.nodePost[key], post)
					}
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
		if node.Label != "" {
			down := n.Down
			node.Down = &down
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
	value  config.HopList
	source string
	auth   bool
}

func resolveChain(account, global config.HopList) resolvedChain {
	source, effective := "account", account
	if len(effective) == 0 {
		source, effective = "global", global
	}
	if len(effective) == 0 || isNoneHops(effective) {
		return resolvedChain{value: config.HopList{"none"}, source: "none"}
	}
	value := make(config.HopList, 0, len(effective))
	auth := false
	for _, hop := range effective {
		u, err := url.Parse(hop)
		if err != nil {
			value = append(value, "configured")
			continue
		}
		auth = auth || u.User != nil
		u.User = nil
		u.Path, u.RawQuery, u.Fragment = "", "", ""
		value = append(value, u.String())
	}
	return resolvedChain{value: value, source: source, auth: auth}
}

// resolveAccountChain is the cascade global -> provider -> account for one side
// of an account. Provider routes with different effective lists report a
// singleton "varies" marker.
func resolveAccountChain(cfg *config.Config, a *config.Account, post bool) resolvedChain {
	own, global := a.PreSOCKS5, cfg.Chain.PreSOCKS5
	if post {
		own, global = a.PostSOCKS5, cfg.Chain.PostSOCKS5
	}
	if len(own) > 0 {
		return resolveChain(own, global)
	}
	names, _ := config.ParseProviderList(a.Provider)
	if len(names) == 0 {
		for _, s := range cfg.Subscriptions {
			names = append(names, s.Name)
		}
	}
	h := hopsOf(cfg.Subscriptions)
	set := h.pre
	if post {
		set = h.post
	}
	var firstRaw config.HopList
	hasFirst := false
	source := "none"
	for _, name := range names {
		effective := set[name]
		provider := len(effective) > 0
		if !provider {
			effective = global
		}
		if !hasFirst {
			firstRaw = append(config.HopList(nil), effective...)
			hasFirst = true
		}
		if !slices.Equal(firstRaw, effective) {
			return resolvedChain{value: config.HopList{"varies"}, source: "provider"}
		}
		if provider {
			source = "provider"
		} else if source == "none" && len(global) > 0 && !isNoneHops(global) {
			source = "global"
		}
	}
	if len(names) == 0 {
		return resolveChain(nil, global)
	}
	if len(firstRaw) == 1 && strings.EqualFold(firstRaw[0], "none") || len(firstRaw) == 0 {
		return resolvedChain{value: config.HopList{"none"}, source: "none"}
	}
	resolved := resolveChain(firstRaw, nil)
	resolved.source = source
	return resolved
}

func statusAccountChain(accountPre, accountPost, globalPre, globalPost config.HopList) *angel.AccountChain {
	pre, post := resolveChain(accountPre, globalPre), resolveChain(accountPost, globalPost)
	return &angel.AccountChain{PreSOCKS5: pre.value, PreSOCKS5Source: pre.source, PreSOCKS5Auth: pre.auth,
		PostSOCKS5: post.value, PostSOCKS5Source: post.source, PostSOCKS5Auth: post.auth}
}

func chainOfResolved(pre, post resolvedChain) *angel.AccountChain {
	return &angel.AccountChain{PreSOCKS5: pre.value, PreSOCKS5Source: pre.source, PreSOCKS5Auth: pre.auth,
		PostSOCKS5: post.value, PostSOCKS5Source: post.source, PostSOCKS5Auth: post.auth}
}

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
		var chain *angel.AccountChain
		if a.Exit == "direct" {
			chain = statusAccountChain(config.HopList{"none"}, a.PostSOCKS5, cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5)
		} else {
			chain = chainOfResolved(resolveAccountChain(cfg, &a, false), resolveAccountChain(cfg, &a, true))
		}
		out = append(out, angel.AccountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: country, Exit: effectiveExit(&a), AllowedClients: a.AllowedClients, Chain: chain})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}
