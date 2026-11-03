package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func (a *subApp) countries(args []string) error {
	fs := flag.NewFlagSet("sub countries", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	u := fs.String("url", "", "count the countries of this subscription URL without storing it")
	v := fs.String("via", "", "fetch --url through this proxy URL")
	pos, e := parseFlags(fs, args)
	if e != nil {
		return cli.Usage("sub countries: %v", e)
	}
	us, vs := false, false
	fs.Visit(func(f *flag.Flag) { us = us || f.Name == "url"; vs = vs || f.Name == "via" })
	switch {
	case us && *u == "":
		return cli.Usage("sub countries: --url needs a value")
	case vs && !us:
		return cli.Usage("sub countries: --via applies to --url only")
	case us && len(pos) > 0:
		return cli.Usage("sub countries: pass a NAME or --url, not both")
	case us:
		return a.countriesURL(*u, *v)
	case len(pos) > 1:
		return cli.Usage("sub countries: at most one subscription name")
	case len(pos) == 1:
		return a.countriesName(pos[0])
	default:
		return a.countriesAll()
	}
}
func (a *subApp) countriesURL(rawURL, via string) error {
	n, c, u, e := ProviderCountriesURL(a.home, rawURL, via, a.fetch)
	if e != nil {
		return e
	}
	cli.Success(a.stdout, map[string]any{"nodes": n, "countries": c, "unknown": u})
	return nil
}
func (a *subApp) countriesName(name string) error {
	r, e := ProviderCountries(a.home, name)
	if e != nil {
		return e
	}
	cli.Success(a.stdout, map[string]any{"provider": r.Name, "nodes": r.Nodes, "countries": r.Countries, "unknown": r.Unknown})
	return nil
}
func (a *subApp) countriesAll() error {
	r, e := ProviderCountriesAll(a.home)
	if e != nil {
		return e
	}
	ps := make([]map[string]any, 0, len(r.Providers))
	for _, p := range r.Providers {
		ps = append(ps, map[string]any{"name": p.Name, "nodes": p.Nodes, "countries": p.Countries, "unknown": p.Unknown})
	}
	cli.Success(a.stdout, map[string]any{"providers": ps, "countries": r.Countries})
	return nil
}
func snapshotEntry(key, name string, snap *subscription.Snapshot, e error) (map[string]any, error) {
	m := map[string]any{key: name, "nodes": nil, "countries": nil, "unknown": nil}
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return m, nil
		}
		return nil, snapshotError(name)
	}
	cs, u := subscription.CountryCounts(snap.Nodes)
	m["nodes"] = len(snap.Nodes)
	m["countries"] = cs
	m["unknown"] = u
	return m, nil
}
func sortCountryCounts(cs []subscription.CountryCount) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Code < cs[j].Code })
}
