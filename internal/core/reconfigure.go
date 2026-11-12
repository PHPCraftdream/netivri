package core

import (
	"errors"
	"fmt"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
	R "github.com/metacubex/mihomo/rules"
	RC "github.com/metacubex/mihomo/rules/common"
	RW "github.com/metacubex/mihomo/rules/wrapper"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

// ErrReconfigureUnsupported reports a change Reconfigure cannot apply in
// place: the instance or the new options route through a default node, a
// node-backed route appears where the instance has none, or the instance runs
// in direct mode. The caller restarts the instance.
var ErrReconfigureUnsupported = errors.New("core: this change needs a new instance")

// errInstanceClosed is returned by Reconfigure on a closed instance.
var errInstanceClosed = errors.New("core: the instance is closed")

var startWarmForReconfigure = startWarm

// Reconfigure replaces the account routes of a running instance: the nodes,
// their labels and tiers and the login-to-route map of o. Bind, Port,
// Authenticator and HomeDir are ignored. AllowedClients keeps the mixed
// listener's existing filter; AccountAllowedClients supplies the server
// listeners' policy. Open connections finish on the node they use.
//
// What a node label keeps across the change: its health (measurements, down
// state, hedge budget), its probe backoff and the site bindings that point at
// it, as long as the login keeps its route key and the route still contains
// the label. Nothing is announced for a node that keeps its state.
// InitialBindings is ignored.
//
// An instance without node-backed routes (direct routes, direct-post pools and
// the global post pool) has no node state: it is updated in place as long as
// the new options have no node-backed route either. Its hop pools keep their
// health by hop identity, as the pools of node-backed routes do.
//
// Mihomo's configuration is not re-applied: the new member proxies and
// sticky groups get names of their own (see memberProxyName) and are added to
// the proxy map together with the new rules. The previous generation stays in
// the map until the next Reconfigure, so a connection matched by the old
// rules a moment before the switch still finds its group.
//
// On ErrReconfigureUnsupported, a validation error or a closed instance
// nothing has changed. Any other error leaves the instance in an undefined
// routing state; the caller should close it and start a new one.
func (i *Instance) Reconfigure(o Options) error {
	if o.AffinityTTL < 0 {
		return nodeInvalid("affinity TTL is negative")
	}
	if o.Node != nil {
		return ErrReconfigureUnsupported
	}
	if o.Warm != nil {
		if _, err := normalizeWarmOptions(*o.Warm); err != nil {
			return err
		}
	}
	if len(o.AccountRoute) == 0 && len(o.Servers) == 0 && len(i.Servers()) == 0 && !i.state().rule {
		return ErrReconfigureUnsupported
	}

	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	i.mu.Lock()
	closed := i.closed
	i.mu.Unlock()
	if closed || current != i {
		return errInstanceClosed
	}
	old := i.state()
	if old.table != nil && len(o.AccountRoute) == 0 {
		return ErrReconfigureUnsupported
	}
	if old.table == nil && old.rule && i.route == routeDirect {
		return i.reconfigureDirect(old, o)
	}
	if old.table == nil || i.route != routeDirect || i.poller == nil {
		if old.table != nil || i.route != routeDirect || len(i.state().accountRoutes) != 0 {
			return ErrReconfigureUnsupported
		}
		if len(o.AccountRoute) != 0 || len(o.RouteNodes) != 0 || len(o.RoutePre) != 0 || len(o.RoutePost) != 0 || o.globalPostActive() {
			return ErrReconfigureUnsupported
		}
		tx, err := i.servers.prepare(o.Servers, i.tt, o.AllowedClients, o.AccountAllowedClients)
		if err != nil {
			return err
		}
		if err := tx.commit(); err != nil {
			tx.rollback()
			return err
		}
		return nil
	}

	// Everything that can fail runs before the instance changes.
	gen := old.gen + 1
	rawProxies, rawGroups, rawRules, err := prepareRoutesGen(o, gen)
	if err != nil {
		return err
	}
	plans, err := buildRoutePlansGen(o, gen)
	if err != nil {
		return err
	}
	members, err := parseMembers(rawProxies)
	if err != nil {
		return err
	}
	names := generationProxyNames(plans, rawProxies, rawGroups)
	proxies := tunnel.Proxies()
	// Route members may intentionally use Mihomo's built-in DIRECT adapter.
	if direct := proxies["DIRECT"]; direct != nil {
		members["DIRECT"] = direct
	}
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	rules, err := parseRouteRules(rawRules, func(name string) bool {
		_, builtin := proxies[name]
		return known[name] || builtin
	})
	if err != nil {
		return err
	}
	for _, plan := range plans {
		for index, name := range plan.names {
			if index < len(plan.postPools) && plan.postPools[index] != nil && len(plan.postPools[index].keys) > 1 {
				continue // parser placeholder; lifecycle installs the real post adapter.
			}
			if members[name] == nil {
				return errors.New("core: missing route member proxy")
			}
		}
	}
	for _, mapping := range rawProxies {
		name, _ := mapping["name"].(string)
		if members[name] == nil {
			return errors.New("core: missing route helper proxy")
		}
	}

	// Stop old probes before carrying their schedules into the next generation.
	serverTx, err := i.servers.prepare(o.Servers, i.tt, o.AllowedClients, o.AccountAllowedClients)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			serverTx.rollback()
		}
	}()
	old.warm.Close()
	nameToLabel := memberNameLabels(plans)
	nameToHealthKey := memberNameHealthKeys(plans)
	// The probe schedule follows the node label, not its hop chain: a changed
	// hop must not make every node of the route probe at once.
	backoff := carryBackoff(old.warm.backoffByName(), old.memberLabels, nameToLabel)

	// The poller also feeds the nodes that only open connections still use;
	// their records are made now, before the new groups read the table
	// without a lock.
	connLabels := liveConnLabels(managerSource{statistic.DefaultManager}, old.connLabels)
	connHealthKeys := liveConnHealthKeys(managerSource{statistic.DefaultManager}, old.connHealthKeys, old.connLabels)
	healthMembers := healthMembersForPlans(plans)
	for name, label := range connLabels {
		key := connHealthKeys[name]
		if key == "" {
			key = label
			connHealthKeys[name] = key
		}
		var hops hopSet
		if old.health != nil {
			hops = old.health.hops[key]
		}
		healthMembers = append(healthMembers, healthMember{key: key, label: label, hops: hops})
	}
	for name, label := range nameToLabel {
		connLabels[name] = label
	}
	for name, key := range nameToHealthKey {
		connHealthKeys[name] = key
	}
	health := old.health
	if health != nil {
		health = carryHealthMembers(old.health, healthMembers, o.OnNodeHealth)
	} else {
		health = newHealthTable()
		health.notify = o.OnNodeHealth
	}
	health.registerMembers(healthMembersForPlans(plans))
	table := newAffinityTable(nil, o.AffinityTTL)
	table.setReturnAfter(o.TierReturnAfter)
	table.carryCounters(old.table)
	loadInitialBindings(table, plans, o.AccountRoute, sameRouteBindings(old.snapshotBindings(), old.accountRoutes, o.AccountRoute))
	poolInputs := make(map[string]C.Proxy, len(proxies)+len(members))
	for name, proxy := range proxies {
		poolInputs[name] = proxy
	}
	for name, proxy := range members {
		poolInputs[name] = proxy
	}
	hopPools := old.hopPools
	if hopPools == nil {
		hopPools = newHopPoolLifecycle(o.OnHopEvent)
	}
	poolMap, poolNames, err := hopPools.installGen(o, plans, poolInputs, health, gen)
	if err != nil {
		return err
	}
	groups, err := buildStickyGroups(plans, poolMap, table, health, o.Observer, o.OnConnEvent)
	if err != nil {
		return err
	}

	updated := make(map[string]C.Proxy, len(poolMap)+len(groups))
	for name, proxy := range poolMap {
		updated[name] = proxy
	}
	for _, name := range old.prevNames {
		delete(updated, name)
	}
	for name, group := range groups {
		updated[name] = group
	}
	names = append(names, poolNames...)
	// Proxies before rules: in between, the old rules still find the old
	// groups, and afterwards the new rules find the new ones.
	tunnel.UpdateProxies(updated, tunnel.Providers())
	tunnel.UpdateRules(rules, nil, tunnel.RuleProviders())

	var warm *warmer
	warmPublished := false
	defer func() {
		if !warmPublished {
			warm.Close()
		}
	}()
	if o.Warm != nil {
		wo := *o.Warm
		nameToHealthKey := memberNameHealthKeys(plans)
		wo.OnProbe = probeHookKeys(nameToLabel, nameToHealthKey, nil, o.Observer, o.Warm.OnProbe)
		wo.OnProbeError = probeErrorHookKeys(nameToLabel, nameToHealthKey, nil, o.Warm.OnProbeError)
		wo.onProbeGroup = probeGroupHealthHook(nameToHealthKey, health)
		wo.Down = func(name string) bool { return health.downKey(nameToHealthKey[name]) }
		wo.only = func(name string) bool { return generatedMemberName(name) && nameToLabel[name] != "" }
		wo.Idle = fallbackIdle(plans, health)
		keys := memberProbeKeys(plans, o)
		wo.Same = func(name string) string { return keys[name] }
		wo.seed = backoff
		wo.probeBaseNames = hopPools.baseProbeNames(plans)
		if len(hopPools.uses) > 0 {
			normalizedWarm, normalizeErr := normalizeWarmOptions(wo)
			if normalizeErr != nil {
				return normalizeErr
			}
			reviver, reviveErr := hopPools.newReviver(normalizedWarm)
			if reviveErr != nil {
				return reviveErr
			}
			wo.hopRevive = reviver.Round
		}
		if warm, err = startWarmForReconfigure(wo, nil); err != nil {
			return err
		}
	}

	if err := serverTx.commit(); err != nil {
		return err
	}
	committed = true
	hopPools.completeInstall(o.SiteBinder)
	i.poller.setTargetsWithHealthKeys(health, connLabels, connHealthKeys)
	i.routing.Store(&routingState{
		gen:              gen,
		accountRoutes:    copyAccountRoutes(o.AccountRoute),
		hopPools:         hopPools,
		warm:             warm,
		table:            table,
		health:           health,
		memberLabels:     nameToLabel,
		connLabels:       connLabels,
		memberHealthKeys: nameToHealthKey,
		connHealthKeys:   connHealthKeys,
		routeLabels:      routeLabels(plans),
		names:            names,
		prevNames:        old.names,
	})
	warmPublished = true
	return nil
}

