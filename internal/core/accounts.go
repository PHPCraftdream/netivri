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
		return prepareUnroutedGen(o, gen)
	}

	var defaultProxy map[string]any
	if o.Node != nil {
		prepared, _, err := prepareNode(o.Node)
		if err != nil {
			return nil, nil, nil, err
		}
		defaultProxy = prepared[0]
	}

	logins, routeKeys, err := accountRouteKeys(o.AccountRoute)
	if err != nil {
		return nil, nil, nil, err
	}
	keys, keyIndex := sortedRouteKeyIndex(routeKeys)

	rb := &routeBuild{o: o, gen: gen, keyIndex: keyIndex, builtPools: make(map[string]bool)}
	if defaultProxy != nil {
		rb.proxies = append(rb.proxies, defaultProxy)
	}
	globalPost := o.globalPostPool()
	if len(globalPost) != 0 {
		post, err := globalPostMapping(globalPost, gen, o.Node != nil)
		if err != nil {
			return nil, nil, nil, err
		}
		if post["type"] == "select" {
			rb.poolGroups = append(rb.poolGroups, post)
		} else {
			rb.proxies = append(rb.proxies, post)
		}
	}
	rb.proxyNames = make(map[string]struct{}, len(rb.proxies))
	for _, proxy := range rb.proxies {
		if name, ok := proxy["name"].(string); ok {
			rb.proxyNames[name] = struct{}{}
		}
	}
	groups := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		members, direct, err := rb.addRoute(key)
		if err != nil {
			return nil, nil, nil, err
		}
		if direct {
			continue
		}
		groups = append(groups, map[string]any{
			"name":    groupProxyName(gen, keyIndex[key]),
			"type":    "select",
			"proxies": members,
		})
	}
	groups = append(groups, rb.poolGroups...)

	rules := make([]string, 0, len(logins)+1)
	for _, login := range logins {
		rules = append(rules, rb.accountRule(login))
	}
	fallback := "DIRECT"
	if o.globalPostActive() {
		fallback = globalPostProxyName(gen)
	} else if o.Node != nil {
		fallback = nodeName
	}
	rules = append(rules, "MATCH,"+fallback)
	return rb.proxies, groups, rules, nil
}

