package main

import (
	"flag"
	"io"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func (a *subApp) add(args []string) error {
	fs := flag.NewFlagSet("sub add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	name := fs.String("name", "", "subscription name (default: derived from the URL host)")
	via := fs.String("via", "", "fetch through this proxy URL")
	pre := addRepeatedStringFlag(fs, "pre-socks5", "pre-hop proxy URL for this provider's nodes (repeat for each hop)")
	post := addRepeatedStringFlag(fs, "post-socks5", "post-hop proxy URL for this provider's nodes (repeat for each hop)")
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
	preGiven, postGiven := false, false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pre-socks5" {
			preGiven = true
		}
		if f.Name == "post-socks5" {
			postGiven = true
		}
	})
	preList, err := parseHopListFlag(*pre, preGiven, "--pre-socks5")
	if err != nil {
		return err
	}
	postList, err := parseHopListFlag(*post, postGiven, "--post-socks5")
	if err != nil {
		return err
	}
	r, err := ProviderAdd(a.home, ProviderAddOptions{URL: pos[0], Name: *name, Via: *via, Pre: preList, Post: postList}, a.fetch, a.now)
	if err != nil {
		return err
	}
	cli.Success(a.stdout, map[string]any{"name": r.Name, "added": r.Added, "existing": r.Existing, "nodes": r.Nodes, "skipped": r.Skipped, "countries": r.Countries})
	return nil
}
