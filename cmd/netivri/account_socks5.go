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
	pre := addRepeatedStringFlag(fs, "pre", "replace the pre-hop list (repeat to set an ordered list)")
	post := addRepeatedStringFlag(fs, "post", "replace the post-hop list (repeat to set an ordered list)")
	addPre := addRepeatedStringFlag(fs, "add-pre", "append one pre-hop URL")
	addPost := addRepeatedStringFlag(fs, "add-post", "append one post-hop URL")
	removePre := addRepeatedStringFlag(fs, "remove-pre", "remove a one-based original pre-hop index")
	removePost := addRepeatedStringFlag(fs, "remove-post", "remove a one-based original post-hop index")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("account set-socks5: %v", err)
	}
	if len(pos) != 1 {
		return cli.Usage("account set-socks5: requires ACCOUNT[,ACCOUNT...]")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	preEdit, preGiven, err := parseHopEditFlags(*pre, *addPre, *removePre, given["pre"], given["add-pre"], given["remove-pre"], "pre")
	if err != nil {
		return err
	}
	postEdit, postGiven, err := parseHopEditFlags(*post, *addPost, *removePost, given["post"], given["add-post"], given["remove-post"], "post")
	if err != nil {
		return err
	}
	if !preGiven && !postGiven {
		return cli.Usage("account set-socks5: requires --pre/--post replacement or --add-pre/--add-post/--remove-pre/--remove-post")
	}
	targets, err := accountTargets(cfg, pos[0], false)
	if err != nil {
		return err
	}
	out, err := mutateAccountSOCKS5Edits(cfg, targets, preGiven, postGiven, preEdit, postEdit, save)
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

func parseHopListFlag(values []string, given bool, flagName string) (config.HopList, error) {
	if !given {
		return nil, nil
	}
	if len(values) == 0 {
		return nil, cli.Usage("%s requires a URL", flagName)
	}
	for _, value := range values {
		if value == "" {
			return nil, cli.Usage("%s requires a non-empty URL", flagName)
		}
	}
	hops := config.HopList(values)
	if err := validateCLIHopList(hops, flagName); err != nil {
		return nil, err
	}
	return hops, nil
}

func parseHopEditFlags(replaceValues, addValues, removeValues []string, replaceGiven, addGiven, removeGiven bool, side string) (HopEdit, bool, error) {
	field := "--" + side
	incremental := addGiven || removeGiven
	if replaceGiven && incremental {
		return HopEdit{}, false, cli.Usage("--%s and --add-%s/--remove-%s cannot be combined", side, side, side)
	}
	if addGiven && removeGiven {
		return HopEdit{}, false, cli.Usage("%s: adding and removing hops cannot be combined", field)
	}
	if replaceGiven {
		if len(replaceValues) == 0 {
			return HopEdit{}, false, cli.Usage("%s requires a value", field)
		}
		for _, value := range replaceValues {
			if value == "" {
				return HopEdit{}, false, cli.Usage("%s requires a non-empty value", field)
			}
		}
		if len(replaceValues) == 1 && strings.EqualFold(replaceValues[0], "inherit") {
			return HopEdit{Replace: replaceHopList(nil)}, true, nil
		}
		for _, value := range replaceValues {
			if strings.EqualFold(value, "inherit") || strings.EqualFold(value, "none") && len(replaceValues) != 1 {
				return HopEdit{}, false, cli.Usage("%s: inherit and none must be supplied alone", field)
			}
		}
		hops := config.HopList(replaceValues)
		if err := validateCLIHopList(hops, field); err != nil {
			return HopEdit{}, false, err
		}
		return HopEdit{Replace: replaceHopList(hops)}, true, nil
	}
	if addGiven {
		if len(addValues) == 0 {
			return HopEdit{}, false, cli.Usage("--add-%s requires a URL", side)
		}
		for _, value := range addValues {
			if strings.EqualFold(value, "none") || strings.EqualFold(value, "inherit") {
				return HopEdit{}, false, cli.Usage("--add-%s accepts proxy URLs only", side)
			}
		}
		hops := config.HopList(addValues)
		if err := validateCLIHopList(hops, "--add-"+side); err != nil {
			return HopEdit{}, false, err
		}
		return HopEdit{Add: hops}, true, nil
	}
	if removeGiven {
		indices, err := parseHopIndices(removeValues, "--remove-"+side)
		if err != nil {
			return HopEdit{}, false, err
		}
		return HopEdit{Remove: indices}, true, nil
	}
	return HopEdit{}, false, nil
}

func validateCLIChain(s, flagName string) error {
	return validateCLIHopList(config.HopList{s}, flagName)
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
	chainInfo := func(own config.HopList, post bool) accountChainInfo {
		resolved := resolveAccountChain(cfg, a, post)
		var ownValue *[]string
		if len(own) > 0 {
			v := redactChains(own)
			ownValue = &v
		}
		return accountChainInfo{Value: redactChains(config.HopList(resolved.value)), Source: resolved.source, Auth: resolved.auth, Own: ownValue, OwnSet: len(own) > 0}
	}
	preInfo := chainInfo(a.PreSOCKS5, false)
	if effectiveExit(a) == "direct" {
		preInfo = accountChainInfo{Value: []string{"none"}, Source: "direct", OwnSet: len(a.PreSOCKS5) > 0}
	}
	return accountInfo{Login: a.Login, Provider: a.Provider, ExitCountry: a.ExitCountry, Exit: effectiveExit(a), AllowedClients: a.AllowedClients, PreSOCKS5: preInfo, PostSOCKS5: chainInfo(a.PostSOCKS5, true), PasswordSet: accountHasPassword(a)}
}

func chainHasAuth(s string) bool { u, e := url.Parse(s); return e == nil && u.User != nil }
