package inbound

import (
	"net"
	"net/netip"
	"sync/atomic"

	C "github.com/metacubex/mihomo/constant"
)

// The prefix lists are read by the accept loops while netivri sets and
// resets them when an instance starts or closes, so they are atomic
// pointers (netivri patch 4).
var lanAllowedIPs atomic.Pointer[[]netip.Prefix]
var lanDisAllowedIPs atomic.Pointer[[]netip.Prefix]

func SetAllowedIPs(prefixes []netip.Prefix) {
	lanAllowedIPs.Store(&prefixes)
}

func SetDisAllowedIPs(prefixes []netip.Prefix) {
	lanDisAllowedIPs.Store(&prefixes)
}

func AllowedIPs() []netip.Prefix {
	if p := lanAllowedIPs.Load(); p != nil {
		return *p
	}
	return nil
}

func DisAllowedIPs() []netip.Prefix {
	if p := lanDisAllowedIPs.Load(); p != nil {
		return *p
	}
	return nil
}

func IsRemoteAddrDisAllowed(addr net.Addr) bool {
	m := C.Metadata{}
	if err := m.SetRemoteAddr(addr); err != nil {
		return false
	}
	ipAddr := m.AddrPort().Addr()
	if ipAddr.IsValid() {
		return isAllowed(ipAddr) && !isDisAllowed(ipAddr)
	}
	return false
}

func isAllowed(addr netip.Addr) bool {
	return prefixesContains(AllowedIPs(), addr)
}

func isDisAllowed(addr netip.Addr) bool {
	return prefixesContains(DisAllowedIPs(), addr)
}
