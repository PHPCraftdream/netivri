package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// idleAfter is how long a fallback node must have carried no traffic to be
// probed at the idle interval, see WarmOptions.Idle.
const idleAfter = 5 * time.Minute

// memberProbeKeys keys every probed member name of the plans by what the probe
// measures: the node's own settings (but not its name) and the pre and post
// hop of its route. Members with one key are the same node reached the same
// way, such as one server listed by two providers or one node of two accounts,
// and the warmer probes one of them (WarmOptions.Same).
func memberProbeKeys(plans []routePlan, o Options) map[string]string {
	keys := make(map[string]string)
	for _, plan := range plans {
		nodes := o.RouteNodes[plan.key]
		for i, name := range plan.names {
			if name == "DIRECT" || i >= len(nodes) {
				continue
			}
			pre := strings.Join(o.nodePrePool(plan.key, i), "\x00")
			post := strings.Join(o.nodePostPool(plan.key, i), "\x00")
			baseKey := probeKey(nodes[i], pre, "")
			if baseKey != "" {
				base := name
				if isRoutePostName(name) {
					base = strings.TrimSuffix(name, "-post")
				}
				keys[base] = baseKey
			}
			key := probeKey(nodes[i], pre, post)
			if key != "" {
				keys[name] = key
			}
		}
	}
	return keys
}

// probeKey hashes the settings of a node apart from its name. "" when they
// cannot be hashed, which probes the node alone.
func probeKey(node map[string]any, pre, post string) string {
	plain := make(map[string]any, len(node))
	for k, v := range node {
		if k != "name" {
			plain[k] = v
		}
	}
	raw, err := json.Marshal([]any{plain, pre, post})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// fallbackIdle returns the WarmOptions.Idle of the plans: a member of a worse
// tier than the best of its route, that has carried no traffic for idleAfter
// while some better member of the route is up. A node nobody falls back to yet
// does not need a probe every interval.
func fallbackIdle(plans []routePlan, health *healthTable) func(string) bool {
	type fallback struct {
		key    string
		better []string
	}
	members := make(map[string]fallback)
	for _, plan := range plans {
		if len(plan.tiers) != len(plan.names) || len(plan.labels) != len(plan.names) {
			continue
		}
		best := plan.tiers[0]
		for _, tier := range plan.tiers {
			best = min(best, tier)
		}
		for i, name := range plan.names {
			if name == "DIRECT" || plan.tiers[i] == best {
				continue
			}
			var better []string
			for j, tier := range plan.tiers {
				if tier < plan.tiers[i] {
					key := plan.labels[j]
					if j < len(plan.healthKeys) && plan.healthKeys[j] != "" {
						key = plan.healthKeys[j]
					}
					better = append(better, key)
				}
			}
			key := plan.labels[i]
			if i < len(plan.healthKeys) && plan.healthKeys[i] != "" {
				key = plan.healthKeys[i]
			}
			members[name] = fallback{key: key, better: better}
		}
	}
	if len(members) == 0 {
		return nil
	}
	return func(name string) bool {
		m, ok := members[name]
		if !ok {
			return false
		}
		h := health.health(m.key)
		if h == nil || !h.idleFor(idleAfter) {
			return false
		}
		for _, key := range m.better {
			if !health.downKey(key) {
				return true
			}
		}
		return false
	}
}
