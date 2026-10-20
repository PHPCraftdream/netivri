package main

import (
	"io"
	"os"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

var version = "0.0.0-dev"

// dispatch routes the arguments and returns the failure to report, if any.
func dispatch(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("no command; see docs/cli.md")
	}

	switch args[0] {
	case "up":
		return runUp(args[1:], stdout)
	case "down":
		return runDown(args[1:], stdout)
	case "status":
		return runStatus(args[1:], stdout)
	case "sub":
		return runSub(args[1:], stdout)
	case "node":
		return runNode(args[1:], stdout)
	case "version":
		cli.Success(stdout, map[string]string{"version": version})
		return nil
	default:
		return cli.Usage("unknown command %q", args[0])
	}
}

func main() {
	if err := dispatch(os.Args[1:], os.Stdout); err != nil {
		cli.Fail(err)
	}
}
