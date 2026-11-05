package main

import (
	"io"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// runConfig implements `netivri config get|set`, the CLI front end over the
// ConfigGet/ConfigSet ops API. Home resolution matches every other command
// (parseHomeArgs), so --home, --local and the compatibility flags are stripped
// before the positional arguments are validated.
func runConfig(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		e := cli.Usage("config: missing subcommand (get, set)")
		e.Hint = "run: netivri help config"
		return e
	}
	home, rest, err := parseHomeArgs("config "+args[0], args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "get":
		if len(rest) > 1 {
			return cli.Usage("config get: at most one KEY")
		}
		key := ""
		if len(rest) == 1 {
			key = rest[0]
		}
		out, err := ConfigGet(home, key)
		if err != nil {
			return err
		}
		cli.Success(stdout, out)
		return nil
	case "set":
		if len(rest) != 2 {
			return cli.Usage("config set: requires KEY and VALUE")
		}
		res, err := ConfigSet(home, rest[0], rest[1])
		if err != nil {
			return err
		}
		cli.Success(stdout, res)
		return nil
	default:
		e := cli.Usage("config: unknown subcommand %q", args[0])
		e.Hint = "run: netivri help config"
		return e
	}
}
