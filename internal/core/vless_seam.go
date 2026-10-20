package core

import (
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

// vlessAdapter unwraps a proxy to its VLESS adapter, through the auto-close
// wrapper that adapter.ParseProxy adds.
func vlessAdapter(p C.Proxy) (*outbound.Vless, bool) {
	ap, ok := p.(*adapter.Proxy)
	if !ok {
		return nil, false
	}
	return outbound.UnwrapVless(ap.ProxyAdapter)
}
