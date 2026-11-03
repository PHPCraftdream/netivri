package main

import (
	"flag"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"io"
)

func (a *subApp) rm(args []string) error {
	fs := flag.NewFlagSet("sub rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	pos, e := parseFlags(fs, args)
	if e != nil {
		return cli.Usage("sub rm: %v", e)
	}
	if len(pos) != 1 {
		return cli.Usage("sub rm: exactly one subscription name is required")
	}
	if _, e = ProviderRemove(a.home, pos[0]); e != nil {
		return e
	}
	cli.Success(a.stdout, map[string]any{"removed": pos[0]})
	return nil
}
