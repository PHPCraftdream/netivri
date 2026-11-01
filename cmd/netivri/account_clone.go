package main

import (
	"flag"
	"io"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// How the password of a cloned account was set, reported as `password`.
const (
	clonePasswordCopied = "copied" // the source's password, hash and all
	clonePasswordNew    = "new"    // the one given with --password
	clonePasswordInit   = "init"   // the first password that connects becomes it
)

// accountClone implements `account clone SOURCE NEW`: a new account with the
// provider, exit country and allowed_clients of SOURCE and, by default, its
// password. --password sets another one, or `init` leaves it to the first
// client that connects; --provider and --country replace the copied route,
// --clients the copied allowed_clients. Nothing is written when any check
// fails, and the password and its hash are never printed.
func accountClone(home string, cfg *config.Config, rest []string, save func() error, stdout io.Writer) error {
	fs := flag.NewFlagSet("account clone", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	password := fs.String("password", "", "password of the new account, or init")
	country := fs.String("country", "", "exit country instead of the copied one")
	provider := fs.String("provider", "", "provider instead of the copied one")
	clients := fs.String("clients", "", "replace the copied allowed-clients rule list")
	exitMode := fs.String("exit", "", "exit mode instead of the copied one")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("account clone: invalid flags")
	}
	passwordGiven := false
	clientsGiven := false
	exitGiven := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "password":
			passwordGiven = true
		case "clients":
			clientsGiven = true
		case "exit":
			exitGiven = true
		}
	})
	if len(pos) != 2 {
		return cli.Usage("account clone: requires SOURCE and NEW logins")
	}
	srcLogin, newLogin := pos[0], pos[1]
	for _, login := range []string{srcLogin, newLogin} {
		if !config.ValidAccountLogin(login) || login == config.InitPlaceholder {
			return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
	}
	src, _ := cfg.Account(srcLogin)
	if src == nil {
		return &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "account not found: " + srcLogin, Exit: cli.ExitNotFound}
	}
	if existing, _ := cfg.Account(newLogin); existing != nil || (cfg.Auth.Login == newLogin && cfg.Auth.Login != config.InitPlaceholder) {
		return &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists: " + newLogin, Exit: cli.ExitConflict}
	}

	next := config.Account{Login: newLogin, Password: src.Password, Provider: src.Provider, ExitCountry: src.ExitCountry, Exit: src.Exit, AllowedClients: src.AllowedClients, PreSOCKS5: src.PreSOCKS5, PostSOCKS5: src.PostSOCKS5}
	mode := clonePasswordCopied
	if src.Password == config.InitPlaceholder {
		mode = clonePasswordInit
	}
	if passwordGiven {
		switch {
		case *password == "":
			return cli.Usage("account clone: --password must not be empty; use --password init to let the first client set it")
		case *password == config.InitPlaceholder:
			next.Password, mode = config.InitPlaceholder, clonePasswordInit
		case len(*password) > auth.MaxPasswordLen:
			return cli.Usage("account clone: --password is longer than %d bytes", auth.MaxPasswordLen)
		default:
			hash, err := auth.Hash(*password)
			if err != nil {
				return &cli.Error{Code: "INTERNAL", Message: "hash the password", Exit: cli.ExitInternal}
			}
			next.Password, mode = hash, clonePasswordNew
		}
	}
	if clientsGiven {
		if _, err := config.ParseAllowedClients(*clients); err != nil {
			return &cli.Error{Code: "CONFIG_INVALID", Message: "invalid --clients: " + err.Error(), Exit: cli.ExitUsage}
		}
		next.AllowedClients = config.NormalizeAllowedClients(*clients)
	}
	routeChanged := false
	if *provider != "" {
		if next.Provider, err = parseProviders(cfg, *provider); err != nil {
			return err
		}
		routeChanged = true
	}
	if *country != "" {
		if next.ExitCountry, err = parseCountries(*country); err != nil {
			return err
		}
		routeChanged = true
	}
	if exitGiven {
		if *exitMode != "route" && *exitMode != "direct" {
			return invalidAccountExitAt(len(cfg.Accounts))
		}
		next.Exit = *exitMode
		routeChanged = true
	}
	if err := validateAccountCandidate(cfg, map[int]config.Account{len(cfg.Accounts): next}); err != nil {
		return err
	}
	if routeChanged && effectiveExit(&next) == "route" {
		if err := checkRoute(home, cfg, next); err != nil {
			return err
		}
	}

	cfg.Accounts = append(cfg.Accounts, next)
	if err := save(); err != nil {
		return err
	}
	cli.Success(stdout, map[string]any{
		"account":     infoForAccount(cfg, &next),
		"cloned_from": srcLogin,
		"password":    mode,
	})
	return nil
}