// hasNodeRoute reports whether any account route of o is backed by nodes.
func hasNodeRoute(o Options) bool {
	for _, key := range o.AccountRoute {
		if len(o.RouteNodes[key]) > 0 {
			return true
		}
	}
	return false
}

// matchProbeFilter limits the probed leaves to the global post chain when
// MATCH traffic uses one; nil probes every netivri leaf.
func matchProbeFilter(o Options, gen int) func(name string) bool {
	switch {
	case o.globalPostActive():
		want := globalPostProxyName(gen)
		return func(name string) bool { return name == want }
	case o.Node != nil:
		return func(name string) bool { return name == nodeName }
	}
	return nil
}

// reconfigureDirect applies o to a rule-mode instance that has no node-backed
// route: direct routes, direct-post pools and the global post pool. Such an
// instance has no affinity table, health table or poller, so only the rules,
// the proxy map, the pool lifecycle and the probes change. Options that need a
// node-backed route, or leave no rule at all, need a new instance.
func (i *Instance) reconfigureDirect(old *routingState, o Options) error {
	if hasNodeRoute(o) || (len(o.AccountRoute) == 0 && !o.globalPostActive()) {
		return ErrReconfigureUnsupported
	}
	gen := old.gen + 1
	rawProxies, rawGroups, rawRules, err := prepareRoutesGen(o, gen)
	if err != nil {
		return err
	}
	members, err := parseMembers(rawProxies)
	if err != nil {
		return err
	}
	names := generationProxyNames(nil, rawProxies, rawGroups)
	proxies := tunnel.Proxies()
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	rules, err := parseRouteRules(rawRules, func(name string) bool {
		_, builtin := proxies[name]
		return known[name] || builtin
	})
	if err != nil {
		return err
	}
	for _, mapping := range rawProxies {
		name, _ := mapping["name"].(string)
		if members[name] == nil {
			return errors.New("core: missing route helper proxy")
		}
	}
	serverTx, err := i.servers.prepare(o.Servers, i.tt, o.AllowedClients, o.AccountAllowedClients)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			serverTx.rollback()
		}
	}()
	old.warm.Close()
	poolInputs := make(map[string]C.Proxy, len(proxies)+len(members))
	for name, proxy := range proxies {
		poolInputs[name] = proxy
	}
	for name, proxy := range members {
		poolInputs[name] = proxy
	}
	hopPools := old.hopPools
	if hopPools == nil {
		hopPools = newHopPoolLifecycle(o.OnHopEvent)
	}
	poolMap, poolNames, err := hopPools.installGen(o, nil, poolInputs, nil, gen)
	if err != nil {
		return err
	}
	for _, name := range old.prevNames {
		delete(poolMap, name)
	}
	names = append(names, poolNames...)
	// Proxies before rules, as in Reconfigure.
	tunnel.UpdateProxies(poolMap, tunnel.Providers())
	tunnel.UpdateRules(rules, nil, tunnel.RuleProviders())

	var warm *warmer
	warmPublished := false
	defer func() {
		if !warmPublished {
			warm.Close()
		}
	}()
	if o.Warm != nil {
		wo := *o.Warm
		wo.only = matchProbeFilter(o, gen)
		wo.probeBaseNames = hopPools.baseProbeNames(nil)
		if len(hopPools.uses) > 0 {
			normalizedWarm, normalizeErr := normalizeWarmOptions(wo)
			if normalizeErr != nil {
				return normalizeErr
			}
			reviver, reviveErr := hopPools.newReviver(normalizedWarm)
			if reviveErr != nil {
				return reviveErr
			}
			wo.hopRevive = reviver.Round
		}
		if warm, err = startWarmForReconfigure(wo, nil); err != nil {
			return err
		}
	}
	if err := serverTx.commit(); err != nil {
		return err
	}
	committed = true
	hopPools.completeInstall(o.SiteBinder)
	i.routing.Store(&routingState{
		gen:           gen,
		accountRoutes: copyAccountRoutes(o.AccountRoute),
		hopPools:      hopPools,
		warm:          warm,
		rule:          true,
		names:         names,
		prevNames:     old.names,
	})
	warmPublished = true
	return nil
}

