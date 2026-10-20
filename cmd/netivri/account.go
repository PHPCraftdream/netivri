package main

import (
	"errors"
	"flag"
	"io"

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
		return cli.Usage("account: missing subcommand (add, list, set-country, set-provider, rm)")
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
			var ok bool
			cc, ok = config.NormalizeCountry(*country)
			if !ok {
				return &cli.Error{Code: "COUNTRY_INVALID", Message: "invalid country code", Exit: cli.ExitUsage}
			}
		}
		prov := *provider
		if prov != "" {
			if err := checkProvider(cfg, prov); err != nil {
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
	case "set-country":
		fs := flag.NewFlagSet("account set-country", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		none := fs.Bool("none", false, "clear country")
		pos, e := parseFlags(fs, rest)
		if e != nil || (*none && len(pos) != 1) || (!*none && len(pos) != 2) {
			return usage("requires LOGIN CC (or LOGIN --none)")
		}
		login := pos[0]
		if !config.ValidAccountLogin(login) {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		a, _ := cfg.Account(login)
		if a == nil {
			return accountNotFound()
		}
		cc := ""
		if !*none {
			var ok bool
			cc, ok = config.NormalizeCountry(pos[1])
			if !ok {
				return &cli.Error{Code: "COUNTRY_INVALID", Message: "invalid country code", Exit: cli.ExitUsage}
			}
		}
		a.ExitCountry = cc
		if err := save(); err != nil {
			return err
		}
		cli.Success(stdout, map[string]any{"account": accountInfo{a.Login, a.Provider, a.ExitCountry, accountHasPassword(a)}})
		return nil
	case "set-provider":
		fs := flag.NewFlagSet("account set-provider", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		none := fs.Bool("none", false, "clear provider")
		pos, e := parseFlags(fs, rest)
		if e != nil || (*none && len(pos) != 1) || (!*none && len(pos) != 2) {
			return usage("requires LOGIN NAME (or LOGIN --none)")
		}
		login := pos[0]
		if !config.ValidAccountLogin(login) {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
		a, _ := cfg.Account(login)
		if a == nil {
			return accountNotFound()
		}
		prov := ""
		if !*none {
			prov = pos[1]
			if err := checkProvider(cfg, prov); err != nil {
				return err
			}
		}
		a.Provider = prov
		if err := save(); err != nil {
			return err
		}
		cli.Success(stdout, map[string]any{"account": accountInfo{a.Login, a.Provider, a.ExitCountry, accountHasPassword(a)}})
		return nil
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
