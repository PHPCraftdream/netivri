package main

import (
	"context"
	"slices"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

type AccountAddOptions struct {
	Login, Provider, Country, Clients, Exit string
	PreSOCKS5, PostSOCKS5                   config.HopList
	ExitGiven                               bool
}
type AccountAddResult struct {
	Account  accountInfo
	Existing bool
	Hint     string
}
type AccountUpdateOptions struct {
	Login                            string
	Provider, Country, Clients, Exit *string
	Pre, Post                        *config.HopList
	PreEdit, PostEdit                *HopEdit
}
type AccountCloneOptions struct {
	Source, Login, Password, Provider, Country, Clients, Exit string
	PasswordGiven, ClientsGiven, ExitGiven                    bool
}
type AccountCloneResult struct {
	Account              accountInfo
	ClonedFrom, Password string
}
type AccountShowResult struct {
	Login, Provider, ExitCountry, Exit string
	PreSOCKS5, PostSOCKS5              accountShowChainInfo
	Providers, Countries               []string
	Tiers                              []accountShowTier
	Live                               bool
	FallbackConnections                *int
}

func (r AccountShowResult) document() map[string]any {
	return map[string]any{"login": r.Login, "provider": r.Provider, "exit_country": r.ExitCountry, "exit": r.Exit, "pre_socks5": r.PreSOCKS5, "post_socks5": r.PostSOCKS5, "providers": r.Providers, "countries": r.Countries, "tiers": r.Tiers, "live": r.Live, "fallback_connections": r.FallbackConnections}
}
func AccountAdd(home string, o AccountAddOptions) (AccountAddResult, error) {
	c, e := loadConfig(home)
	if e != nil {
		return AccountAddResult{}, e
	}
	r := AccountAddResult{}
	e = accountAddWith(home, c, o, func() error { return saveAccountConfig(home, c) }, &r)
	return r, e
}
func accountAddWith(home string, c *config.Config, o AccountAddOptions, save func() error, r *AccountAddResult) error {
	if !config.ValidAccountLogin(o.Login) {
		return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
	}
	if err := validateCLIHopList(o.PreSOCKS5, "--pre-socks5"); err != nil {
		return err
	}
	if err := validateCLIHopList(o.PostSOCKS5, "--post-socks5"); err != nil {
		return err
	}
	cc := ""
	if o.Country != "" {
		v, e := parseCountries(o.Country)
		if e != nil {
			return e
		}
		cc = v
	}
	prov := ""
	if o.Provider != "" {
		v, e := parseProviders(c, o.Provider)
		if e != nil {
			return e
		}
		prov = v
	}
	exit := o.Exit
	if o.ExitGiven && (exit != "route" && exit != "direct") {
		return invalidAccountExitAt(len(c.Accounts))
	}
	if !o.ExitGiven && exit != "" && exit != "route" && exit != "direct" {
		return invalidAccountExitAt(len(c.Accounts))
	}
	clients := ""
	if o.Clients != "" {
		if _, e := config.ParseAllowedClients(o.Clients); e != nil {
			return &cli.Error{Code: "CONFIG_INVALID", Message: "invalid --clients: " + e.Error(), Exit: cli.ExitUsage}
		}
		clients = config.NormalizeAllowedClients(o.Clients)
	}
	if a, _ := c.Account(o.Login); a != nil {
		if a.ExitCountry == cc && a.Provider == prov && effectiveExit(a) == effectiveExit(&config.Account{Exit: exit}) && a.AllowedClients == clients && slices.Equal(a.PreSOCKS5, o.PreSOCKS5) && slices.Equal(a.PostSOCKS5, o.PostSOCKS5) {
			r.Account, r.Existing = infoForAccount(c, a), true
			if a.Exit == "direct" {
				r.Hint = directExitHint
			}
			return nil
		}
		return &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists with a different provider, country, exit, clients rule or proxy hops", Exit: cli.ExitConflict}
	}
	if c.Auth.Login == o.Login && c.Auth.Login != config.InitPlaceholder {
		return &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists", Exit: cli.ExitConflict}
	}
	n := config.Account{Login: o.Login, Password: config.InitPlaceholder, Provider: prov, ExitCountry: cc, Exit: exit, AllowedClients: clients, PreSOCKS5: cloneHopList(o.PreSOCKS5), PostSOCKS5: cloneHopList(o.PostSOCKS5)}
	if e := validateAccountCandidate(c, map[int]config.Account{len(c.Accounts): n}); e != nil {
		return e
	}
	originalAccounts := c.Accounts
	c.Accounts = append(c.Accounts, n)
	if e := save(); e != nil {
		c.Accounts = originalAccounts
		return e
	}
	r.Account, r.Existing = infoForAccount(c, &c.Accounts[len(c.Accounts)-1]), false
	if exit == "direct" {
		r.Hint = directExitHint
	}
	return nil
}
func AccountList(home string) ([]accountInfo, error) {
	c, e := loadConfig(home)
	if e != nil {
		return nil, e
	}
	return accountList(c), nil
}
func AccountShow(home, login string) (AccountShowResult, error) {
	c, e := loadConfig(home)
	if e != nil {
		return AccountShowResult{}, e
	}
	return accountShowResult(context.Background(), home, c, login)
}
func accountShowResult(ctx context.Context, home string, c *config.Config, login string) (AccountShowResult, error) {
	if !config.ValidAccountLogin(login) {
		return AccountShowResult{}, &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
	}
	a, _ := c.Account(login)
	if a == nil {
		return AccountShowResult{}, accountNotFound()
	}
	providers, countries := routeLists(*a)
	tiers, e := accountShowTiers(home, c, providers, countries)
	if e != nil {
		return AccountShowResult{}, e
	}
	if effectiveExit(a) == "direct" {
		tiers = []accountShowTier{}
	}
	info := infoForAccount(c, a)
	return AccountShowResult{Login: a.Login, Provider: a.Provider, ExitCountry: a.ExitCountry, Exit: effectiveExit(a), PreSOCKS5: showChainInfoForAccount(info.PreSOCKS5), PostSOCKS5: showChainInfoForAccount(info.PostSOCKS5), Providers: providers, Countries: countries, Tiers: tiers, Live: accountShowLive(ctx, home, login, tiers), FallbackConnections: fallbackConnections(tiers)}, nil
}
func AccountUpdate(home string, o AccountUpdateOptions) (accountInfo, error) {
	c, e := loadConfig(home)
	if e != nil {
		return accountInfo{}, e
	}
	return accountUpdateWith(home, c, o, func() error { return saveAccountConfig(home, c) })
}
func accountUpdateWith(home string, c *config.Config, o AccountUpdateOptions, save func() error) (accountInfo, error) {
	if !config.ValidAccountLogin(o.Login) {
		return accountInfo{}, &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
	}
	a, _ := c.Account(o.Login)
	if a == nil {
		return accountInfo{}, accountNotFound()
	}
	n := *a
	n.PreSOCKS5 = cloneHopList(a.PreSOCKS5)
	n.PostSOCKS5 = cloneHopList(a.PostSOCKS5)
	if o.Provider != nil {
		v, e := normalizeAccountField(c, "provider", *o.Provider)
		if e != nil {
			return accountInfo{}, e
		}
		n.Provider = v
	}
	if o.Country != nil {
		v, e := normalizeAccountField(c, "country", *o.Country)
		if e != nil {
			return accountInfo{}, e
		}
		n.ExitCountry = v
	}
	if o.Clients != nil {
		v, e := normalizeAccountField(c, "clients", *o.Clients)
		if e != nil {
			return accountInfo{}, e
		}
		n.AllowedClients = v
	}
	if o.Exit != nil {
		if err := validateAccountExitMode(*o.Exit); err != nil {
			return accountInfo{}, err
		}
		n.Exit = *o.Exit
	}
	if hops, e := hopUpdate(n.PreSOCKS5, o.Pre, o.PreEdit, "pre_socks5"); e != nil {
		return accountInfo{}, e
	} else {
		n.PreSOCKS5 = hops
	}
	if hops, e := hopUpdate(n.PostSOCKS5, o.Post, o.PostEdit, "post_socks5"); e != nil {
		return accountInfo{}, e
	} else {
		n.PostSOCKS5 = hops
	}
	_, i := c.Account(o.Login)
	if o.Exit != nil {
		if err := validateAccountExitChange(n, i); err != nil {
			return accountInfo{}, err
		}
	}
	if e := validateAccountCandidate(c, map[int]config.Account{i: n}); e != nil {
		return accountInfo{}, e
	}
	changed := n.Provider != a.Provider || n.ExitCountry != a.ExitCountry || effectiveExit(&n) != effectiveExit(a)
	if changed && effectiveExit(&n) == "route" {
		if e := checkRoute(home, c, n); e != nil {
			return accountInfo{}, e
		}
	}
	original := *a
	*a = n
	if e := save(); e != nil {
		*a = original
		return accountInfo{}, e
	}
	return infoForAccount(c, a), nil
}
func normalizeAccountField(c *config.Config, field, value string) (string, error) {
	switch field {
	case "provider":
		if value == "" {
			return "", nil
		}
		return parseProviders(c, value)
	case "country":
		if value == "" {
			return "", nil
		}
		return parseCountries(value)
	case "clients":
		if value == "" {
			return "", nil
		}
		if _, e := config.ParseAllowedClients(value); e != nil {
			return "", &cli.Error{Code: "CONFIG_INVALID", Message: e.Error(), Exit: cli.ExitUsage}
		}
		return config.NormalizeAllowedClients(value), nil
	}
	return value, nil
}
func AccountClone(home string, o AccountCloneOptions) (AccountCloneResult, error) {
	c, e := loadConfig(home)
	if e != nil {
		return AccountCloneResult{}, e
	}
	return accountCloneWith(home, c, o, func() error { return saveAccountConfig(home, c) })
}
func accountCloneWith(home string, c *config.Config, o AccountCloneOptions, save func() error) (AccountCloneResult, error) {
	for _, l := range []string{o.Source, o.Login} {
		if !config.ValidAccountLogin(l) || l == config.InitPlaceholder {
			return AccountCloneResult{}, &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
		}
	}
	src, _ := c.Account(o.Source)
	if src == nil {
		return AccountCloneResult{}, &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "account not found: " + o.Source, Exit: cli.ExitNotFound}
	}
	if a, _ := c.Account(o.Login); a != nil || (c.Auth.Login == o.Login && c.Auth.Login != config.InitPlaceholder) {
		return AccountCloneResult{}, &cli.Error{Code: "ACCOUNT_EXISTS", Message: "account already exists: " + o.Login, Exit: cli.ExitConflict}
	}
	n := config.Account{Login: o.Login, Password: src.Password, Provider: src.Provider, ExitCountry: src.ExitCountry, Exit: src.Exit, AllowedClients: src.AllowedClients, PreSOCKS5: cloneHopList(src.PreSOCKS5), PostSOCKS5: cloneHopList(src.PostSOCKS5)}
	mode := clonePasswordCopied
	if src.Password == config.InitPlaceholder {
		mode = clonePasswordInit
	}
	if o.PasswordGiven {
		switch {
		case o.Password == "":
			return AccountCloneResult{}, cli.Usage("account clone: --password must not be empty; use --password init to let the first client set it")
		case o.Password == config.InitPlaceholder:
			n.Password, mode = config.InitPlaceholder, clonePasswordInit
		case len(o.Password) > auth.MaxPasswordLen:
			return AccountCloneResult{}, cli.Usage("account clone: --password is longer than %d bytes", auth.MaxPasswordLen)
		default:
			h, e := auth.Hash(o.Password)
			if e != nil {
				return AccountCloneResult{}, &cli.Error{Code: "INTERNAL", Message: "hash the password", Exit: cli.ExitInternal}
			}
			n.Password, mode = h, clonePasswordNew
		}
	}
	if o.ClientsGiven {
		if _, e := config.ParseAllowedClients(o.Clients); e != nil {
			return AccountCloneResult{}, &cli.Error{Code: "CONFIG_INVALID", Message: "invalid --clients: " + e.Error(), Exit: cli.ExitUsage}
		}
		n.AllowedClients = config.NormalizeAllowedClients(o.Clients)
	}
	route := false
	if o.Provider != "" {
		v, e := normalizeAccountField(c, "provider", o.Provider)
		if e != nil {
			return AccountCloneResult{}, e
		}
		n.Provider = v
		route = true
	}
	if o.Country != "" {
		v, e := normalizeAccountField(c, "country", o.Country)
		if e != nil {
			return AccountCloneResult{}, e
		}
		n.ExitCountry = v
		route = true
	}
	if o.ExitGiven {
		if o.Exit != "route" && o.Exit != "direct" {
			return AccountCloneResult{}, invalidAccountExitAt(len(c.Accounts))
		}
		n.Exit = o.Exit
		route = true
	}
	if e := validateAccountCandidate(c, map[int]config.Account{len(c.Accounts): n}); e != nil {
		return AccountCloneResult{}, e
	}
	if route && effectiveExit(&n) == "route" {
		if e := checkRoute(home, c, n); e != nil {
			return AccountCloneResult{}, e
		}
	}
	originalAccounts := c.Accounts
	c.Accounts = append(c.Accounts, n)
	if e := save(); e != nil {
		c.Accounts = originalAccounts
		return AccountCloneResult{}, e
	}
	return AccountCloneResult{Account: infoForAccount(c, &c.Accounts[len(c.Accounts)-1]), ClonedFrom: o.Source, Password: mode}, nil
}
func AccountRemove(home, login string) error {
	c, e := loadConfig(home)
	if e != nil {
		return e
	}
	return accountRemoveWith(c, login, func() error { return saveAccountConfig(home, c) })
}
func accountRemoveWith(c *config.Config, login string, save func() error) error {
	if !config.ValidAccountLogin(login) {
		return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
	}
	_, i := c.Account(login)
	if i < 0 {
		return accountNotFound()
	}
	originalAccounts := c.Accounts
	candidateAccounts := append([]config.Account(nil), c.Accounts...)
	c.Accounts = append(candidateAccounts[:i], candidateAccounts[i+1:]...)
	if err := save(); err != nil {
		c.Accounts = originalAccounts
		return err
	}
	return nil
}
func updateAccountTargets(home string, c *config.Config, field string, targets []*config.Account, spec string, all bool, value string, save func() error) ([]accountInfo, error) {
	if len(targets) == 0 {
		return nil, accountNotFound()
	}
	v, e := normalizeAccountField(c, field, value)
	if e != nil {
		return nil, e
	}
	candidate := *c
	candidate.Accounts = append([]config.Account(nil), c.Accounts...)
	updates := make(map[int]config.Account, len(targets))
	for _, a := range targets {
		n := *a
		n.PreSOCKS5 = cloneHopList(a.PreSOCKS5)
		n.PostSOCKS5 = cloneHopList(a.PostSOCKS5)
		switch field {
		case "country":
			n.ExitCountry = v
		case "provider":
			n.Provider = v
		case "clients":
			n.AllowedClients = v
		}
		_, i := c.Account(a.Login)
		updates[i] = n
		candidate.Accounts[i] = n
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if field != "clients" {
		for _, a := range targets {
			_, i := c.Account(a.Login)
			n := updates[i]
			if n.Provider != a.Provider || n.ExitCountry != a.ExitCountry || n.AllowedClients != a.AllowedClients || n.Exit != a.Exit {
				if e := checkRoute(home, c, n); e != nil {
					return nil, e
				}
			}
		}
	}
	originals := make(map[int]config.Account, len(targets))
	for _, a := range targets {
		_, i := c.Account(a.Login)
		originals[i] = *a
		*a = updates[i]
	}
	if e := save(); e != nil {
		for _, a := range targets {
			_, i := c.Account(a.Login)
			*a = originals[i]
		}
		return nil, e
	}
	out := make([]accountInfo, 0, len(targets))
	for _, a := range targets {
		out = append(out, infoForAccount(c, a))
	}
	return out, nil
}
func mutateAccountExit(home string, c *config.Config, targets []*config.Account, value string, save func() error) ([]accountInfo, error) {
	if len(targets) == 0 {
		return nil, accountNotFound()
	}
	next := make([]config.Account, len(targets))
	for i, a := range targets {
		next[i] = *a
		next[i].Exit = value
		_, idx := c.Account(a.Login)
		if e := validateAccountExitChange(next[i], idx); e != nil {
			return nil, e
		}
		if value == "route" && effectiveExit(a) != "route" {
			if e := checkRoute(home, c, next[i]); e != nil {
				return nil, e
			}
		}
	}
	candidate := *c
	candidate.Accounts = append([]config.Account(nil), c.Accounts...)
	for i, a := range targets {
		_, idx := c.Account(a.Login)
		candidate.Accounts[idx] = next[i]
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	originals := make(map[int]config.Account, len(targets))
	for i, a := range targets {
		_, idx := c.Account(a.Login)
		originals[idx] = *a
		*a = next[i]
	}
	if e := save(); e != nil {
		for _, a := range targets {
			_, idx := c.Account(a.Login)
			*a = originals[idx]
		}
		return nil, e
	}
	out := make([]accountInfo, 0, len(targets))
	for _, a := range targets {
		out = append(out, infoForAccount(c, a))
	}
	return out, nil
}
func mutateAccountSOCKS5(c *config.Config, targets []*config.Account, preGiven, postGiven bool, pre, post string, save func() error) ([]accountInfo, error) {
	preEdit, postEdit := HopEdit{}, HopEdit{}
	if preGiven {
		edit, _, err := parseHopEditFlags([]string{pre}, nil, nil, true, false, false, "pre")
		if err != nil {
			return nil, err
		}
		preEdit = edit
	}
	if postGiven {
		edit, _, err := parseHopEditFlags([]string{post}, nil, nil, true, false, false, "post")
		if err != nil {
			return nil, err
		}
		postEdit = edit
	}
	return mutateAccountSOCKS5Edits(c, targets, preGiven, postGiven, preEdit, postEdit, save)
}

func mutateAccountSOCKS5Edits(c *config.Config, targets []*config.Account, preGiven, postGiven bool, preEdit, postEdit HopEdit, save func() error) ([]accountInfo, error) {
	if len(targets) == 0 {
		return nil, accountNotFound()
	}
	candidate := *c
	candidate.Accounts = append([]config.Account(nil), c.Accounts...)
	updates := make(map[int]config.Account, len(targets))
	for _, a := range targets {
		n := *a
		n.PreSOCKS5 = cloneHopList(a.PreSOCKS5)
		n.PostSOCKS5 = cloneHopList(a.PostSOCKS5)
		if preGiven {
			list, err := validateHopEdit(n.PreSOCKS5, preEdit, "pre_socks5")
			if err != nil {
				return nil, err
			}
			n.PreSOCKS5 = list
		}
		if postGiven {
			list, err := validateHopEdit(n.PostSOCKS5, postEdit, "post_socks5")
			if err != nil {
				return nil, err
			}
			n.PostSOCKS5 = list
		}
		_, i := c.Account(a.Login)
		updates[i] = n
		candidate.Accounts[i] = n
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	originals := make(map[int]config.Account, len(targets))
	for _, a := range targets {
		_, i := c.Account(a.Login)
		originals[i] = *a
		*a = updates[i]
	}
	if e := save(); e != nil {
		for _, a := range targets {
			_, i := c.Account(a.Login)
			*a = originals[i]
		}
		return nil, e
	}
	out := make([]accountInfo, 0, len(targets))
	for _, a := range targets {
		out = append(out, infoForAccount(c, a))
	}
	return out, nil
}
