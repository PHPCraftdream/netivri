package main

import (
	"errors"
	"flag"
	"io"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

const directExitHint = "direct exit reveals this machine's address to destinations and ignores the VPN"

func saveAccountConfig(home string, cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		var validation *cli.Error
		if errors.As(err, &validation) && validation.Code == config.CodeConfigInvalid {
			for i := range cfg.Accounts {
				a := cfg.Accounts[i]
				if a.Exit != "direct" || (a.Provider == "" && a.ExitCountry == "" && (a.PreSOCKS5 == "" || strings.EqualFold(a.PreSOCKS5, "none"))) {
					continue
				}
				if directErr := config.ValidateAccountExit(a, i); directErr != nil {
					var directValidation *cli.Error
					if errors.As(directErr, &directValidation) && directValidation.Message == validation.Message {
						hint := "account " + a.Login + " is using direct exit; run netivri account set-exit " + a.Login + " route first"
						return &cli.Error{Code: validation.Code, Message: validation.Message, Hint: hint, Exit: validation.Exit}
					}
				}
			}
			return validation
		}
		return err
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		var cliErr *cli.Error
		if errors.As(err, &cliErr) {
			return cliErr
		}
		return &cli.Error{Code: "INTERNAL", Message: "save config", Exit: cli.ExitInternal}
	}
	return nil
}

func validateAccountCandidate(cfg *config.Config, updates map[int]config.Account) error {
	for index, account := range updates {
		if err := config.ValidateAccountExit(account, index); err != nil {
			var validation *cli.Error
			if errors.As(err, &validation) && account.Exit == "direct" && (account.Provider != "" || account.ExitCountry != "" || (account.PreSOCKS5 != "" && !strings.EqualFold(account.PreSOCKS5, "none"))) {
				hint := "account " + account.Login + " is using direct exit; run netivri account set-exit " + account.Login + " route first"
				return &cli.Error{Code: config.CodeConfigInvalid, Message: validation.Message, Hint: hint, Exit: cli.ExitUsage}
			}
			return err
		}
	}
	return nil
}

func effectiveExit(a *config.Account) string {
	if a.Exit == "" {
		return "route"
	}
	return a.Exit
}

func validateAccountExitMode(value string) error {
	if value != "route" && value != "direct" {
		return configInvalidAt(0, "must be empty, route or direct")
	}
	return nil
}

func validateAccountExitChange(account config.Account, index int) error {
	if account.Exit == "direct" && (account.Provider != "" || account.ExitCountry != "" || (account.PreSOCKS5 != "" && !strings.EqualFold(account.PreSOCKS5, "none"))) {
		return configInvalidAt(index, "direct requires empty provider and exit_country and no pre_socks5")
	}
	return nil
}

func invalidAccountExitAt(index int) error {
	return &cli.Error{Code: config.CodeConfigInvalid, Message: "invalid accounts[" + strconv.Itoa(index) + "].exit: use route or direct", Exit: cli.ExitUsage}
}

func accountSetExit(home string, cfg *config.Config, rest []string, save func() error, stdout io.Writer) error {
	fs := flag.NewFlagSet("account set-exit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allFlag := fs.Bool("all", false, "change every account")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("account set-exit: requires ACCOUNT[,ACCOUNT...] route|direct, or --all route|direct")
	}
	all := *allFlag
	var spec, value string
	if all {
		if len(pos) != 1 {
			return cli.Usage("account set-exit: requires ACCOUNT[,ACCOUNT...] route|direct, or --all route|direct")
		}
		value = pos[0]
	} else {
		if len(pos) != 2 {
			return cli.Usage("account set-exit: requires ACCOUNT[,ACCOUNT...] route|direct, or --all route|direct")
		}
		spec, value = pos[0], pos[1]
	}
	if err := validateAccountExitMode(value); err != nil {
		return err
	}
	targets, err := accountTargets(cfg, spec, all)
	if err != nil {
		return err
	}
	out, err := mutateAccountExit(home, cfg, targets, value, save)
	if err != nil {
		return err
	}
	multi := all || strings.Contains(spec, ",")
	if value == "direct" {
		if multi {
			cli.Success(stdout, map[string]any{"accounts": out, "hint": directExitHint})
		} else {
			cli.Success(stdout, map[string]any{"account": out[0], "hint": directExitHint})
		}
	} else if multi {
		cli.Success(stdout, map[string]any{"accounts": out})
	} else {
		cli.Success(stdout, map[string]any{"account": out[0]})
	}
	return nil
}

func configInvalidAt(index int, reason string) error {
	return &cli.Error{Code: config.CodeConfigInvalid, Message: "invalid accounts[" + strconv.Itoa(index) + "].exit: " + reason, Exit: cli.ExitUsage}
}
