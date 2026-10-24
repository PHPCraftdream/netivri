package config

import "strings"

// Longest account lists: with ValidProviderName's 64 characters a route key
// stays far below the core's key limit.
const (
	MaxAccountProviders = 16
	MaxAccountCountries = 32
)

// ParseProviderList parses an account's provider list: names separated by
// commas, blanks around a name ignored, the first of a repeated name kept.
// An empty string is the empty list. ok is false for an empty item, an invalid
// name or too many names.
func ParseProviderList(s string) (names []string, ok bool) {
	return parseList(s, MaxAccountProviders, func(item string) (string, bool) {
		return item, ValidProviderName(item)
	})
}

// ParseCountryList parses an account's exit-country list: two-letter codes
// separated by commas, upper-cased, with the same rules as ParseProviderList.
func ParseCountryList(s string) (codes []string, ok bool) {
	return parseList(s, MaxAccountCountries, NormalizeCountry)
}

// JoinList renders a list in the canonical stored form: items joined by a
// bare comma.
func JoinList(items []string) string { return strings.Join(items, ",") }

func parseList(s string, max int, norm func(string) (string, bool)) ([]string, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, true
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(s, ",") {
		item, ok := norm(strings.TrimSpace(raw))
		if !ok {
			return nil, false
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	if len(out) > max {
		return nil, false
	}
	return out, true
}
