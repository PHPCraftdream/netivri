package core

import (
	"sort"

	"github.com/metacubex/mihomo/constant"
)

// directPostPoolUses builds lifecycle uses for direct-post routes that have a
// multi-hop post pool. The route index follows prepareRoutesGen: sorted,
// distinct AccountRoute values. Direct routes have no node carrier; the
// lifecycle uses that flag to probe and attribute failures without node
// health evidence.
func directPostPoolUses(o Options, gen int, _ map[string]constant.Proxy) ([]*hopPoolUse, error) {
	routeSet := make(map[string]struct{}, len(o.AccountRoute))
	for _, route := range o.AccountRoute {
		routeSet[route] = struct{}{}
	}
	routes := make([]string, 0, len(routeSet))
	for route := range routeSet {
		routes = append(routes, route)
	}
	sort.Strings(routes)

	var uses []*hopPoolUse
	for index, route := range routes {
		if len(o.RouteNodes[route]) != 0 || len(o.routePrePool(route)) != 0 {
			continue
		}
		post := o.routePostPool(route)
		if len(post) < 2 {
			continue
		}
		spec := newHopPoolSpec(postHopPool, post, gen)
		if spec == nil || len(spec.keys) < 2 {
			continue
		}
		members := make(map[string]member, len(spec.keys))
		for _, key := range spec.keys {
			members[key] = nil
		}
		adapterName := helperProxyName(gen, index, 0, "post")
		logical := hopPoolLogicalKey{role: postHopPool, route: route, node: 0}
		uses = append(uses, &hopPoolUse{
			logical: logical, route: route, role: postHopPool, node: 0,
			spec: spec, adapterName: adapterName, members: members,
			direct: true,
		})
	}
	return uses, nil
}
