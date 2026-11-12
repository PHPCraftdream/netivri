package hopstate

import "github.com/PHPCraftdream/netivri/pkg/hopkit/hop"

// HopKey preserves the internal API while delegating canonicalization to hop.Key.
func HopKey(rawURL string) (string, bool) { return hop.Key(rawURL) }

func validHashKey(key string) bool { return hop.ValidKey(key) }
