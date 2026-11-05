package main

import (
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// rename renames a provider; the accounts that use it and its stored nodes follow.
func (a *subApp) rename(args []string) error {
	fs := flag.NewFlagSet("sub rename", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	pos, err := parseFlags(fs, args)
	if err != nil || len(pos) != 2 {
		return cli.Usage("sub rename: requires the current name and the new name")
	}
	if e := ProviderRename(a.home, pos[0], pos[1]); e != nil {
		return e
	}
	cli.Success(a.stdout, map[string]any{"name": pos[1], "renamed_from": pos[0]})
	return nil
}

// set changes the subscription URL and/or the fetch proxy of a provider. The
// URL is a credential and is never echoed.
func (a *subApp) set(args []string) error {
	fs := flag.NewFlagSet("sub set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	url := fs.String("url", "", "new subscription URL")
	via := fs.String("via", "", "fetch proxy URL, or none to fetch without one")
	pos, err := parseFlags(fs, args)
	if err != nil || len(pos) != 1 {
		return cli.Usage("sub set: requires NAME and --url and/or --via")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["url"] && !given["via"] {
		return cli.Usage("sub set: requires --url and/or --via")
	}
	var o ProviderUpdateOptions
	changed := []string{}
	if given["url"] {
		if strings.TrimSpace(*url) == "" {
			return cli.Usage("sub set: --url needs a subscription URL")
		}
		o.URL = url
		changed = append(changed, "url")
	}
	if given["via"] {
		v := *via
		if strings.EqualFold(v, "none") {
			v = ""
		} else if v == "" {
			return cli.Usage("sub set: --via needs a proxy URL or none")
		}
		o.Via = &v
		changed = append(changed, "via")
	}
	if e := ProviderUpdate(a.home, pos[0], o); e != nil {
		return e
	}
	cli.Success(a.stdout, map[string]any{"name": pos[0], "changed": changed})
	return nil
}
