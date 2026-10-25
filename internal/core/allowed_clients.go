package core

import (
	"net/netip"

	"github.com/metacubex/mihomo/adapter/inbound"
)

// allowAllIPs is the process-global filter state that allows every client
// address (Mihomo's default).
var allowAllIPs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

// resetAllowedClients restores the allow-all filter state so a later
// instance or test never inherits a closed rule.
func resetAllowedClients() { inbound.SetAllowedIPs(allowAllIPs) }
