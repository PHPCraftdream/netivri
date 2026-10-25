package config

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Limits of a listen.allowed_clients / accounts[].allowed_clients value.
const (
	MaxAllowedClientRules = 64
	MaxAllowedClientBytes = 1024
)

// AllowedClientsAny is the rule that stands for every address.
const AllowedClientsAny = "*"

var (
	allowAllPrefixes       = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
	loopbackClientPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
)

// AllowedClients is a parsed allowed-clients value: the prefixes a client
// address must match. nil allows every address.
type AllowedClients []netip.Prefix

// ParseAllowedClients parses an allowed-clients value: comma-separated rules,
// blanks around a comma ignored. An empty value allows every address. Rules:
// "*" (everywhere in the list), "loopback" (127.0.0.0/8 and ::1, case
// insensitive), an exact IPv4/IPv6 address, an IPv4 whole-octet mask
// ("192.168.1.*", "192.168.*", "10.*") and an IPv4/IPv6 CIDR prefix. IPv6 has
// no "*" masks; an IPv6 item containing one is an error pointing to CIDR.
// A "*" behind a non-"*" octet ("192.*.1.5") and a partial-octet wildcard
// ("19*") are errors. At most MaxAllowedClientRules rules and
// MaxAllowedClientBytes bytes in total; an empty item is an error.
func ParseAllowedClients(s string) (AllowedClients, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if len(s) > MaxAllowedClientBytes {
		return nil, fmt.Errorf("the value is longer than %d bytes", MaxAllowedClientBytes)
	}
	items := strings.Split(s, ",")
	if len(items) > MaxAllowedClientRules {
		return nil, fmt.Errorf("more than %d rules", MaxAllowedClientRules)
	}
	allowAll := false
	rules := make(AllowedClients, 0, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("empty rule at position %d", i+1)
		}
		if item == AllowedClientsAny {
			allowAll = true
			continue
		}
		if strings.EqualFold(item, "loopback") {
			rules = append(rules, loopbackClientPrefixes...)
			continue
		}
		if strings.Contains(item, "*") && !strings.Contains(item, ":") {
			p, err := parseIPv4Mask(item)
			if err != nil {
				return nil, err
			}
			rules = append(rules, p)
			continue
		}
		if strings.Contains(item, ":") {
			if strings.Contains(item, "*") {
				return nil, fmt.Errorf("IPv6 rules use CIDR notation (for example fd00::/8); %q has no \"*\" masks", item)
			}
			p, err := netip.ParsePrefix(item)
			if err != nil {
				if _, err2 := netip.ParseAddr(item); err2 != nil {
					return nil, fmt.Errorf("invalid rule %q", item)
				}
				// An exact address is a single-address prefix; Unmap first
				// so ::ffff:127.0.0.1 keeps the IPv4 /32 length.
				mapped, _ := netip.ParseAddr(item)
				un := mapped.Unmap()
				rules = append(rules, netip.PrefixFrom(un, un.BitLen()))
				continue
			}
			rules = append(rules, p.Masked())
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			if _, err2 := netip.ParseAddr(item); err2 != nil {
				return nil, fmt.Errorf("invalid rule %q", item)
			}
			// An exact address is a single-address prefix; Unmap first
			// so ::ffff:127.0.0.1 keeps the IPv4 /32 length.
			mapped, _ := netip.ParseAddr(item)
			un := mapped.Unmap()
			rules = append(rules, netip.PrefixFrom(un, un.BitLen()))
			continue
		}
		rules = append(rules, p.Masked())
	}
	// Fail closed: a non-empty value must never yield an empty set.
	if !allowAll && len(rules) == 0 {
		return nil, fmt.Errorf("no valid rule in %q", s)
	}
	if allowAll {
		return allowAllPrefixes, nil
	}
	return rules, nil
}

// parseIPv4Mask parses a whole-octet mask such as "192.168.1.*", "192.168.*"
// or "10.*": every octet is 1-3 ASCII digits <= 255 or "*", and a "*" may
// appear only in trailing positions.
func parseIPv4Mask(item string) (netip.Prefix, error) {
	parts := strings.Split(item, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return netip.Prefix{}, fmt.Errorf("invalid rule %q", item)
	}
	// Validate first, then count the leading non-"*" octets: the mask
	// length is the whole leading part, not the last octet seen.
	for i, part := range parts {
		if part == "*" {
			if i+1 < len(parts) && parts[i+1] != "*" {
				return netip.Prefix{}, fmt.Errorf("invalid rule %q", item)
			}
			continue
		}
		if len(part) < 1 || len(part) > 3 {
			return netip.Prefix{}, fmt.Errorf("invalid rule %q", item)
		}
		v := 0
		for j := 0; j < len(part); j++ {
			if part[j] < '0' || part[j] > '9' {
				return netip.Prefix{}, fmt.Errorf("invalid rule %q", item)
			}
			v = v*10 + int(part[j]-'0')
		}
		if v > 255 {
			return netip.Prefix{}, fmt.Errorf("invalid rule %q", item)
		}
	}
	var b [4]byte
	for i, part := range parts {
		if part == "*" {
			// Octets after the first "*" stay zero.
			continue
		}
		v := 0
		for j := 0; j < len(part); j++ {
			v = v*10 + int(part[j]-'0')
		}
		b[i] = byte(v)
	}
	n := 0
	for n < len(parts) && parts[n] != "*" {
		n++
	}
	return netip.PrefixFrom(netip.AddrFrom4(b), 8*n), nil
}

// Allows reports whether addr matches one of the rules. Only nil means
// "no rule": every address is allowed. A non-nil empty set is a
// restrictive list that yielded nothing and denies every address (the
// parser cannot produce one — its fail-closed guard errors — this is
// defense in depth). IPv4-mapped IPv6 addresses are unmapped first.
func (a AllowedClients) Allows(addr netip.Addr) bool {
	if a == nil {
		return true // no rule
	}
	if len(a) == 0 {
		return false // a restrictive list that yielded nothing denies everyone
	}
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range a {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// AllowsAddr is Allows for a net.Addr (the TCP peer address). An address that
// is not an IP address matches nothing.
func (a AllowedClients) AllowsAddr(addr net.Addr) bool {
	if a == nil {
		return true // no rule: the kind of address does not matter
	}
	ta, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(ta.IP)
	if !ok {
		return false
	}
	return a.Allows(ip)
}

// Prefixes returns the parsed prefixes (nil means allow all).
func (a AllowedClients) Prefixes() []netip.Prefix { return a }

// NormalizeAllowedClients renders the value for display and fingerprints:
// items trimmed and joined by a bare comma, an empty value as "*".
func NormalizeAllowedClients(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return AllowedClientsAny
	}
	items := strings.Split(s, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return strings.Join(items, ",")
}

// AllowedClientPrefixes parses an already validated value; nil on error.
// nil is also "allow all"; a non-nil empty slice never comes back: the
// fail-closed guard in ParseAllowedClients rejects a non-empty value that
// yields no rule, and an empty value yields nil.
func AllowedClientPrefixes(s string) []netip.Prefix {
	rules, err := ParseAllowedClients(s)
	if err != nil {
		return nil
	}
	return rules
}
