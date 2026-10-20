package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// runNode dispatches the `node` subcommands.
func runNode(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("node: missing subcommand (list)")
	}
	home, rest, err := parseHomeArgs("node "+args[0], args[1:])
	if err != nil {
		return err
	}
	app := newSubApp(home, stdout)
	switch args[0] {
	case "list":
		return app.nodeList(rest)
	default:
		return cli.Usage("unknown node command %q", args[0])
	}
}

// nodeList implements `node list [--sub N] [--country CC] [--alive]`.
// Order is stable: subscription order, then provider order. Node.Raw is
// never serialised. --alive is reserved: health checks do not exist yet.
func (a *subApp) nodeList(args []string) error {
	fs := flag.NewFlagSet("node list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	subFlag := fs.String("sub", "", "only nodes of this subscription")
	countryFlag := fs.String("country", "", "filter by ISO 3166-1 alpha-2 country code")
	var alive bool
	fs.BoolVar(&alive, "alive", false, "reserved (health checks are not available yet)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("node list: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("node list: unexpected argument %q", pos[0])
	}
	if alive {
		return cli.Usage("node list: --alive is not available yet")
	}
	country := strings.ToUpper(*countryFlag)
	if country != "" && !subscription.IsCountryCode(country) {
		return cli.Usage("node list: --country must be an ISO 3166-1 alpha-2 code")
	}

	cfg, err := a.load()
	if err != nil {
		return err
	}
	var subs []config.Subscription
	if *subFlag != "" {
		for _, s := range cfg.Subscriptions {
			if s.Name == *subFlag {
				subs = append(subs, s)
				break
			}
		}
		if subs == nil {
			return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
		}
	} else {
		subs = cfg.Subscriptions
	}

	store := a.store()
	nodes := make([]map[string]any, 0)
	for _, s := range subs {
		snap, err := store.Load(s.Name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // never fetched yet
			}
			return snapshotError(s.Name)
		}
		for _, n := range snap.Nodes {
			if country != "" && n.Country != country {
				continue
			}
			nodes = append(nodes, map[string]any{
				"id":      n.ID,
				"name":    n.Name,
				"type":    n.Type,
				"server":  n.Server,
				"port":    n.Port,
				"country": n.Country,
				"sub":     s.Name,
			})
		}
	}
	cli.Success(a.stdout, map[string]any{"nodes": nodes, "count": len(nodes)})
	return nil
}
