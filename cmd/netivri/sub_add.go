package main

import (
	"flag"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"io"
)

func (a *subApp) add(args []string) error {
	fs := flag.NewFlagSet("sub add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	name := fs.String("name", "", "subscription name (default: derived from the URL host)")
	via := fs.String("via", "", "fetch through this proxy URL")
	pre := fs.String("pre-socks5", "", "pre-hop proxy URL for this provider's nodes (socks5://, http:// or https://) or none")
	post := fs.String("post-socks5", "", "post-hop proxy URL for this provider's nodes (socks5://, http:// or https://) or none")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub add: %v", err)
	}
	if len(pos) != 1 {
		return cli.Usage("sub add: exactly one <url> argument is required")
	}
	if *name != "" && !validSubName(*name) {
		return cli.Usage("sub add: --name must match [A-Za-z0-9._-]{1,64}")
	}
	for _, h := range []struct{ v, flag string }{{*pre, "--pre-socks5"}, {*post, "--post-socks5"}} {
		if h.v != "" {
			if e := validateCLIChain(h.v, h.flag); e != nil {
				return e
			}
		}
	}
	r, err := ProviderAdd(a.home, ProviderAddOptions{URL: pos[0], Name: *name, Via: *via, Pre: *pre, Post: *post}, a.fetch, a.now)
	if err != nil {
		return err
	}
	cli.Success(a.stdout, map[string]any{"name": r.Name, "added": r.Added, "existing": r.Existing, "nodes": r.Nodes, "skipped": r.Skipped, "countries": r.Countries})
	return nil
}
