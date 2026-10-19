package config

import (
	"reflect"
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
// Errors from the ktav wrappers (KTAV_LIB_MISSING) propagate unchanged.
func Marshal(cfg *Config) (string, error) {
	if cfg == nil {
		return "", invalid("configuration must not be nil")
	}
	body, err := ktavDumps(cfg)
	if err != nil {
		// Binding rendering errors get the same structured-fields-only
		// hygiene as parse errors; guard errors stay unchanged.
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

// configEqual compares two configurations, treating a nil slice of
// subscriptions and an empty non-nil slice as equal: omitempty renders
// both the same way, so neither state can survive a rewrite.
func configEqual(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	if len(x.Subscriptions) == 0 {
		x.Subscriptions = nil
	}
	if len(y.Subscriptions) == 0 {
		y.Subscriptions = nil
	}
	return reflect.DeepEqual(x, y)
}
