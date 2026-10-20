package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// countries implements `sub countries [NAME | --url URL [--via PROXY]]`.
//
// Three forms:
//   - NAME: the stored snapshot, no network.
//   - no argument: every configured provider plus the summed union.
//   - --url URL [--via PROXY]: fetch and parse only, nothing is stored and
//     the config is untouched. The URL and the proxy are never printed.
//
// A provider without a stored snapshot is reported with null fields,
// mirroring `sub list`; a snapshot that exists but cannot be read is
// SUB_SNAPSHOT_INVALID.
func (a *subApp) countries(args []string) error {
	fs := flag.NewFlagSet("sub countries", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	urlFlag := fs.String("url", "", "count the countries of this subscription URL without storing it")
	viaFlag := fs.String("via", "", "fetch --url through this proxy URL")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub countries: %v", err)
	}
	urlSet, viaSet := false, false
	fs.Visit(func(f *flag.Flag) {
		urlSet = urlSet || f.Name == "url"
		viaSet = viaSet || f.Name == "via"
	})
	switch {
	case urlSet && *urlFlag == "":
		return cli.Usage("sub countries: --url needs a value")
	case viaSet && !urlSet:
		return cli.Usage("sub countries: --via applies to --url only")
	case urlSet && len(pos) > 0:
		return cli.Usage("sub countries: pass a NAME or --url, not both")
	case urlSet:
		return a.countriesURL(*urlFlag, *viaFlag)
	case len(pos) > 1:
		return cli.Usage("sub countries: at most one subscription name")
	case len(pos) == 1:
		return a.countriesName(pos[0])
	default:
		return a.countriesAll()
	}
}

// countriesURL fetches and parses a subscription URL without storing
// anything. It goes through the same strict path as `sub add`: the --via
// proxy or fetch.proxy, no direct fallback.
func (a *subApp) countriesURL(rawURL, via string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	res, err := a.fetch(context.Background(), rawURL, fetch.Options{Proxy: proxyFor(via, cfg.Fetch.Proxy)})
	if err != nil {
		return err
	}
	parsed, err := subscription.ParseDetailed(res.Body)
	if err != nil {
		return err
	}
	counts, unknown := subscription.CountryCounts(parsed.Nodes)
	cli.Success(a.stdout, map[string]any{
		"nodes":     len(parsed.Nodes),
		"countries": counts,
		"unknown":   unknown,
	})
	return nil
}

// snapshotEntry renders one provider's country summary under nameKey; a
// missing stored snapshot keeps every value null, like `sub list`.
func snapshotEntry(nameKey, name string, snap *subscription.Snapshot, err error) (map[string]any, error) {
	entry := map[string]any{nameKey: name, "nodes": nil, "countries": nil, "unknown": nil}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return entry, nil
		}
		return nil, snapshotError(name)
	}
	counts, unknown := subscription.CountryCounts(snap.Nodes)
	entry["nodes"] = len(snap.Nodes)
	entry["countries"] = counts
	entry["unknown"] = unknown
	return entry, nil
}

// countriesName prints the country summary of one configured provider.
func (a *subApp) countriesName(name string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	found := false
	for _, s := range cfg.Subscriptions {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	snap, err := a.store().Load(name)
	entry, err := snapshotEntry("provider", name, snap, err)
	if err != nil {
		return err
	}
	cli.Success(a.stdout, entry)
	return nil
}

// countriesAll prints every configured provider plus the summed union of
// their stored snapshots, in config order.
func (a *subApp) countriesAll() error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	store := a.store()
	providers := make([]map[string]any, 0, len(cfg.Subscriptions))
	union := make(map[string]int)
	for _, s := range cfg.Subscriptions {
		snap, err := store.Load(s.Name)
		entry, err := snapshotEntry("name", s.Name, snap, err)
		if err != nil {
			return err
		}
		if counts, ok := entry["countries"].([]subscription.CountryCount); ok {
			for _, c := range counts {
				union[c.Code] += c.Nodes
			}
		}
		providers = append(providers, entry)
	}
	countries := make([]subscription.CountryCount, 0, len(union))
	for code, num := range union {
		countries = append(countries, subscription.CountryCount{Code: code, Nodes: num})
	}
	sortCountryCounts(countries)
	cli.Success(a.stdout, map[string]any{"providers": providers, "countries": countries})
	return nil
}

// sortCountryCounts orders counts by country code.
func sortCountryCounts(counts []subscription.CountryCount) {
	sort.Slice(counts, func(i, j int) bool { return counts[i].Code < counts[j].Code })
}
