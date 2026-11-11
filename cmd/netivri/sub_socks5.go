package main

import (
	"flag"
	"io"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// setSOCKS5 edits the provider-level pre and post hop lists.
func (a *subApp) setSOCKS5(args []string) error {
	fs := flag.NewFlagSet("sub set-socks5", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	pre := addRepeatedStringFlag(fs, "pre", "replace the provider pre-hop list")
	post := addRepeatedStringFlag(fs, "post", "replace the provider post-hop list")
	addPre := addRepeatedStringFlag(fs, "add-pre", "append one pre-hop URL")
	addPost := addRepeatedStringFlag(fs, "add-post", "append one post-hop URL")
	removePre := addRepeatedStringFlag(fs, "remove-pre", "remove a one-based original pre-hop index")
	removePost := addRepeatedStringFlag(fs, "remove-post", "remove a one-based original post-hop index")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub set-socks5: %v", err)
	}
	if len(pos) != 1 {
		return cli.Usage("sub set-socks5: requires NAME and at least one hop edit")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var o ProviderUpdateOptions
	for _, h := range []struct {
		side                                string
		values, adds, removes               []string
		replaceGiven, addGiven, removeGiven bool
		out                                 **config.HopList
		edit                                **HopEdit
	}{{"pre", *pre, *addPre, *removePre, given["pre"], given["add-pre"], given["remove-pre"], &o.Pre, &o.PreEdit}, {"post", *post, *addPost, *removePost, given["post"], given["add-post"], given["remove-post"], &o.Post, &o.PostEdit}} {
		ed, wasGiven, err := parseHopEditFlags(h.values, h.adds, h.removes, h.replaceGiven, h.addGiven, h.removeGiven, h.side)
		if err != nil {
			return err
		}
		if !wasGiven {
			continue
		}
		if ed.Replace != nil {
			*h.out = replaceHopList(*ed.Replace)
		} else {
			copy := ed
			*h.edit = &copy
		}
	}
	if o.Pre == nil && o.Post == nil && o.PreEdit == nil && o.PostEdit == nil {
		return cli.Usage("sub set-socks5: requires a replacement or incremental edit")
	}
	if e := ProviderUpdate(a.home, pos[0], o); e != nil {
		return e
	}
	cfg, e := a.load()
	if e != nil {
		return e
	}
	for _, s := range cfg.Subscriptions {
		if s.Name == pos[0] {
			cli.Success(a.stdout, map[string]any{"name": s.Name, "pre_socks5": redactChains(s.PreSOCKS5), "post_socks5": redactChains(s.PostSOCKS5)})
			return nil
		}
	}
	return nil
}
