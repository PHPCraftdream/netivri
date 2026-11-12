package main

import (
	"fmt"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/angel"
)

// hopPoolLine renders a pool compactly, e.g. "post 3 hops: 2 up, 1 down".
// A hop that is being probed is down until the probe succeeds.
func hopPoolLine(p angel.HopPoolInfo) string {
	line := fmt.Sprintf("%s %d hops: %d up", p.Role, p.HopCount, p.ActiveCount)
	if p.QuarantinedCount > 0 {
		line += fmt.Sprintf(", %d down", p.QuarantinedCount)
	}
	return line
}

// defaultRouteLabel names the pools of the global default route (connections
// without an account route) where account pools show a login.
const defaultRouteLabel = "default route"

// hopPoolAllDown reports a pool in which every hop is quarantined.
func hopPoolAllDown(p angel.HopPoolInfo) bool {
	return p.HopCount > 0 && p.QuarantinedCount == p.HopCount
}

// hopPoolSignature identifies one pool across accounts that share its route.
func hopPoolSignature(p angel.HopPoolInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%d", p.Role, p.HopCount)
	for _, h := range p.Hops {
		b.WriteString("/" + h.ID)
	}
	return b.String()
}
