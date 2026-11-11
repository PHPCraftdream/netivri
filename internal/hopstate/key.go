package hopstate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/hop"
)

// HopKey preserves the internal API while delegating canonicalization to hop.Key.
func HopKey(rawURL string) (string, bool) { return hop.Key(rawURL) }

func validHashKey(key string) bool {
	if len(key) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == sha256.Size && key == strings.ToLower(key)
}
