package core

import (
	"net/netip"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// siteKey reduces a connection destination to the site it belongs to: the
// registrable domain (public-suffix aware) of the host, or the textual IP
// for IP literals and for an empty host with a resolved destination IP.
// A host that is itself a public suffix, or has no dot, is used as is.
func siteKey(host string, ip netip.Addr) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		if ip.IsValid() {
			return ip.String()
		}
		return ""
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String()
	}
	if site, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return site
	}
	return host
}