// generationProxyNames includes names introduced by direct routes and raw
// select groups as well as the planned account-route members.
func generationProxyNames(plans []routePlan, proxies, groups []map[string]any) []string {
	names := append(planProxyNames(plans), proxyMappingNames(proxies)...)
	return append(names, proxyMappingNames(groups)...)
}

// parseMembers turns the prepared member mappings into Mihomo proxies keyed
// by their internal names, the way Mihomo's own configuration parser does.
func parseMembers(mappings []map[string]any) (map[string]C.Proxy, error) {
	members := make(map[string]C.Proxy, len(mappings))
	for index, mapping := range mappings {
		proxy, err := adapter.ParseProxy(mapping, adapter.WithTunnelForAPI(tunnel.Tunnel))
		if err != nil {
			// Mihomo parser errors may echo proxy fields, including credentials.
			return nil, fmt.Errorf("proxy %d: invalid proxy configuration", index)
		}
		members[proxy.Name()] = proxy
	}
	return members, nil
}

// parseRouteRules parses rule lines the way Mihomo's configuration parser
// parses top-level rules; every target must be accepted by known.
func parseRouteRules(lines []string, known func(name string) bool) ([]C.Rule, error) {
	rules := make([]C.Rule, 0, len(lines))
	for index, line := range lines {
		tp, payload, target, params := RC.ParseRulePayload(line, true)
		if target == "" || !known(target) {
			return nil, fmt.Errorf("core: route rule %d is invalid", index)
		}
		parsed, err := R.ParseRule(tp, payload, target, params, nil)
		if err != nil {
			return nil, fmt.Errorf("core: route rule %d: %w", index, err)
		}
		rules = append(rules, RW.NewRuleWrapper(parsed))
	}
	return rules, nil
}

