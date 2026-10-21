package main

import (
	"io"
	"os"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

var version = "0.0.0-dev"

// commands maps every command to its runner. `help` is not here: it is
// answered by parseHelpRequest before a command is looked up, and
// help_test.go checks that this table and the help topics agree.
var commands = map[string]func(args []string, stdout io.Writer) error{
	"up":      runUp,
	"down":    runDown,
	"status":  runStatus,
	"sub":     runSub,
	"node":    runNode,
	"account": runAccount,
	"stats":   runStats,
	"diag":    runDiag,
	"version": runVersion,
}

func runVersion(_ []string, stdout io.Writer) error {
	cli.Success(stdout, map[string]string{"version": version})
	return nil
}

// dispatch routes the arguments and returns the failure to report, if any.
// Asking for help (no arguments, `help`, `--help`, `<command> --help`) is
// answered before any command runs.
func dispatch(args []string, stdout io.Writer) error {
	if req, ok := parseHelpRequest(args); ok {
		return runHelp(req, stdout)
	}
	run, ok := commands[args[0]]
	if !ok {
		e := cli.Usage("unknown command %q", args[0])
		e.Hint = "run: netivri help"
		return e
	}
	return run(args[1:], stdout)
}

func main() {
	if err := dispatch(os.Args[1:], os.Stdout); err != nil {
		cli.Fail(err)
	}
}
