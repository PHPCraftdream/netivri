package main

import (
	"flag"
	"io"
	"net/url"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func accountSetSOCKS5(cfg *config.Config, rest []string, save func() error, stdout io.Writer) error {
	fs := flag.NewFlagSet("account set-socks5", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pre := fs.String("pre", "", "pre-hop proxy URL (socks5://, http:// or https://), none or inherit")
	post := fs.String("post", "", "post-hop proxy URL (socks5://, http:// or https://), none or inherit")
	pos, err := parseFlags(fs, rest)
	if err != nil || len(pos) != 1 {
		return cli.Usage("account set-socks5: requires ACCOUNT[,ACCOUNT...] and --pre and/or --post")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["pre"] && !given["post"] {
		return cli.Usage("account set-socks5: requires --pre and/or --post")
	}
	targets, err := accountTargets(cfg, pos[0], false)
	if err != nil {
		return err
	}
	out, err := mutateAccountSOCKS5(cfg, targets, given["pre"], given["post"], *pre, *post, save)
	if err != nil {
		return err
	}
	if len(out) == 1 {
		cli.Success(stdout, map[string]any{"account": out[0]})
	} else {
		cli.Success(stdout, map[string]any{"accounts": out})
	}
	return nil
}
func validateCLIChain(s, flagName string) error {
	c := config.Default()
	c.Chain.PreSOCKS5 = s
	if err := c.Validate(); err != nil {
		return &cli.Error{Code: config.CodeConfigInvalid, Message: "invalid " + flagName + " proxy URL: use socks5://, http:// or https:// with a host and port", Exit: cli.ExitUsage}
	}
	return nil
}
func redactChain(s string) string {
	if s == "" || strings.EqualFold(s, "none") {
		return s
	}
	u, e := url.Parse(s)
	if e != nil {
		return "configured"
	}
	u.User = nil
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func infoForAccount(cfg *config.Config, a *config.Account) accountInfo {
	chainInfo := func(own, global string) accountChainInfo {
		resolved := resolveChain(own, global)
		var ownValue *string
		if own != "" {
			v := redactChain(own)
			ownValue = &v
		}
		return accountChainInfo{Value: resolved.value, Source: resolved.source, Auth: resolved.auth, Own: ownValue}
	}
	preInfo := chainInfo(a.PreSOCKS5, cfg.Chain.PreSOCKS5)
	if effectiveExit(a) == "direct" {
		preInfo = accountChainInfo{Value: "none", Source: "direct"}
	}
	return accountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: a.ExitCountry, Exit: effectiveExit(a), AllowedClients: a.AllowedClients, PreSOCKS5: preInfo, PostSOCKS5: chainInfo(a.PostSOCKS5, cfg.Chain.PostSOCKS5), PasswordSet: accountHasPassword(a)}
}
func chainHasAuth(s string) bool { u, e := url.Parse(s); return e == nil && u.User != nil }
