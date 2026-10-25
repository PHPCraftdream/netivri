package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// update implements `sub update [N | --all]`: re-fetch and re-store.
// With a name only that subscription (SUB_NOT_FOUND when unknown); with
// --all or no argument all subscriptions in config order. A failed
// subscription keeps its old stored snapshot. When any subscription
// fails, the results object is still printed on stdout and the command
// ends with SUB_UPDATE_FAILED (exit 3).
func (a *subApp) update(args []string) error {
	fs := flag.NewFlagSet("sub update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	allFlag := fs.Bool("all", false, "update every configured subscription")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub update: %v", err)
	}
	if len(pos) > 1 {
		return cli.Usage("sub update: at most one subscription name")
	}
	if *allFlag && len(pos) == 1 {
		return cli.Usage("sub update: pass a name or --all, not both")
	}

	cfg, err := a.load()
	if err != nil {
		return err
	}
	var targets []config.Subscription
	if len(pos) == 1 {
		for _, s := range cfg.Subscriptions {
			if s.Name == pos[0] {
				targets = append(targets, s)
				break
			}
		}
		if targets == nil {
			return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
		}
	} else {
		targets = cfg.Subscriptions
	}

	store := a.store()
	results := make([]map[string]any, 0, len(targets))
	failed := false
	for _, s := range targets {
		r := a.updateOne(store, cfg, s)
		if r["ok"] != true {
			failed = true
		}
		results = append(results, r)
	}
	cli.Success(a.stdout, map[string]any{"results": results})
	if failed {
		return &cli.Error{
			Code:    codeSubUpdateFailed,
			Message: "one or more subscriptions failed to update",
			Exit:    cli.ExitNetwork,
		}
	}
	return nil
}

// updateOne fetches, parses and stores one subscription and reports the
// per-subscription result. On failure the stored snapshot is untouched.
func (a *subApp) updateOne(store *subscription.Store, cfg *config.Config, s config.Subscription) map[string]any {
	res := map[string]any{"name": s.Name, "ok": false, "nodes": nil, "skipped": nil, "error": nil}
	res["quota"] = nil
	snap, err := fetchSnapshot(context.Background(), a.fetch, a.now, cfg, s)
	if err != nil {
		return withFetchError(res, err)
	}
	if err := store.Save(s.Name, snap); err != nil {
		res["error"] = map[string]string{"code": "INTERNAL", "message": "store subscription: " + err.Error()}
		return res
	}
	res["ok"] = true
	res["nodes"] = len(snap.Nodes)
	res["skipped"] = snap.Skipped
	res["quota"] = quotaView(snap.Quota)
	return res
}

// fetchSnapshot fetches and parses one subscription through its proxy. It
// stores nothing; `sub update` and the angel's refresh share it.
func fetchSnapshot(
	ctx context.Context,
	get func(ctx context.Context, rawURL string, o fetch.Options) (*fetch.Result, error),
	now func() time.Time,
	cfg *config.Config,
	s config.Subscription,
) (subscription.Snapshot, error) {
	res, err := get(ctx, s.URL, fetch.Options{Proxy: proxyFor(s.Via, cfg.Fetch.Proxy)})
	if err != nil {
		return subscription.Snapshot{}, err
	}
	parsed, err := subscription.ParseDetailed(res.Body)
	if err != nil {
		return subscription.Snapshot{}, err
	}
	return subscription.Snapshot{
		FetchedAt: now().UTC(),
		Nodes:     parsed.Nodes,
		Skipped:   parsed.Skipped.Banner + parsed.Skipped.Invalid,
		Quota:     fetch.ParseQuota(res.Header.Get("subscription-userinfo")),
	}, nil
}

func withFetchError(res map[string]any, err error) map[string]any {
	var ce *cli.Error
	if !errors.As(err, &ce) {
		ce = &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	res["error"] = map[string]string{"code": ce.Code, "message": ce.Message}
	return res
}
