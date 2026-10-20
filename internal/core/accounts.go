package core

import (
	"fmt"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

const accountGroupPrefix = "netivri-group-"

// prepareRoutes builds independent Mihomo routing mappings without mutating
// the caller's node maps or country slices.
func prepareRoutes(o Options) ([]map[string]any, []map[string]any, []string, error) {
	if len(o.AccountCountry) == 0 {
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

	logins := make([]string, 0, len(o.AccountCountry))
	countries := make(map[string]struct{})
	for login, country := range o.AccountCountry {
		if !validAccountLogin(login) {
			return nil, nil, nil, nodeInvalid("invalid account login")
		}
		if !validCountry(country) {
			return nil, nil, nil, nodeInvalid("invalid account country")
		}
		logins = append(logins, login)
		countries[country] = struct{}{}
	}
	sort.Strings(logins)

	countryCodes := make([]string, 0, len(countries))
	for country := range countries {
		countryCodes = append(countryCodes, country)
	}
	sort.Strings(countryCodes)

	var proxies []map[string]any
	if defaultProxy != nil {
		proxies = append(proxies, defaultProxy)
	}
	groups := make([]map[string]any, 0, len(countryCodes))
	for _, country := range countryCodes {
		nodes := o.Countries[country]
		if len(nodes) == 0 {
			return nil, nil, nil, &cli.Error{
				Code:    "NO_NODES_FOR_COUNTRY",
				Message: "no nodes are available for an account country",
				Exit:    cli.ExitNotFound,
			}
		}

		members := make([]string, 0, len(nodes))
		for index, node := range nodes {
			prepared, _, err := prepareNode(node)
			if err != nil {
				return nil, nil, nil, err
			}
			name := fmt.Sprintf("netivri-%s-%d", country, index)
			prepared[0]["name"] = name
			proxies = append(proxies, prepared[0])
			members = append(members, name)
		}
		groups = append(groups, map[string]any{
			"name":    accountGroupPrefix + country,
			"type":    "select",
			"proxies": members,
		})
	}

	rules := make([]string, 0, len(logins)+1)
	for _, login := range logins {
		rules = append(rules, "IN-USER,"+login+","+accountGroupPrefix+o.AccountCountry[login])
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

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' &&
		country[1] >= 'A' && country[1] <= 'Z'
}
