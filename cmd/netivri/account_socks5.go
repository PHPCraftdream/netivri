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
	normalize := func(s, side string) (string, error) {
		if strings.EqualFold(s, "inherit") {
			return "", nil
		}
		if strings.EqualFold(s, "none") {
			return "none", nil
		}
		if err := validateCLIChain(s, "--"+side); err != nil {
			return "", err
		}
		return s, nil
	}
	var pv, qv string
	if given["pre"] {
		pv, err = normalize(*pre, "pre")
		if err != nil {
			return err
		}
	}
	if given["post"] {
		qv, err = normalize(*post, "post")
		if err != nil {
			return err
		}
	}
	for _, a := range targets {
		if given["pre"] {
			a.PreSOCKS5 = pv
		}
		if given["post"] {
			a.PostSOCKS5 = qv
		}
	}
	if err := save(); err != nil {
		return err
	}
	out := make([]accountInfo, 0, len(targets))
	for _, a := range targets {
		out = append(out, infoForAccount(cfg, a))
	}
	if len(targets) == 1 {
		cli.Success(stdout, map[string]any{"account": out[0]})
		return nil
	}
	cli.Success(stdout, map[string]any{"accounts": out})
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
	return accountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: a.ExitCountry, AllowedClients: a.AllowedClients, PreSOCKS5: chainInfo(a.PreSOCKS5, cfg.Chain.PreSOCKS5), PostSOCKS5: chainInfo(a.PostSOCKS5, cfg.Chain.PostSOCKS5), PasswordSet: accountHasPassword(a)}
}
func chainHasAuth(s string) bool { u, e := url.Parse(s); return e == nil && u.User != nil }
