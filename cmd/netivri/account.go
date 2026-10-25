package main

import (
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

type accountInfo struct {
	Login       string `json:"login"`
	Provider    string `json:"provider"`
	ExitCountry string `json:"exit_country"`
	PasswordSet bool   `json:"password_set"`
}

func runAccount(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("account: missing subcommand (add, clone, list, show, set-country, set-provider, rm)")
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
		if err := config.Save(config.ConfigPath(home), cfg); err != nil {
			var cliErr *cli.Error
			if errors.As(err, &cliErr) {
				return cliErr
			}
			return &cli.Error{Code: "INTERNAL", Message: "save config", Exit: cli.ExitInternal}
		}
		return nil
	}
	usage := func(msg string) error { return cli.Usage("account %s: %s", args[0], msg) }
	switch args[0] {
	case "list":
		if len(rest) != 0 {
			return usage("unexpected argument")
		}
		items := make([]accountInfo, 0, len(cfg.Accounts))
		for _, a := range cfg.Accounts {
			items = append(items, accountInfo{a.Login, a.Provider, a.ExitCountry, accountHasPassword(&a)})
		}
		cli.Success(stdout, map[string]any{"accounts": items})
		return nil
	case "show":
		return accountShow(home, cfg, rest, stdout)
	case "add":
		fs := flag.NewFlagSet("account add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		country := fs.String("country", "", "exit country")
		provider := fs.String("provider", "", "subscription (provider) the account exits through")
		pos, e := parseFlags(fs, rest)
		if e != nil {
			return usage("invalid flags")
		}
		if len(pos) != 1 {
			return usage("requires LOGIN")
		}
		login := pos[0]
		if !config.ValidAccountLogin(login) {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		cc := ""
		if *country != "" {
			var err error
			if cc, err = parseCountries(*country); err != nil {
				return err
			}
		}
		prov := ""
		if *provider != "" {
			var err error
			if prov, err = parseProviders(cfg, *provider); err != nil {
				return err
			}
		}
		if cfg.Auth.Login == login && cfg.Auth.Login != config.InitPlaceholder {
			return &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists", Exit: cli.ExitConflict}
		}
		if a, _ := cfg.Account(login); a != nil {
			if a.ExitCountry == cc && a.Provider == prov {
				cli.Success(stdout, map[string]any{"account": accountInfo{a.Login, a.Provider, a.ExitCountry, accountHasPassword(a)}, "existing": true})
				return nil
			}
			return &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists with a different provider or country", Exit: cli.ExitConflict}
		}
		cfg.Accounts = append(cfg.Accounts, config.Account{Login: login, Password: config.InitPlaceholder, Provider: prov, ExitCountry: cc})
		if err := save(); err != nil {
			return err
		}
		cli.Success(stdout, map[string]any{"account": accountInfo{login, prov, cc, false}, "existing": false})
		return nil
	case "clone":
		return accountClone(home, cfg, rest, save, stdout)
	case "set-country":
		return accountSet("country", home, cfg, rest, save, stdout)
	case "set-provider":
		return accountSet("provider", home, cfg, rest, save, stdout)
	case "rm":
		if len(rest) != 1 {
			return usage("requires LOGIN")
		}
		login := rest[0]
		if !config.ValidAccountLogin(login) {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		_, i := cfg.Account(login)
		if i < 0 {
			return accountNotFound()
		}
		cfg.Accounts = append(cfg.Accounts[:i], cfg.Accounts[i+1:]...)
		if err := save(); err != nil {
			return err
		}
		cli.Success(stdout, map[string]any{"removed": login})
		return nil
	default:
		return cli.Usage("unknown account command %q", args[0])
	}
}

// accountSet implements `account set-country` and `account set-provider`
// (field "country" or "provider"): ACCOUNT[,ACCOUNT...] VALUE, --all VALUE,
// or either form with --none. Every account is checked before anything is
// written: a change that would leave an account without nodes is refused as a
// whole.
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
		} else {
			value, err = parseProviders(cfg, value)
		}
		if err != nil {
			return err
		}
	}
	changed := func(a *config.Account, to string) config.Account {
		c := *a
		if field == "country" {
			c.ExitCountry = to
		} else {
			c.Provider = to
		}
		return c
	}
	for _, a := range targets {
		next := changed(a, value)
		if next == *a {
			continue // repeating the same value always succeeds
		}
		if err := checkRoute(home, cfg, next); err != nil {
			return err
		}
	}
	for _, a := range targets {
		*a = changed(a, value)
	}
	if err := save(); err != nil {
		return err
	}
	items := make([]accountInfo, 0, len(targets))
	for _, a := range targets {
		items = append(items, accountInfo{a.Login, a.Provider, a.ExitCountry, accountHasPassword(a)})
	}
	if !*all && !strings.Contains(spec, ",") {
		cli.Success(stdout, map[string]any{"account": items[0]})
		return nil
	}
	cli.Success(stdout, map[string]any{"accounts": items})
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
