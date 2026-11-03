package main

import (
	"flag"
	"io"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// How the password of a cloned account was set, reported as `password`.
const (
	clonePasswordNew  = "new"  // the one given with --password
	clonePasswordInit = "init" // the first password that connects becomes it
)

// accountClone implements `account clone SOURCE NEW`: a new account with the
// provider, exit country and allowed_clients of SOURCE and a fresh placeholder
// password that the first client to authenticate sets. --password sets another
// one; --provider and --country replace the copied route, --clients the copied
// allowed_clients. Nothing is written when any check fails, and the password
// and its hash are never printed.
func accountClone(home string, cfg *config.Config, rest []string, save func() error, stdout io.Writer) error {
	fs := flag.NewFlagSet("account clone", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	password := fs.String("password", "", "password of the new account instead of a fresh placeholder (the first client that authenticates sets it)")
	country := fs.String("country", "", "exit country instead of the copied one")
	provider := fs.String("provider", "", "provider instead of the copied one")
	clients := fs.String("clients", "", "replace the copied allowed-clients rule list")
	exit := fs.String("exit", "", "exit mode instead of the copied one")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("account clone: invalid flags")
	}
	if len(pos) != 2 {
		return cli.Usage("account clone: requires SOURCE and NEW logins")
	}
	o := AccountCloneOptions{Source: pos[0], Login: pos[1], Password: *password, Provider: *provider, Country: *country, Clients: *clients, Exit: *exit}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "password":
			o.PasswordGiven = true
		case "clients":
			o.ClientsGiven = true
		case "exit":
			o.ExitGiven = true
		}
	})
	r, err := accountCloneWith(home, cfg, o, save)
	if err != nil {
		return err
	}
	cli.Success(stdout, map[string]any{"account": r.Account, "cloned_from": r.ClonedFrom, "password": r.Password})
	return nil
}
