//go:build e2e

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

// TestE2ESubQuota runs `sub update` and `sub list` against a provider that
// sends a subscription-userinfo header and checks the reported quota.
func TestE2ESubQuota(t *testing.T) {
	home := t.TempDir()
	const subBody = `proxies:
  - {name: "Frankfurt 01", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=100; download=23; total=1000; expire=1800000000")
		_, _ = w.Write([]byte(subBody))
	}))
	t.Cleanup(srv.Close)

	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: srv.URL}}
	})

	code, out, stderr := accountsRun(t, home, "sub", "update", "main")
	if code != 0 {
		t.Fatalf("sub update: %s%s", out, stderr)
	}
	var upd struct {
		Data struct {
			Results []struct {
				Name  string `json:"name"`
				OK    bool   `json:"ok"`
				Quota *struct {
					Used   int64   `json:"used"`
					Total  *int64  `json:"total"`
					Expire *string `json:"expire"`
				} `json:"quota"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &upd); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(upd.Data.Results) != 1 || !upd.Data.Results[0].OK {
		t.Fatalf("update results = %s", out)
	}
	wantUsed, wantTotal := int64(123), int64(1000)
	wantExpire := "2027-01-15T08:00:00Z"
	if upd.Data.Results[0].Quota == nil {
		t.Fatalf("update result has no quota (feature missing?) %s", out)
	}
	q := *upd.Data.Results[0].Quota
	if q.Used != wantUsed || q.Total == nil || *q.Total != wantTotal || q.Expire == nil || *q.Expire != wantExpire {
		t.Errorf("update quota = %+v, want used 123 total 1000 expire %s", q, wantExpire)
	}

	code, out, stderr = accountsRun(t, home, "sub", "list")
	if code != 0 {
		t.Fatalf("sub list: %s%s", out, stderr)
	}
	var list struct {
		Data struct {
			Subscriptions []struct {
				Name  string `json:"name"`
				Nodes *int   `json:"nodes"`
				Quota *struct {
					Used   int64   `json:"used"`
					Total  *int64  `json:"total"`
					Expire *string `json:"expire"`
				} `json:"quota"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(list.Data.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %s", out)
	}
	sub := list.Data.Subscriptions[0]
	if sub.Name != "main" || sub.Nodes == nil || *sub.Nodes != 1 {
		t.Fatalf("list entry = %s", out)
	}
	if sub.Quota == nil {
		t.Fatalf("list entry has no quota (feature missing?) %s", out)
	}
	if q := *sub.Quota; q.Used != wantUsed || q.Total == nil || *q.Total != wantTotal || q.Expire == nil || *q.Expire != wantExpire {
		t.Errorf("list quota = %+v, want used 123 total 1000 expire %s", q, wantExpire)
	}
	assertNoSecrets(t, out, stderr, srv.URL)
}
