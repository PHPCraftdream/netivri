package config

import (
	"reflect"
	"strings"
)

// managedHeader is prepended to every rendered configuration. The
// whole file is regenerated on change, so hand-written comments do not
// survive (Ktav comments occupy a whole line, spec section 3.4).
const managedHeader = "## Managed by netivri: this file is rewritten on change and comments are not preserved.\n" +
	"## Keep personal notes in a separate file.\n"

// Marshal renders the whole configuration with the bindings Dumps
// (struct fields only, so key order is deterministic) and a header
// comment. Before returning, the produced text is parsed back with the
// guarded strict loader onto Default() and must be deeply equal to the
// input: netivri never writes a document that does not round-trip.
func Marshal(cfg *Config) (string, error) {
	if cfg == nil {
		return "", invalid("configuration must not be nil")
	}
	// Render a shallow copy with the default listen.allowed_clients
	// omitted, like other defaults: an absent key decodes back to "*".
	render := *cfg
	if render.Listen.AllowedClients == AllowedClientsAny {
		render.Listen.AllowedClients = ""
	}
	var body string
	var err error
	if len(render.Servers) == 0 {
		if render.Chain == (Chain{}) {
			// No chain proxies: render exactly as before the chain existed, so a
			// rewrite adds no empty chain table to an existing file.
			body, err = ktavDumps(&configWithoutChain{
				Listen: render.Listen, Auth: render.Auth, Fetch: render.Fetch,
				Subscriptions: render.Subscriptions, Accounts: render.Accounts, Selection: render.Selection,
			})
		} else {
			body, err = ktavDumps(&render)
		}
	} else {
		wire := renderServers(&render)
		if render.Chain == (Chain{}) {
			body, err = ktavDumps(&configServersWithoutChain{
				Listen: render.Listen, Auth: render.Auth, Fetch: render.Fetch,
				Subscriptions: render.Subscriptions, Accounts: render.Accounts, Servers: wire, Selection: render.Selection,
			})
		} else {
			body, err = ktavDumps(&configServersWithChain{
				Listen: render.Listen, Auth: render.Auth, Fetch: render.Fetch, Chain: render.Chain,
				Subscriptions: render.Subscriptions, Accounts: render.Accounts, Servers: wire, Selection: render.Selection,
			})
		}
	}
	// Binding rendering errors get the same structured-fields-only
	// hygiene as parse errors.
	if err != nil {
		return "", configFromBindingError(err)
	}
	doc := managedHeader + body
	if err := ktavLoadsStrict(doc); err != nil {
		return "", configFromBindingError(err)
	}
	check, err := ktavLoadsIntoConfig(doc)
	if err != nil {
		return "", err
	}
	if !configEqual(check, cfg) {
		return "", invalid("the rendered configuration does not round-trip")
	}
	return doc, nil
}

// configWithoutChain is Config without the chain table, in the same key order.
type configWithoutChain struct {
	Listen        Listen         `json:"listen"`
	Auth          Auth           `json:"auth"`
	Fetch         Fetch          `json:"fetch"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	Accounts      []Account      `json:"accounts,omitempty"`
	Selection     Selection      `json:"selection"`
}
type configServersWithoutChain struct {
	Listen        Listen           `json:"listen"`
	Auth          Auth             `json:"auth"`
	Fetch         Fetch            `json:"fetch"`
	Subscriptions []Subscription   `json:"subscriptions,omitempty"`
	Accounts      []Account        `json:"accounts,omitempty"`
	Servers       []map[string]any `json:"servers,omitempty"`
	Selection     Selection        `json:"selection"`
}
type configServersWithChain struct {
	Listen        Listen           `json:"listen"`
	Auth          Auth             `json:"auth"`
	Fetch         Fetch            `json:"fetch"`
	Chain         Chain            `json:"chain"`
	Subscriptions []Subscription   `json:"subscriptions,omitempty"`
	Accounts      []Account        `json:"accounts,omitempty"`
	Servers       []map[string]any `json:"servers,omitempty"`
	Selection     Selection        `json:"selection"`
}

func renderServers(cfg *Config) []map[string]any {
	out := make([]map[string]any, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		m := map[string]any{"name": s.Name, "protocol": s.Protocol, "port": s.Port, "accounts": s.Accounts}
		if s.Default {
			m["default"] = true
		}
		if s.Bind != "" && s.Bind != "0.0.0.0" {
			m["bind"] = s.Bind
		}
		if s.PublicHost != "" {
			m["public_host"] = s.PublicHost
		}
		for k, v := range s.Params {
			if _, reserved := m[k]; reserved {
				continue
			}
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

func cloneStringParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	cloned := make(map[string]string, len(params))
	for k, v := range params {
		cloned[k] = v
	}
	return cloned
}

// configEqual compares two configurations, treating nil and empty
// subscriptions, accounts, servers, and server parameter maps as equivalent
// when omitempty renders them identically.
func configEqual(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.Servers = append([]Server(nil), a.Servers...)
	y.Servers = append([]Server(nil), b.Servers...)
	for i := range x.Servers {
		x.Servers[i].Params = cloneStringParams(x.Servers[i].Params)
	}
	for i := range y.Servers {
		y.Servers[i].Params = cloneStringParams(y.Servers[i].Params)
	}
	if len(x.Accounts) == 0 {
		x.Accounts = nil
	}
	if len(y.Accounts) == 0 {
		y.Accounts = nil
	}
	if len(x.Subscriptions) == 0 {
		x.Subscriptions = nil
	}
	if len(y.Subscriptions) == 0 {
		y.Subscriptions = nil
	}
	if len(x.Servers) == 0 {
		x.Servers = nil
	}
	if len(y.Servers) == 0 {
		y.Servers = nil
	}
	if len(x.Servers) != len(y.Servers) {
		return false
	}
	for i := range x.Servers {
		if x.Servers[i].Default != y.Servers[i].Default {
			return false
		}
		if x.Servers[i].Bind == "" {
			x.Servers[i].Bind = "0.0.0.0"
		}
		if y.Servers[i].Bind == "" {
			y.Servers[i].Bind = "0.0.0.0"
		}
		if len(x.Servers[i].Params) == 0 {
			x.Servers[i].Params = nil
		}
		if len(y.Servers[i].Params) == 0 {
			y.Servers[i].Params = nil
		}
		if !reflect.DeepEqual(x.Servers[i], y.Servers[i]) {
			return false
		}
	}
	// An empty listen.allowed_clients means the default "*": normalize
	// both sides so a programmatically built "" and an omitted key are
	// one state.
	if strings.TrimSpace(x.Listen.AllowedClients) == "" {
		x.Listen.AllowedClients = AllowedClientsAny
	}
	if strings.TrimSpace(y.Listen.AllowedClients) == "" {
		y.Listen.AllowedClients = AllowedClientsAny
	}
	// Empty chain fields and the sentinel both represent the same disabled state.
	if strings.EqualFold(x.Chain.PreSOCKS5, "none") {
		x.Chain.PreSOCKS5 = ""
	}
	if strings.EqualFold(y.Chain.PreSOCKS5, "none") {
		y.Chain.PreSOCKS5 = ""
	}
	if strings.EqualFold(x.Chain.PostSOCKS5, "none") {
		x.Chain.PostSOCKS5 = ""
	}
	if strings.EqualFold(y.Chain.PostSOCKS5, "none") {
		y.Chain.PostSOCKS5 = ""
	}
	return reflect.DeepEqual(x, y)
}
