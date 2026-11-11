package main

import (
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

type accountChainInfo struct {
	Value  []string  `json:"value"`
	Source string    `json:"source"`
	Auth   bool      `json:"auth"`
	Own    *[]string `json:"own,omitempty"`
}

type accountInfo struct {
	Login          string           `json:"login"`
	Provider       string           `json:"provider"`
	ExitCountry    string           `json:"exit_country"`
	AllowedClients string           `json:"allowed_clients"`
	PreSOCKS5      accountChainInfo `json:"pre_socks5"`
	PostSOCKS5     accountChainInfo `json:"post_socks5"`
	PasswordSet    bool             `json:"password_set"`
	Exit           string           `json:"exit"`
}

func accountList(cfg *config.Config) []accountInfo {
	items := make([]accountInfo, 0, len(cfg.Accounts))
	for i := range cfg.Accounts {
		info := infoForAccount(cfg, &cfg.Accounts[i])
		info.PreSOCKS5.Own = nil
		info.PostSOCKS5.Own = nil
		items = append(items, info)
	}
	return items
}
func runAccount(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("account: missing subcommand (add, clone, list, show, set-country, set-provider, set-clients, rm)")
	}
	home, rest, err := parseHomeArgs("account "+args[0], args[1:])
	if err != nil {
		return err
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}
	save := func() error {
		return saveAccountConfig(home, cfg)
	}
	usage := func(msg string) error { return cli.Usage("account %s: %s", args[0], msg) }
	switch args[0] {
	case "list":
		if len(rest) != 0 {
			return usage("unexpected argument")
		}
		items := accountList(cfg)
		cli.Success(stdout, map[string]any{"accounts": items})
		return nil
	case "show":
		return accountShow(home, cfg, rest, stdout)
	case "add":
		fs := flag.NewFlagSet("account add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		country := fs.String("country", "", "exit country")
		provider := fs.String("provider", "", "subscription (provider) the account exits through")
		clients := fs.String("clients", "", "client addresses that may use this account (allowed-clients rules)")
		pre := addRepeatedStringFlag(fs, "pre-socks5", "pre-hop proxy URL (repeat for each hop)")
		post := addRepeatedStringFlag(fs, "post-socks5", "post-hop proxy URL (repeat for each hop)")
		exitFlag := fs.String("exit", "", "account exit mode (route or direct)")
		pos, e := parseFlags(fs, rest)
		if e != nil {
			return usage("invalid flags")
		}
		preGiven, postGiven, exitModeGiven := false, false, false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "pre-socks5" {
				preGiven = true
			}
			if f.Name == "post-socks5" {
				postGiven = true
			}
			if f.Name == "exit" {
				exitModeGiven = true
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
		if len(pos) != 1 {
			return usage("requires LOGIN")
		}
		o := AccountAddOptions{Login: pos[0], Provider: *provider, Country: *country, Clients: *clients, PreSOCKS5: preList, PostSOCKS5: postList}
		if exitModeGiven {
			o.Exit = *exitFlag
		}
		result := AccountAddResult{}
		o.ExitGiven = exitModeGiven
		if err := accountAddWith(home, cfg, o, save, &result); err != nil {
			return err
		}
		out := map[string]any{"account": result.Account, "existing": result.Existing}
		if result.Hint != "" {
			out["hint"] = result.Hint
		}
		cli.Success(stdout, out)
		return nil
	case "clone":
		return accountClone(home, cfg, rest, save, stdout)
	case "set-exit":
		return accountSetExit(home, cfg, rest, save, stdout)
	case "set-socks5":
		return accountSetSOCKS5(cfg, rest, save, stdout)
	case "set-country":
		return accountSet("country", home, cfg, rest, save, stdout)
	case "set-provider":
		return accountSet("provider", home, cfg, rest, save, stdout)
	case "set-clients":
		return accountSet("clients", home, cfg, rest, save, stdout)
	case "rm":
		if len(rest) != 1 {
			return usage("requires LOGIN")
		}
		login := rest[0]
		if !config.ValidAccountLogin(login) {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		if err := accountRemoveWith(cfg, login, save); err != nil {
			return err
		}
		cli.Success(stdout, map[string]any{"removed": login})
		return nil
	default:
		return cli.Usage("unknown account command %q", args[0])
	}
}

// accountSet implements `account set-country`, `account set-provider` and
// `account set-clients` (field "country", "provider" or "clients"):
// ACCOUNT[,ACCOUNT...] VALUE, --all VALUE, or either form with --none. Every
// account is checked before anything is written: a change that would leave an
// account without nodes is refused as a whole.
func accountSet(field, home string, cfg *config.Config, rest []string, save func() error, stdout io.Writer) error {
	name := "account set-" + field
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	none := fs.Bool("none", false, "clear the value")
	all := fs.Bool("all", false, "change every account")
	pos, err := parseFlags(fs, rest)
	want := 2
	if *all {
		want--
	}
	if *none {
		want--
	}
	if err != nil || len(pos) != want {
		return cli.Usage("%s: requires ACCOUNT[,ACCOUNT...] and a value, or --all and a value (--none replaces the value)", name)
	}
	spec := ""
	if !*all {
		spec, pos = pos[0], pos[1:]
	}
	targets, err := accountTargets(cfg, spec, *all)
	if err != nil {
		return err
	}
	value := ""
	if !*none {
		value = pos[0]
		var err error
		if field == "country" {
			value, err = parseCountries(value)
		} else if field == "clients" {
			if _, err = config.ParseAllowedClients(value); err != nil {
				return &cli.Error{Code: "CONFIG_INVALID", Message: err.Error(), Exit: cli.ExitUsage}
			}
			value = config.NormalizeAllowedClients(value)
		} else {
			value, err = parseProviders(cfg, value)
		}
		if err != nil {
			return err
		}
	}
	items, err := updateAccountTargets(home, cfg, field, targets, spec, *all, value, save)
	if err != nil {
		return err
	}
	if !*all && !strings.Contains(spec, ",") {
		cli.Success(stdout, map[string]any{"account": items[0]})
	} else {
		cli.Success(stdout, map[string]any{"accounts": items})
	}
	return nil
}

// accountTargets resolves the accounts a set-* command changes: every account
// with all, else the comma-separated logins in spec (repeats are ignored).
func accountTargets(cfg *config.Config, spec string, all bool) ([]*config.Account, error) {
	if all {
		if len(cfg.Accounts) == 0 {
			return nil, &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "there are no accounts", Exit: cli.ExitNotFound}
		}
		out := make([]*config.Account, len(cfg.Accounts))
		for i := range cfg.Accounts {
			out[i] = &cfg.Accounts[i]
		}
		return out, nil
	}
	var out []*config.Account
	seen := map[string]bool{}
	for _, login := range strings.Split(spec, ",") {
		if !config.ValidAccountLogin(login) {
			return nil, &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		if seen[login] {
			continue
		}
		seen[login] = true
		a, _ := cfg.Account(login)
		if a == nil {
			return nil, &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "account not found: " + login, Exit: cli.ExitNotFound}
		}
		out = append(out, a)
	}
	return out, nil
}

// checkRoute refuses an account whose provider and country leave the route
// without a stored node. The question is the one `up` asks, so what passes
// here starts; an account with neither provider nor country has no route and
// always passes.
func checkRoute(home string, cfg *config.Config, a config.Account) error {
	probe := &config.Config{Subscriptions: cfg.Subscriptions, Accounts: []config.Account{a}}
	_, err := accountRoutePool(home, probe)
	var ce *cli.Error
	if errors.As(err, &ce) && ce.Code == "NO_NODES_FOR_COUNTRY" {
		return &cli.Error{
			Code:    ce.Code,
			Message: ce.Message + " (account " + a.Login + "); nothing was changed",
			Hint:    "see what a subscription offers: netivri sub countries {SUBSCRIPTION}; refresh the lists: netivri sub update",
			Exit:    ce.Exit,
		}
	}
	return err
}

// parseCountries turns an exit-country argument, one code or a comma-separated
// list in priority order, into the canonical stored form ("DE,NL").
func parseCountries(raw string) (string, error) {
	codes, ok := config.ParseCountryList(raw)
	if !ok || len(codes) == 0 {
		return "", &cli.Error{Code: "COUNTRY_INVALID", Message: "invalid country code or list of codes", Exit: cli.ExitUsage}
	}
	return config.JoinList(codes), nil
}

// parseProviders turns a provider argument, one name or a comma-separated list
// in priority order, into the canonical stored form ("main,backup") and
// requires every name to be a configured subscription.
func parseProviders(cfg *config.Config, raw string) (string, error) {
	names, ok := config.ParseProviderList(raw)
	if !ok || len(names) == 0 {
		return "", cli.Usage("account: the provider must be a name matching [A-Za-z0-9._-]{1,64} or a comma-separated list of them")
	}
	for _, name := range names {
		if err := checkProvider(cfg, name); err != nil {
			return "", err
		}
	}
	return config.JoinList(names), nil
}

// checkProvider validates a provider name and requires it to be a
// configured subscription. The name is a label, never a URL.
func checkProvider(cfg *config.Config, name string) error {
	if !config.ValidProviderName(name) {
		return cli.Usage("account: --provider must match [A-Za-z0-9._-]{1,64}")
	}
	for _, s := range cfg.Subscriptions {
		if s.Name == name {
			return nil
		}
	}
	return &cli.Error{Code: "SUB_NOT_FOUND", Message: "no subscription with that name", Exit: cli.ExitNotFound}
}

func accountHasPassword(a *config.Account) bool {
	return a.Password != "" && a.Password != config.InitPlaceholder
}

func accountNotFound() error {
	return &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "account not found", Exit: cli.ExitNotFound}
}
