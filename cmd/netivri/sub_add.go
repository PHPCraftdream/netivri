package main

import (
	"context"
	"flag"
	"io"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// add implements `sub add <url> [--name N] [--via PROXY]`.
//
// Idempotent by URL: an already configured URL behaves like an update
// (re-fetch and re-store) and reports "existing": true; no duplicate is
// created. The config file is only touched after fetch, parse and store
// all succeeded, so a failure leaves it untouched (and uncreated).
func (a *subApp) add(args []string) error {
	fs := flag.NewFlagSet("sub add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	nameFlag := fs.String("name", "", "subscription name (default: derived from the URL host)")
	viaFlag := fs.String("via", "", "fetch through this proxy URL")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub add: %v", err)
	}
	if len(pos) != 1 {
		return cli.Usage("sub add: exactly one <url> argument is required")
	}
	rawURL := pos[0]
	if *nameFlag != "" && !validSubName(*nameFlag) {
		return cli.Usage("sub add: --name must match [A-Za-z0-9._-]{1,64}")
	}

	cfg, err := a.load()
	if err != nil {
		return err
	}

	existing := false
	name := *nameFlag
	if name == "" {
		name = deriveName(rawURL, cfg.Subscriptions)
	}
	for _, s := range cfg.Subscriptions {
		if s.URL == rawURL {
			// Same URL: reuse the configured name, ignore --name.
			existing = true
			name = s.Name
			break
		}
	}
	// An explicit --name owned by a different URL is a conflict; a
	// derived name is already unique (deriveName adds a suffix).
	if !existing && *nameFlag != "" {
		for _, s := range cfg.Subscriptions {
			if s.Name == name {
				return &cli.Error{
					Code:    codeSubExistsName,
					Message: "subscription name is already used by a different URL",
					Hint:    "pick another --name or remove the existing subscription with `netivri sub rm`",
					Exit:    cli.ExitConflict,
				}
			}
		}
	}

	// Quota follows the latest fetch (consistent with `sub update`):
	// a re-add of an existing URL refreshes the stored quota instead of
	// keeping the stale one or erasing it.
	snap, err := fetchSnapshot(context.Background(), a.fetch, a.now, cfg, config.Subscription{Name: name, URL: rawURL, Via: *viaFlag})
	if err != nil {
		return err
	}
	if err := a.store().Save(name, snap); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "store subscription: " + err.Error(), Exit: cli.ExitInternal}
	}

	if !existing {
		cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: name, URL: rawURL, Via: *viaFlag})
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := config.Save(a.configPath(), cfg); err != nil {
			return &cli.Error{Code: "INTERNAL", Message: "save config: " + err.Error(), Exit: cli.ExitInternal}
		}
	}

	countries := map[string]int{}
	for _, n := range snap.Nodes {
		cc := n.Country
		if cc == "" {
			cc = "unknown"
		}
		countries[cc]++
	}
	cli.Success(a.stdout, map[string]any{
		"name":      name,
		"added":     !existing,
		"existing":  existing,
		"nodes":     len(snap.Nodes),
		"skipped":   snap.Skipped,
		"countries": countries,
	})
	return nil
}