// sameRouteBindings keeps the bindings of the logins whose route key did not
// change. A new key means new country or provider lists: the account asked for
// other nodes, so its sites pick anew instead of staying where they were.
func sameRouteBindings(records []BindingRecord, oldRoutes, newRoutes map[string]string) []BindingRecord {
	out := records[:0:0]
	for _, r := range records {
		if key, ok := newRoutes[r.Login]; ok && key == oldRoutes[r.Login] {
			out = append(out, r)
		}
	}
	return out
}

// planLabels lists the node labels of every plan, in plan and member order.
func planLabels(plans []routePlan) []string {
	var labels []string
	for _, plan := range plans {
		labels = append(labels, plan.labels...)
	}
	return labels
}

// carryBackoff moves the probe schedule of the old proxy names to the new
// names of the same node labels. A label that is a member of several routes
// keeps its longest backoff.
func liveConnHealthKeys(source trackerSource, healthKeys, labels map[string]string) map[string]string {
	out := make(map[string]string)
	legacyLabels := len(healthKeys) == 0
	source.Range(func(t throughputTracker) bool {
		info := t.Info()
		if info == nil {
			return true
		}
		for _, name := range info.Chain {
			if key, ok := healthKeys[name]; ok {
				out[name] = key
			} else if legacyLabels {
				if label, ok := labels[name]; ok {
					out[name] = label
				}
			}
		}
		return true
	})
	return out
}

func carryBackoff(old map[string]probeSchedule, oldKeys, newKeys map[string]string) map[string]probeSchedule {
	byKey := make(map[string]probeSchedule, len(old))
	for name, st := range old {
		key, ok := oldKeys[name]
		if !ok {
			continue
		}
		if prev, seen := byKey[key]; !seen || st.downRounds > prev.downRounds || (st.downRounds == prev.downRounds && st.nextDue.After(prev.nextDue)) {
			byKey[key] = st
		}
	}
	out := make(map[string]probeSchedule)
	for name, key := range newKeys {
		if st, ok := byKey[key]; ok {
			out[name] = st
		}
	}
	return out
}

// liveConnLabels keeps the entries of labels whose proxy name is still in
// the chain of an open connection.
func liveConnLabels(source trackerSource, labels map[string]string) map[string]string {
	out := make(map[string]string)
	source.Range(func(t throughputTracker) bool {
		info := t.Info()
		if info == nil {
			return true
		}
		for _, name := range info.Chain {
			if label, ok := labels[name]; ok {
				out[name] = label
			}
		}
		return true
	})
	return out
}
