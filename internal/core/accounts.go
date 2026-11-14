package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const accountGroupPrefix = "netivri-group-"

func proxyMappingNames(mappings []map[string]any) []string {
	var names []string
	for _, mapping := range mappings {
		if name, ok := mapping["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// prepareRoutes builds independent Mihomo routing mappings without mutating
// the caller's node maps or route node slices. Route keys are opaque to the
// core: they are sorted, indexed and replaced by internal names, so a key
// never reaches a rule or a proxy name.
func prepareRoutes(o Options) ([]map[string]any, []map[string]any, []string, error) {
	return prepareRoutesGen(o, 0)
}

// memberProxyName is the internal name of node n of route r in routing
// generation gen. Generation 0 (Start) keeps the short form; every
// Reconfigure uses names of its own, so a connection opened before it is
// still attributed to the node it really went through.
func memberProxyName(gen, r, n int) string {
	if gen == 0 {
		return fmt.Sprintf("netivri-r%d-%d", r, n)
	}
	return fmt.Sprintf("netivri-g%d-r%d-%d", gen, r, n)
}

func helperProxyName(gen, route, node int, side string) string {
	if gen == 0 {
		return fmt.Sprintf("netivri-r%d-%d-%s", route, node, side)
	}
	return fmt.Sprintf("netivri-g%d-r%d-%d-%s", gen, route, node, side)
}

func helperProxyNameFromMember(member string, side string) string { return member + "-" + side }

func routeGroupMemberName(gen, route, node int, post bool) string {
	member := memberProxyName(gen, route, node)
	if post {
		return helperProxyNameFromMember(member, "post")
	}
	return member
}

func generatedNodeName(name string) bool { return isRouteNodeName(name) }

func isRouteNodeName(name string) bool {
	if !strings.HasPrefix(name, "netivri-") {
		return false
	}
	body := strings.TrimPrefix(name, "netivri-")
	if strings.HasPrefix(body, "g") {
		generation, rest, ok := strings.Cut(body[1:], "-")
		if !ok || !allDigits(generation) {
			return false
		}
		body = rest
	}
	if !strings.HasPrefix(body, "r") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(body, "r"), "-")
	return len(parts) == 2 && allDigits(parts[0]) && allDigits(parts[1])
}
func allDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < 48 || c > 57 {
			return false
		}
	}
	return true
}
func isChainHelperName(name string) bool {
	for _, side := range []string{"-pre", "-post"} {
		if strings.HasSuffix(name, side) && isRouteNodeName(strings.TrimSuffix(name, side)) {
			return true
		}
	}
	return false
}
func generatedMemberName(name string) bool { return isRouteNodeName(name) || isChainHelperName(name) }
func isRoutePostName(name string) bool {
	return strings.HasSuffix(name, "-post") && isRouteNodeName(strings.TrimSuffix(name, "-post"))
}

func globalPostProxyName(gen int) string {
	if gen == 0 {
		return "netivri-global-post"
	}
	return fmt.Sprintf("netivri-g%d-global-post", gen)
}

// groupProxyName is the internal name of the group of route r in routing
// generation gen.
func groupProxyName(gen, r int) string {
	if gen == 0 {
		return fmt.Sprintf("%s%d", accountGroupPrefix, r)
	}
	return fmt.Sprintf("netivri-g%d-group-%d", gen, r)
}

