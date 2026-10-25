package fetch

import (
	"reflect"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestParseQuota(t *testing.T) {
	expire := time.Unix(1763500000, 0)
	cases := []struct {
		name   string
		header string
		want   *subscription.Quota
	}{
		{"full header", "upload=123; download=456; total=107374182400; expire=1763500000",
			&subscription.Quota{Upload: 123, Download: 456, Total: 107374182400, Expire: expire}},
		{"missing header", "", nil},
		{"only some keys", "total=107374182400", &subscription.Quota{Total: 107374182400}},
		{"garbled integers", "upload=abc; total=1", &subscription.Quota{Total: 1}},
		{"negative upload", "upload=-5; download=7", &subscription.Quota{Download: 7}},
		{"negative expire", "expire=-1", nil},
		{"huge total", "total=1152921504606846977", nil},
		{"zero expire", "upload=1; expire=0", &subscription.Quota{Upload: 1}},
		{"expire at the cap kept", "upload=1; expire=253402300799",
			&subscription.Quota{Upload: 1, Expire: time.Unix(253402300799, 0)}},
		{"expire past the cap dropped", "upload=1; expire=253402300800", &subscription.Quota{Upload: 1}},
		{"unknown key ignored", "comment=hello; upload=9", &subscription.Quota{Upload: 9}},
		{"whitespace variants", "  upload = 8 ;\ttotal=16\n; expire =1763500000 ",
			&subscription.Quota{Upload: 8, Total: 16, Expire: expire}},
		{"empty value", "upload=; total=2", &subscription.Quota{Total: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQuota(tc.header)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ParseQuota(%q) = %+v, want nil", tc.header, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseQuota(%q) = nil, want %+v", tc.header, tc.want)
			}
			if !got.Expire.Equal(tc.want.Expire) {
				t.Errorf("Expire = %v, want %v", got.Expire, tc.want.Expire)
			}
			got.Expire, tc.want.Expire = time.Time{}, time.Time{}
			if !reflect.DeepEqual(*got, *tc.want) {
				t.Errorf("ParseQuota(%q) = %+v, want %+v", tc.header, *got, *tc.want)
			}
		})
	}
}