// prepareUnroutedGen is prepareRoutesGen without account routes: the default
// node and the global post pool, if any, behind MATCH.
func prepareUnroutedGen(o Options, gen int) ([]map[string]any, []map[string]any, []string, error) {
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

// routeBuild collects the proxies, select groups and proxy names of one
// routing generation while prepareRoutesGen walks the account routes.
type routeBuild struct {
	o          Options
	gen        int
	keyIndex   map[string]int
	proxies    []map[string]any
	poolGroups []map[string]any
	proxyNames map[string]struct{}
	builtPools map[string]bool
}

// routeKinds reports the shape of route key with n nodes: directPost is a
// post chain without a node, directRoute has neither a node nor any chain.
func (o Options) routeKinds(key string, n int) (directPost, directRoute bool) {
	pre, post := o.routePrePool(key), o.routePostPool(key)
	directPost = n == 0 && len(post) > 0 && len(pre) == 0
	directRoute = n == 0 && len(pre) == 0 && len(post) == 0 && (explicitNoChain(o.RoutePre, key) || explicitEmptyPool(o.RoutePrePool, key)) && (explicitNoChain(o.RoutePost, key) || explicitEmptyPool(o.RoutePostPool, key))
	return directPost, directRoute
}

// accountRouteKeys validates the login-to-route map and returns the logins,
// sorted, and the set of distinct route keys.
func accountRouteKeys(routes map[string]string) ([]string, map[string]struct{}, error) {
	logins := make([]string, 0, len(routes))
	routeKeys := make(map[string]struct{})
	for login, routeKey := range routes {
		if !validAccountLogin(login) {
			return nil, nil, nodeInvalid("invalid account login")
		}
		if !validRouteKey(routeKey) {
			return nil, nil, nodeInvalid("invalid account route key")
		}
		logins = append(logins, login)
		routeKeys[routeKey] = struct{}{}
	}
	sort.Strings(logins)
	return logins, routeKeys, nil
}

// sortedRouteKeyIndex sorts the distinct route keys; the index of a key is its
// route number in the proxy names.
func sortedRouteKeyIndex(routeKeys map[string]struct{}) ([]string, map[string]int) {
	keys := make([]string, 0, len(routeKeys))
	for key := range routeKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keyIndex := make(map[string]int, len(keys))
	for index, key := range keys {
		keyIndex[key] = index
	}
	return keys, keyIndex
}

// addRoute adds the proxies of route key. It returns the member names of the
// route's select group, or direct when the route has no group of its own.
func (rb *routeBuild) addRoute(key string) ([]string, bool, error) {
	o := rb.o
	nodes := o.RouteNodes[key]
	directPost, directRoute := o.routeKinds(key, len(nodes))
	if len(nodes) == 0 && !directPost && !directRoute {
		return nil, false, &cli.Error{
			Code:    "NO_NODES_FOR_COUNTRY",
			Message: "no nodes are available for an account route",
			Exit:    cli.ExitNotFound,
		}
	}
	if directPost {
		if err := rb.addDirectPost(key); err != nil {
			return nil, false, err
		}
	}
	if directPost || directRoute {
		return nil, true, nil
	}
	if err := checkedNodeHops(o, key, len(nodes)); err != nil {
		return nil, false, err
	}
	if err := checkedPoolNodeHops(o, key, len(nodes)); err != nil {
		return nil, false, err
	}
	members := make([]string, 0, len(nodes))
	for index, node := range nodes {
		member, err := rb.addNode(key, index, node)
		if err != nil {
			return nil, false, err
		}
		members = append(members, member)
	}
	return members, false, nil
}

// addDirectPost adds the post chain of route key when it has no node: one
// distinct hop chains DIRECT, several become a select group of their own.
func (rb *routeBuild) addDirectPost(key string) error {
	routePost := rb.o.routePostPool(key)
	postName := helperProxyName(rb.gen, rb.keyIndex[key], 0, "post")
	postPool := newHopPoolSpec(postHopPool, routePost, rb.gen)
	if postPool == nil || len(postPool.keys) <= 1 {
		// Keep the legacy scalar direct-post mapping unchanged when the
		// effective pool has only one distinct hop.
		uri := routePost[0]
		if postPool != nil {
			uri = postPool.uris[0]
		}
		post, err := chainProxy(uri, postName)
		if err != nil {
			return err
		}
		post["dialer-proxy"] = "DIRECT"
		rb.proxies = append(rb.proxies, post)
		return nil
	}
	rb.poolGroups = append(rb.poolGroups, map[string]any{"name": postName, "type": "select", "proxies": []string{"DIRECT"}})
	return nil
}

// addNode adds the proxies of node index of route key and returns the member
// name its group selects.
func (rb *routeBuild) addNode(key string, index int, node map[string]any) (string, error) {
	o := rb.o
	prepared, _, err := prepareNode(node)
	if err != nil {
		return "", err
	}
	name := memberProxyName(rb.gen, rb.keyIndex[key], index)
	prePool := newHopPoolSpec(preHopPool, o.nodePrePool(key, index), rb.gen)
	if prePool != nil && len(prePool.keys) > 1 {
		if !rb.builtPools[prePool.name] {
			addPoolPlaceholder(prePool, &rb.proxies, &rb.poolGroups, rb.proxyNames)
			rb.builtPools[prePool.name] = true
		}
		prepared[0]["dialer-proxy"] = prePool.name
	} else if prePool != nil {
		preName := helperProxyName(rb.gen, rb.keyIndex[key], index, "pre")
		pre, err := chainProxy(prePool.uris[0], preName)
		if err != nil {
			return "", err
		}
		prepared[0]["dialer-proxy"] = preName
		rb.proxies = append(rb.proxies, pre)
		rb.proxyNames[preName] = struct{}{}
	}
	member := name
	postPool := newHopPoolSpec(postHopPool, o.nodePostPool(key, index), rb.gen)
	if postPool != nil && len(postPool.keys) > 1 {
		postName := helperProxyName(rb.gen, rb.keyIndex[key], index, "post")
		rb.poolGroups = append(rb.poolGroups, map[string]any{"name": postName, "type": "select", "proxies": []string{"DIRECT"}})
		member = postName
	} else if postPool != nil {
		postName := helperProxyName(rb.gen, rb.keyIndex[key], index, "post")
		post, err := chainProxy(postPool.uris[0], postName)
		if err != nil {
			return "", err
		}
		post["dialer-proxy"] = name
		rb.proxies = append(rb.proxies, post)
		rb.proxyNames[postName] = struct{}{}
		member = postName
	}
	prepared[0]["name"] = name
	rb.proxies = append(rb.proxies, prepared[0])
	rb.proxyNames[name] = struct{}{}
	return member, nil
}

// accountRule is the rule line of login: DIRECT for a direct route, the post
// proxy for a direct-post route, else the route's select group.
func (rb *routeBuild) accountRule(login string) string {
	key := rb.o.AccountRoute[login]
	nodes := rb.o.RouteNodes[key]
	directPost, directRoute := rb.o.routeKinds(key, len(nodes))
	if directRoute {
		return "IN-USER," + login + ",DIRECT"
	}
	if directPost {
		return "IN-USER," + login + "," + helperProxyName(rb.gen, rb.keyIndex[key], 0, "post")
	}
	return "IN-USER," + login + "," + groupProxyName(rb.gen, rb.keyIndex[key])
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

func printableASCII(s string, limit int) bool {
	if len(s) < 1 || len(s) > limit {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