// prepareRoutesGen is prepareRoutes with the proxy names of routing
// generation gen.
func prepareRoutesGen(o Options, gen int) ([]map[string]any, []map[string]any, []string, error) {
	if err := validateOptionHopPools(o); err != nil {
		return nil, nil, nil, err
	}
	if len(o.AccountRoute) == 0 {
		globalPost := o.globalPostPool()
		if o.Node == nil && len(globalPost) == 0 {
			return nil, nil, nil, nil
		}
		if o.Node == nil {
			post, err := globalPostMapping(globalPost, gen, false)
			if err != nil {
				return nil, nil, nil, err
			}
			rules := []string{"MATCH," + globalPostProxyName(gen)}
			if post["type"] == "select" {
				return nil, []map[string]any{post}, rules, nil
			}
			return []map[string]any{post}, nil, rules, nil
		}
		proxies, rules, err := prepareNode(o.Node)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(globalPost) != 0 {
			// A pool of several hops is refused with a default node (see
			// validateOptionHopPools), so this is the legacy scalar chain.
			post, err := globalPostMapping(globalPost, gen, true)
			if err != nil {
				return nil, nil, nil, err
			}
			return append(proxies, post), nil, []string{"MATCH," + globalPostProxyName(gen)}, nil
		}
		return proxies, nil, rules, nil
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
	var poolGroups []map[string]any
	builtPools := make(map[string]bool)
	if defaultProxy != nil {
		proxies = append(proxies, defaultProxy)
	}
	globalPost := o.globalPostPool()
	if len(globalPost) != 0 {
		post, err := globalPostMapping(globalPost, gen, o.Node != nil)
		if err != nil {
			return nil, nil, nil, err
		}
		if post["type"] == "select" {
			poolGroups = append(poolGroups, post)
		} else {
			proxies = append(proxies, post)
		}
	}
	proxyNames := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		if name, ok := proxy["name"].(string); ok {
			proxyNames[name] = struct{}{}
		}
	}
	groups := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		nodes := o.RouteNodes[key]
		pre := o.routePrePool(key)
		post := o.routePostPool(key)
		directPost := len(nodes) == 0 && len(post) > 0 && len(pre) == 0
		directRoute := len(nodes) == 0 && len(pre) == 0 && len(post) == 0 && (explicitNoChain(o.RoutePre, key) || explicitEmptyPool(o.RoutePrePool, key)) && (explicitNoChain(o.RoutePost, key) || explicitEmptyPool(o.RoutePostPool, key))
		if len(nodes) == 0 && !directPost && !directRoute {
			return nil, nil, nil, &cli.Error{
				Code:    "NO_NODES_FOR_COUNTRY",
				Message: "no nodes are available for an account route",
				Exit:    cli.ExitNotFound,
			}
		}

		members := make([]string, 0, len(nodes))
		if directRoute || directPost {
			if directPost {
				routePost := o.routePostPool(key)
				postName := helperProxyName(gen, keyIndex[key], 0, "post")
				postPool := newHopPoolSpec(postHopPool, routePost, gen)
				if postPool == nil || len(postPool.keys) <= 1 {
					// Keep the legacy scalar direct-post mapping unchanged when
					// the effective pool has only one distinct hop.
					uri := routePost[0]
					if postPool != nil {
						uri = postPool.uris[0]
					}
					post, err := chainProxy(uri, postName)
					if err != nil {
						return nil, nil, nil, err
					}
					post["dialer-proxy"] = "DIRECT"
					proxies = append(proxies, post)
				} else {
					poolGroups = append(poolGroups, map[string]any{"name": postName, "type": "select", "proxies": []string{"DIRECT"}})
				}
			}
			continue
		}
		if err := checkedNodeHops(o, key, len(nodes)); err != nil {
			return nil, nil, nil, err
		}
		if err := checkedPoolNodeHops(o, key, len(nodes)); err != nil {
			return nil, nil, nil, err
		}
		for index, node := range nodes {
			prepared, _, err := prepareNode(node)
			if err != nil {
				return nil, nil, nil, err
			}
			name := memberProxyName(gen, keyIndex[key], index)
			prePool := newHopPoolSpec(preHopPool, o.nodePrePool(key, index), gen)
			if prePool != nil && len(prePool.keys) > 1 {
				if !builtPools[prePool.name] {
					if err := addPoolPlaceholder(prePool, &proxies, &poolGroups, proxyNames); err != nil {
						return nil, nil, nil, err
					}
					builtPools[prePool.name] = true
				}
				prepared[0]["dialer-proxy"] = prePool.name
			} else if prePool != nil {
				pre, err := chainProxy(prePool.uris[0], helperProxyName(gen, keyIndex[key], index, "pre"))
				if err != nil {
					return nil, nil, nil, err
				}
				prepared[0]["dialer-proxy"] = pre["name"]
				proxies = append(proxies, pre)
				proxyNames[pre["name"].(string)] = struct{}{}
			}
			member := name
			postPool := newHopPoolSpec(postHopPool, o.nodePostPool(key, index), gen)
			if postPool != nil && len(postPool.keys) > 1 {
				postName := helperProxyName(gen, keyIndex[key], index, "post")
				poolGroups = append(poolGroups, map[string]any{"name": postName, "type": "select", "proxies": []string{"DIRECT"}})
				member = postName
			} else if postPool != nil {
				postName := helperProxyName(gen, keyIndex[key], index, "post")
				post, err := chainProxy(postPool.uris[0], postName)
				if err != nil {
					return nil, nil, nil, err
				}
				post["dialer-proxy"] = name
				proxies = append(proxies, post)
				proxyNames[postName] = struct{}{}
				member = postName
			}
			prepared[0]["name"] = name
			proxies = append(proxies, prepared[0])
			proxyNames[name] = struct{}{}
			members = append(members, member)
		}
		groups = append(groups, map[string]any{
			"name":    groupProxyName(gen, keyIndex[key]),
			"type":    "select",
			"proxies": members,
		})
	}
	groups = append(groups, poolGroups...)

	rules := make([]string, 0, len(logins)+1)
	for _, login := range logins {
		key := o.AccountRoute[login]
		nodes := o.RouteNodes[key]
		directPost := len(nodes) == 0 && len(o.routePostPool(key)) > 0 && len(o.routePrePool(key)) == 0
		directRoute := len(nodes) == 0 && len(o.routePrePool(key)) == 0 && len(o.routePostPool(key)) == 0 && (explicitNoChain(o.RoutePre, key) || explicitEmptyPool(o.RoutePrePool, key)) && (explicitNoChain(o.RoutePost, key) || explicitEmptyPool(o.RoutePostPool, key))
		if directRoute {
			rules = append(rules, "IN-USER,"+login+",DIRECT")
			continue
		}
		if directPost {
			rules = append(rules, "IN-USER,"+login+","+helperProxyName(gen, keyIndex[key], 0, "post"))
			continue
		}
		group := groupProxyName(gen, keyIndex[key])
		rules = append(rules, "IN-USER,"+login+","+group)
	}
	fallback := "DIRECT"
	if o.globalPostActive() {
		fallback = globalPostProxyName(gen)
	} else if o.Node != nil {
		fallback = nodeName
	}
	rules = append(rules, "MATCH,"+fallback)
	return proxies, groups, rules, nil
}

