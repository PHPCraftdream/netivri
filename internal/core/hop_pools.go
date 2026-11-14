package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	hopkey "github.com/PHPCraftdream/netivri/pkg/hopkit/hop"
)

type hopPoolRole uint8

const (
	preHopPool hopPoolRole = iota + 1
	postHopPool
)

type siteBinder interface {
	Get(login, site string) (string, bool)
	Bind(login, site, key string)
	Unbind(login, site string)
	Forget(key string) int
}

type hopPoolSpec struct {
	role     hopPoolRole
	keys     []string
	uris     []string
	name     string
	id       string
	configID string
}

// poolIdentity scopes the ordered health keys to their pre/post pool role.
func poolIdentity(role hopPoolRole, keys []string) string {
	data, _ := json.Marshal(struct {
		Role hopPoolRole
		Keys []string
	}{role, keys})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hopPoolKey(raw string) string {
	key, ok := hopkey.Key(raw)
	if ok {
		return key
	}
	return fallbackHopPoolKey(raw)
}

func fallbackHopPoolKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if _, err := chainProxy(raw, ""); err != nil {
		return ""
	}
	canonical := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	sum := sha256.Sum256([]byte(canonical))
	return "u:" + hex.EncodeToString(sum[:])
}

func newHopPoolSpec(role hopPoolRole, uris []string, gen int) *hopPoolSpec {
	keys, unique := make([]string, 0, len(uris)), make([]string, 0, len(uris))
	mappings := make([]map[string]any, 0, len(uris))
	entryConfigs := make([]string, 0, len(uris))
	seen := make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		if uri == "" || isNoChain(uri) {
			continue
		}
		key := hopPoolKey(uri)
		if key == "" {
			key = invalidHopPoolKey(uri)
		}
		// Health identity deliberately ignores userinfo. Duplicate endpoint
		// keys inside one ordered pool retain the first mapping; separate pool
		// configurations still get credential-sensitive config identities.
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		mapping, err := chainProxy(uri, "")
		if err != nil {
			mapping = map[string]any{"invalid": invalidHopPoolKey(uri)}
		} else {
			delete(mapping, "name")
		}
		keys, unique = append(keys, key), append(unique, uri)
		mappings = append(mappings, mapping)
		entryConfigs = append(entryConfigs, configHash(mapping))
	}
	if len(keys) == 0 {
		return nil
	}
	id := poolIdentity(role, keys)
	configID := configHash(mappings)
	prefix, side := "netivri-", "pre"
	if gen > 0 {
		prefix = fmt.Sprintf("netivri-g%d-", gen)
	}
	if role == postHopPool {
		side = "post"
	}
	spec := &hopPoolSpec{
		role: role, keys: keys, uris: unique, id: id, configID: configID,
		name: prefix + side + "-pool-" + id + "-" + configID,
	}
	return spec
}

func configHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func invalidHopPoolKey(raw string) string {
	canonical := "invalid-hop-pool-key"
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		canonical += "\x00" + strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	}
	sum := sha256.Sum256([]byte(canonical))
	return "invalid-" + hex.EncodeToString(sum[:])
}

func effectivePool(pool []string, exists bool, scalar string) []string {
	if !exists {
		if scalar == "" || isNoChain(scalar) {
			return nil
		}
		return []string{scalar}
	}
	out := make([]string, 0, len(pool))
	for _, uri := range pool {
		if uri != "" && !isNoChain(uri) {
			out = append(out, uri)
		}
	}
	return out
}

func (o Options) nodePrePool(key string, i int) []string {
	if nodes, ok := o.NodePrePool[key]; ok {
		if i >= 0 && i < len(nodes) {
			return effectivePool(nodes[i], true, "")
		}
		return nil
	}
	if route, ok := o.RoutePrePool[key]; ok {
		return effectivePool(route, true, "")
	}
	return effectivePool(nil, false, o.nodePre(key, i))
}

func (o Options) nodePostPool(key string, i int) []string {
	if nodes, ok := o.NodePostPool[key]; ok {
		if i >= 0 && i < len(nodes) {
			return effectivePool(nodes[i], true, "")
		}
		return nil
	}
	if route, ok := o.RoutePostPool[key]; ok {
		return effectivePool(route, true, "")
	}
	return effectivePool(nil, false, o.nodePost(key, i))
}

func validateOptionHopPools(o Options) error {
	validate := func(raw string) error {
		if raw == "" || isNoChain(raw) {
			return nil
		}
		if _, err := chainProxy(raw, "hop-validation"); err != nil {
			return err
		}
		return nil
	}
	// List-valued settings are new configuration and are validated even when
	// they are not selected by a route. Legacy scalar settings are checked only
	// when the historical route cascade makes them effective.
	for _, uri := range o.GlobalPostPool {
		if err := validate(uri); err != nil {
			return err
		}
	}
	if spec := newHopPoolSpec(postHopPool, o.globalPostPool(), 0); o.Node != nil && spec != nil && len(spec.keys) > 1 {
		return nodeInvalid("a global post hop pool cannot be combined with a default node")
	}
	for _, values := range []map[string][]string{o.RoutePrePool, o.RoutePostPool} {
		for _, list := range values {
			for _, raw := range list {
				if err := validate(raw); err != nil {
					return err
				}
			}
		}
	}
	for _, values := range []map[string][][]string{o.NodePrePool, o.NodePostPool} {
		for _, nodes := range values {
			for _, list := range nodes {
				for _, raw := range list {
					if err := validate(raw); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, route := range o.AccountRoute {
		nodes := o.RouteNodes[route]
		if err := checkedNodeHops(o, route, len(nodes)); err != nil {
			return err
		}
		if err := checkedPoolNodeHops(o, route, len(nodes)); err != nil {
			return err
		}
		if len(nodes) == 0 {
			// These route-level scalars are consumed by direct-route decisions
			// and direct-post mapping, as in the legacy chain path.
			for _, raw := range append(o.routePrePool(route), o.routePostPool(route)...) {
				if err := validate(raw); err != nil {
					return err
				}
			}
			continue
		}
		for index := range nodes {
			for _, raw := range append(o.nodePrePool(route, index), o.nodePostPool(route, index)...) {
				if err := validate(raw); err != nil {
					return err
				}
			}
		}
	}
	if len(o.AccountRoute) == 0 || o.Node != nil {
		if o.GlobalPostPool != nil {
			return nil
		}
		return validate(o.GlobalPost)
	}
	return nil
}

func checkedPoolNodeHops(o Options, key string, n int) error {
	if hops, ok := o.NodePrePool[key]; ok && len(hops) != n {
		return nodeInvalid("node pre hop pool count does not match the route")
	}
	if hops, ok := o.NodePostPool[key]; ok && len(hops) != n {
		return nodeInvalid("node post hop pool count does not match the route")
	}
	return nil
}

func addPoolPlaceholder(spec *hopPoolSpec, _ *[]map[string]any, groups *[]map[string]any, _ map[string]struct{}) error {
	if spec == nil || len(spec.keys) < 2 {
		return nil
	}
	*groups = append(*groups, map[string]any{"name": spec.name, "type": "select", "proxies": []string{"DIRECT"}})
	return nil
}

func healthPoolMemberKey(label string, pre, post []string) string {
	keys := func(uris []string) []string {
		out := make([]string, 0, len(uris))
		for _, raw := range uris {
			if key := hopPoolKey(raw); key != "" {
				out = append(out, key)
			}
		}
		return out
	}
	data, _ := json.Marshal(struct {
		Label string
		Pre   []string
		Post  []string
	}{label, keys(pre), keys(post)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
