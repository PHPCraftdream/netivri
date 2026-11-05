package main

import (
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
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

// nodeList implements `node list [--sub N] [--country CC] [--alive]`
// through the shared NodeList operation.
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

	entries, err := NodeList(a.home, *subFlag, country)
	if err != nil {
		return err
	}
	nodes := make([]map[string]any, 0, len(entries))
	for _, n := range entries {
		nodes = append(nodes, map[string]any{
			"id":      n.ID,
			"name":    n.Name,
			"type":    n.Type,
			"server":  n.Server,
			"port":    n.Port,
			"country": n.Country,
			"sub":     n.Sub,
		})
	}
	cli.Success(a.stdout, map[string]any{"nodes": nodes, "count": len(nodes)})
	return nil
}
