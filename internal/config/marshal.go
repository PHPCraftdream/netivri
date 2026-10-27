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
	if err != nil {
		// Binding rendering errors get the same structured-fields-only
		// hygiene as parse errors.
		return "", configFromBindingError(err)
	}
	doc := managedHeader + body
	check := Default()
	if err := ktavLoadsStrict(doc); err != nil {
		return "", configFromBindingError(err)
	}
	if err := ktavLoadsInto(doc, check); err != nil {
		return "", configFromBindingError(err)
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

// configEqual compares two configurations, treating nil and empty
// subscriptions and accounts slices as equal: omitempty renders both
// states the same way, so neither state can survive a rewrite.
func configEqual(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
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
