package core

import (
	"fmt"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const accountGroupPrefix = "netivri-group-"

// prepareRoutes builds independent Mihomo routing mappings without mutating
// the caller's node maps or route node slices. Route keys are opaque to the
// core: they are sorted, indexed and replaced by internal names, so a key
// never reaches a rule or a proxy name.
func prepareRoutes(o Options) ([]map[string]any, []map[string]any, []string, error) {
	if len(o.AccountRoute) == 0 {
		if o.Node == nil {
			return nil, nil, nil, nil
		}
		proxies, rules, err := prepareNode(o.Node)
		return proxies, nil, rules, err
	}

	var defaultProxy map[string]any
	if o.Node != nil {
		prepared, _, err := prepareNode(o.Node)
		if err != nil {
			return nil, nil, nil, err
		}
		defaultProxy = prepared[0]
	}

	logins := make([]string, 0, len(o.AccountRoute))
	routeKeys := make(map[string]struct{})
	for login, routeKey := range o.AccountRoute {
		if !validAccountLogin(login) {
			return nil, nil, nil, nodeInvalid("invalid account login")
		}
		if !validRouteKey(routeKey) {
			return nil, nil, nil, nodeInvalid("invalid account route key")
		}
		logins = append(logins, login)
		routeKeys[routeKey] = struct{}{}
	}
	sort.Strings(logins)

	keys := make([]string, 0, len(routeKeys))
	for key := range routeKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keyIndex := make(map[string]int, len(keys))
	for index, key := range keys {
		keyIndex[key] = index
	}

	var proxies []map[string]any
	if defaultProxy != nil {
		proxies = append(proxies, defaultProxy)
	}
	groups := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		nodes := o.RouteNodes[key]
		if len(nodes) == 0 {
			return nil, nil, nil, &cli.Error{
				Code:    "NO_NODES_FOR_COUNTRY",
				Message: "no nodes are available for an account route",
				Exit:    cli.ExitNotFound,
			}
		}

		members := make([]string, 0, len(nodes))
		for index, node := range nodes {
			prepared, _, err := prepareNode(node)
			if err != nil {
				return nil, nil, nil, err
			}
			name := fmt.Sprintf("netivri-r%d-%d", keyIndex[key], index)
			prepared[0]["name"] = name
			proxies = append(proxies, prepared[0])
			members = append(members, name)
		}
		groups = append(groups, map[string]any{
			"name":    fmt.Sprintf("%s%d", accountGroupPrefix, keyIndex[key]),
			"type":    "select",
			"proxies": members,
		})
	}

	rules := make([]string, 0, len(logins)+1)
	for _, login := range logins {
		group := fmt.Sprintf("%s%d", accountGroupPrefix, keyIndex[o.AccountRoute[login]])
		rules = append(rules, "IN-USER,"+login+","+group)
	}
	fallback := "DIRECT"
	if o.Node != nil {
		fallback = nodeName
	}
	rules = append(rules, "MATCH,"+fallback)
	return proxies, groups, rules, nil
}

func validAccountLogin(login string) bool {
	if len(login) < 1 || len(login) > 64 {
		return false
	}
	for _, c := range []byte(login) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '@' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// validRouteKey accepts opaque route keys of 1..128 printable ASCII
// characters (0x20..0x7E). Keys are internal only and never reach a rule.
func validRouteKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range []byte(key) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