// globalPostMapping builds the MATCH post: the legacy chain proxy for one
// distinct hop, a placeholder select group for several (the lifecycle replaces
// it with the pool adapter). uris is the non-empty effective list.
func globalPostMapping(uris []string, gen int, throughNode bool) (map[string]any, error) {
	spec := newHopPoolSpec(postHopPool, uris, gen)
	if spec != nil && len(spec.keys) > 1 {
		return map[string]any{"name": globalPostProxyName(gen), "type": "select", "proxies": []string{"DIRECT"}}, nil
	}
	post, err := chainProxy(uris[0], globalPostProxyName(gen))
	if err != nil {
		return nil, err
	}
	if throughNode {
		post["dialer-proxy"] = nodeName
	}
	return post, nil
}

func explicitEmptyPool(m map[string][]string, key string) bool {
	values, ok := m[key]
	return ok && len(effectivePool(values, true, "")) == 0
}

func (o Options) globalPostPool() []string {
	if o.GlobalPostPool != nil {
		return effectivePool(o.GlobalPostPool, true, "")
	}
	return effectivePool(nil, false, o.GlobalPost)
}

func (o Options) globalPostActive() bool { return len(o.globalPostPool()) != 0 }

func (o Options) routePrePool(key string) []string {
	if values, ok := o.RoutePrePool[key]; ok {
		return effectivePool(values, true, "")
	}
	return effectivePool(nil, false, o.RoutePre[key])
}

func (o Options) routePostPool(key string) []string {
	if values, ok := o.RoutePostPool[key]; ok {
		return effectivePool(values, true, "")
	}
	return effectivePool(nil, false, o.RoutePost[key])
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

// Longest route key and node label, in printable ASCII characters. A route
// key can hold the provider and country lists of an account.
const (
	maxRouteKeyLen  = 2048
	maxNodeLabelLen = 128
)

// validRouteKey accepts opaque route keys of 1..2048 printable ASCII
// characters (0x20..0x7E). Keys are internal only and never reach a rule.
func validRouteKey(key string) bool { return printableASCII(key, maxRouteKeyLen) }

// validNodeLabel accepts statistics labels of 1..128 printable ASCII characters.
func validNodeLabel(label string) bool { return printableASCII(label, maxNodeLabelLen) }

func printableASCII(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
