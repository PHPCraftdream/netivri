package main

import (
	"os"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

var version = "0.0.0-dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		cli.Fail(cli.Usage("no command; see docs/cli.md"))
	}

	switch args[0] {
	case "up":
		runUp(args[1:])
	case "version":
		cli.Success(os.Stdout, map[string]string{"version": version})
	default:
		cli.Fail(cli.Usage("unknown or not yet implemented command %q", args[0]))
	}
}
