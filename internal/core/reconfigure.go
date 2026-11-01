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
// place: the instance or the new options route through a default node, or
// one of them has no account routes. The caller restarts the instance.
var ErrReconfigureUnsupported = errors.New("core: this change needs a new instance")

// errInstanceClosed is returned by Reconfigure on a closed instance.
var errInstanceClosed = errors.New("core: the instance is closed")

// Reconfigure replaces the account routes of a running instance: the nodes,
// their labels and tiers and the login-to-route map of o. Bind, Port,
// Authenticator and HomeDir are ignored. AllowedClients keeps the mixed
// listener's existing filter; AccountAllowedClients supplies the server
// listeners' policy. Open connections finish on the node they use.
//
// What a node label keeps across the change: its health (measurements, down
// state, hedge budget), its probe backoff and the site bindings that point at
// it, as long as the login keeps its route key and the route still contains
// the label. Nothing is
// announced for a node that keeps its state. InitialBindings is ignored.
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
	if len(o.AccountRoute) == 0 && len(o.Servers) == 0 && len(i.Servers()) == 0 {
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
	if old.table == nil || i.route != routeDirect || i.poller == nil {
		if old.table != nil || i.route != routeDirect || len(i.state().accountRoutes) != 0 {
			return ErrReconfigureUnsupported
		}
		if len(o.AccountRoute) != 0 || len(o.RouteNodes) != 0 || len(o.RoutePre) != 0 || len(o.RoutePost) != 0 || (o.GlobalPost != "" && !isNoChain(o.GlobalPost)) {
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
	rawProxies, _, rawRules, err := prepareRoutesGen(o, gen)
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
	names := planProxyNames(plans)
	for _, mapping := range rawProxies {
		if name, ok := mapping["name"].(string); ok && name != "DIRECT" {
			names = append(names, name)
		}
	}
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
		for _, name := range plan.names {
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
	backoff := carryBackoff(old.warm.backoffByName(), old.memberLabels, memberNameLabels(plans))

	health := carryHealth(old.health, planLabels(plans), o.OnNodeHealth)
	// The poller also feeds the nodes that only open connections still use;
	// their records are made now, before the new groups read the table
	// without a lock.
	nameToLabel := memberNameLabels(plans)
	connLabels := liveConnLabels(managerSource{statistic.DefaultManager}, old.connLabels)
	for name, label := range nameToLabel {
		connLabels[name] = label
	}
	health.register(mapValues(connLabels))
	table := newAffinityTable(nil, o.AffinityTTL)
	table.setReturnAfter(o.TierReturnAfter)
	table.carryCounters(old.table)
	loadInitialBindings(table, plans, o.AccountRoute, sameRouteBindings(old.snapshotBindings(), old.accountRoutes, o.AccountRoute))
	groups, err := buildStickyGroups(plans, members, table, health, o.Observer, o.OnConnEvent)
	if err != nil {
		return err
	}

	updated := make(map[string]C.Proxy, len(proxies)+len(members)+len(groups))
	for name, proxy := range proxies {
		updated[name] = proxy
	}
	for _, name := range old.prevNames {
		delete(updated, name)
	}
	for name, proxy := range members {
		updated[name] = proxy
	}
	for name, group := range groups {
		updated[name] = group
	}
	// Proxies before rules: in between, the old rules still find the old
	// groups, and afterwards the new rules find the new ones.
	tunnel.UpdateProxies(updated, tunnel.Providers())
	tunnel.UpdateRules(rules, nil, tunnel.RuleProviders())

	var warm *warmer
	if o.Warm != nil {
		wo := *o.Warm
		wo.OnProbe = probeHook(nameToLabel, health, o.Observer, o.Warm.OnProbe)
		wo.OnProbeError = probeErrorHook(nameToLabel, health, o.Warm.OnProbeError)
		wo.Down = func(name string) bool { return health.down(nameToLabel[name]) }
		wo.only = func(name string) bool { return generatedMemberName(name) && nameToLabel[name] != "" }
		wo.Idle = fallbackIdle(plans, health)
		keys := memberProbeKeys(plans, o)
		wo.Same = func(name string) string { return keys[name] }
		wo.seed = backoff
		if warm, err = startWarm(wo, nil); err != nil {
			return err
		}
	}

	i.poller.setTargets(health, connLabels)
	if err := serverTx.commit(); err != nil {
		return err
	}
	committed = true
	i.routing.Store(&routingState{
		gen:           gen,
		accountRoutes: copyAccountRoutes(o.AccountRoute),
		warm:          warm,
		table:         table,
		health:        health,
		memberLabels:  nameToLabel,
		connLabels:    connLabels,
		routeLabels:   routeLabels(plans),
		names:         names,
		prevNames:     old.names,
	})
	return nil
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
func carryBackoff(old map[string]probeSchedule, oldLabels, newLabels map[string]string) map[string]probeSchedule {
	byLabel := make(map[string]probeSchedule, len(old))
	for name, st := range old {
		label, ok := oldLabels[name]
		if !ok {
			continue
		}
		if prev, seen := byLabel[label]; !seen || st.downRounds > prev.downRounds {
			byLabel[label] = st
		}
	}
	out := make(map[string]probeSchedule)
	for name, label := range newLabels {
		if st, ok := byLabel[label]; ok {
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
