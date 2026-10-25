package fetch

import (
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// maxQuotaValue rejects absurd quota numbers no provider means.
const maxQuotaValue = 1 << 60

// maxQuotaExpire caps expire at 9999-12-31T23:59:59Z: larger values do not
// round-trip through time.Time JSON/RFC3339 and would break snapshot saving.
const maxQuotaExpire = 253402300799

// ParseQuota parses the subscription-userinfo header, for example
// "upload=123; download=456; total=107374182400; expire=1763500000".
// Every field stands alone: a missing, empty or garbled one is absent
// (the zero value, and expire 0 means no expiry). It returns nil when
// the header is empty or no field survives.
func ParseQuota(header string) *subscription.Quota {
	var q subscription.Quota
	have := false
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 || n > maxQuotaValue {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			q.Upload, have = n, true
		case "download":
			q.Download, have = n, true
		case "total":
			q.Total, have = n, true
		case "expire":
			if n <= 0 || n > maxQuotaExpire {
				continue
			}
			q.Expire, have = time.Unix(n, 0), true
		}
	}
	if !have {
		return nil
	}
	return &q
}
