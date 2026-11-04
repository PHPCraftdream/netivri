package main

import (
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// setSOCKS5 sets or clears the provider-level pre and post hops.
func (a *subApp) setSOCKS5(args []string) error {
	fs := flag.NewFlagSet("sub set-socks5", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	pre := fs.String("pre", "", "pre-hop proxy URL (socks5://, http:// or https://), none or inherit")
	post := fs.String("post", "", "post-hop proxy URL (socks5://, http:// or https://), none or inherit")
	pos, err := parseFlags(fs, args)
	if err != nil || len(pos) != 1 {
		return cli.Usage("sub set-socks5: requires NAME and --pre and/or --post")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["pre"] && !given["post"] {
		return cli.Usage("sub set-socks5: requires --pre and/or --post")
	}
	var o ProviderUpdateOptions
	for _, h := range []struct {
		given bool
		value string
		out   **string
		flag  string
	}{{given["pre"], *pre, &o.Pre, "--pre"}, {given["post"], *post, &o.Post, "--post"}} {
		if !h.given {
			continue
		}
		v := h.value
		if strings.EqualFold(v, "inherit") {
			v = ""
		} else if v == "" {
			return cli.Usage("sub set-socks5: %s needs a proxy URL, none or inherit", h.flag)
		} else if e := validateCLIChain(v, h.flag); e != nil {
			return e
		}
		*h.out = &v
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
			cli.Success(a.stdout, map[string]any{"name": s.Name, "pre_socks5": redactChain(s.PreSOCKS5), "post_socks5": redactChain(s.PostSOCKS5)})
			return nil
		}
	}
	return nil
}
