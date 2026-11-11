package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func (a *subApp) list() error {
	r, e := ProviderList(a.home)
	if e != nil {
		return e
	}
	ss := make([]map[string]any, 0, len(r.Providers))
	for _, p := range r.Providers {
		item := map[string]any{"name": p.Name, "host": p.Host, "via_set": p.ViaSet, "nodes": p.Nodes, "fetched_at": p.FetchedAt, "quota": p.Quota, "pre_socks5": p.Pre, "post_socks5": p.Post}
		ss = append(ss, item)
	}
	cli.Success(a.stdout, map[string]any{"subscriptions": ss})
	return nil
}
func quotaView(q *subscription.Quota) map[string]any {
	if q == nil {
		return nil
	}
	v := map[string]any{"used": q.Upload + q.Download, "total": nil, "expire": nil}
	if q.Total > 0 {
		v["total"] = q.Total
	}
	if !q.Expire.IsZero() {
		v["expire"] = q.Expire.UTC().Format(time.RFC3339)
	}
	return v
}
func snapshotError(name string) *cli.Error {
	return &cli.Error{Code: "SUB_SNAPSHOT_INVALID", Message: fmt.Sprintf("the stored snapshot of subscription %q cannot be read", name), Hint: "run `netivri sub update` to fetch it again", Exit: cli.ExitInternal}
}
func urlHost(raw string) string {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return ""
	}
	return u.Hostname()
}
