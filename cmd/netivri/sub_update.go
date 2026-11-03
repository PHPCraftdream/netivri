package main

import (
	"context"
	"errors"
	"flag"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"io"
	"time"
)

func (a *subApp) update(args []string) error {
	fs := flag.NewFlagSet("sub update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	all := fs.Bool("all", false, "update every configured subscription")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub update: %v", err)
	}
	if len(pos) > 1 {
		return cli.Usage("sub update: at most one subscription name")
	}
	if *all && len(pos) == 1 {
		return cli.Usage("sub update: pass a name or --all, not both")
	}
	name := ""
	if len(pos) == 1 {
		name = pos[0]
		if name == "" {
			if _, err := a.load(); err != nil {
				return err
			}
			return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
		}
	}
	rs, err := ProviderRefresh(a.home, name, *all, a.fetch, a.now)
	if err != nil && rs == nil {
		return err
	}
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]any{"name": r.Name, "ok": r.OK, "nodes": r.Nodes, "skipped": r.Skipped, "error": r.Error, "quota": r.Quota})
	}
	cli.Success(a.stdout, map[string]any{"results": out})
	return err
}
func (a *subApp) updateOne(store *subscription.Store, cfg *config.Config, s config.Subscription) map[string]any {
	res := map[string]any{"name": s.Name, "ok": false, "nodes": nil, "skipped": nil, "error": nil, "quota": nil}
	snap, e := fetchSnapshot(context.Background(), a.fetch, a.now, cfg, s)
	if e != nil {
		return withFetchError(res, e)
	}
	if e = store.Save(s.Name, snap); e != nil {
		res["error"] = map[string]string{"code": "INTERNAL", "message": "store subscription: " + e.Error()}
		return res
	}
	res["ok"] = true
	res["nodes"] = len(snap.Nodes)
	res["skipped"] = snap.Skipped
	res["quota"] = quotaView(snap.Quota)
	return res
}
func fetchSnapshot(ctx context.Context, get func(context.Context, string, fetch.Options) (*fetch.Result, error), now func() time.Time, cfg *config.Config, s config.Subscription) (subscription.Snapshot, error) {
	res, e := get(ctx, s.URL, fetch.Options{Proxy: proxyFor(s.Via, cfg.Fetch.Proxy)})
	if e != nil {
		return subscription.Snapshot{}, e
	}
	parsed, e := subscription.ParseDetailed(res.Body)
	if e != nil {
		return subscription.Snapshot{}, e
	}
	return subscription.Snapshot{FetchedAt: now().UTC(), Nodes: parsed.Nodes, Skipped: parsed.Skipped.Banner + parsed.Skipped.Invalid, Quota: fetch.ParseQuota(res.Header.Get("subscription-userinfo"))}, nil
}
func withFetchError(res map[string]any, err error) map[string]any {
	var ce *cli.Error
	if !errors.As(err, &ce) {
		ce = &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	res["error"] = map[string]string{"code": ce.Code, "message": ce.Message}
	return res
}
