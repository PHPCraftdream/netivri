# hopkit

Reusable Go packages for proxy-hop routing and health:

- `pool`: weighted hop selection, health tracking, quarantine, and probe leases.
- `hop`: canonical secret-free hop identity keys (`hop.Key`).
- `dial`: health-aware dialing and structured failure attribution.
- `check`: hop reachability probing.
- `revive`: bounded recovery-probe scheduling.

Minimal pool usage:

```go
package main

import "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"

func main() {
	health := pool.New([]string{"opaque-hop-key"}, pool.Options{})
	_ = health.Snapshot()
}
```

## Known limits

The vendored SOCKS client does not check or surface REP 2–6. The classifier recognizes explicit error text, including synthetic fixtures; a native `proxyclient` replacement is deferred.

