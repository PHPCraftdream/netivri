package main

import (
	"context"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
)

type upOptions struct {
	Foreground bool
	Port       int
	Bind       string
	Home       string
}

// defaultHome resolves the state directory: NETIVRI_HOME, then
// userConfigDir()/netivri (docs/cli.md --home default).
func defaultHome(env func(string) string, userConfigDir func() (string, error)) (string, error) {
	if v := env("NETIVRI_HOME"); v != "" {
		return v, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "netivri"), nil
}

// isLoopbackHost accepts 127.0.0.0/8, ::1 and "localhost".
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseUp parses `up` flags and validates the result.
func parseUp(args []string, env func(string) string, userConfigDir func() (string, error)) (upOptions, error) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := upOptions{Port: 7890, Bind: "127.0.0.1"}
	fs.BoolVar(&opts.Foreground, "foreground", false, "run in the foreground (required in v1)")
	fs.IntVar(&opts.Port, "port", 7890, "mixed proxy port, 0 for a random free port")
	fs.StringVar(&opts.Bind, "bind", "127.0.0.1", "bind address")
	homeFlag := fs.String("home", "", "state directory (default NETIVRI_HOME or user config dir)")
	// Global flags from docs/cli.md, accepted for compatibility: --json and
	// --no-color are no-ops (output is always JSON without ANSI); --quiet
	// suppresses nothing today.
	_ = fs.Bool("json", false, "machine output (always on)")
	_ = fs.Bool("quiet", false, "suppress human-oriented stderr output")
	_ = fs.Bool("no-color", false, "disable ANSI colors (never used)")

	if err := fs.Parse(args); err != nil {
		return opts, cli.Usage("up: %v", err)
	}
	if fs.NArg() > 0 {
		return opts, cli.Usage("up: unexpected argument %q", fs.Arg(0))
	}
	if !opts.Foreground {
		return opts, cli.Usage("daemon mode is not implemented yet; use --foreground")
	}
	if !isLoopbackHost(opts.Bind) {
		return opts, &cli.Error{
			Code:    "AUTH_REQUIRED",
			Message: "non-loopback bind requires authentication",
			Hint:    "keep --bind on 127.0.0.1 or ::1 until proxy authentication exists",
			Exit:    cli.ExitUsage,
		}
	}
	opts.Home = *homeFlag
	if opts.Home == "" {
		h, err := defaultHome(env, userConfigDir)
		if err != nil {
			return opts, &cli.Error{Code: "INTERNAL", Message: "resolve home directory: " + err.Error(), Exit: cli.ExitInternal}
		}
		opts.Home = h
	}
	return opts, nil
}

func runUp(args []string, stdout io.Writer) error {
	opts, err := parseUp(args, os.Getenv, os.UserConfigDir)
	if err != nil {
		return err
	}

	// Installed before Start so a signal during startup is not lost.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst, err := core.Start(ctx, core.Options{HomeDir: opts.Home, Bind: opts.Bind, Port: opts.Port})
	if err != nil {
		return err
	}

	cli.Success(stdout, map[string]any{
		"daemon":    "running",
		"pid":       os.Getpid(),
		"listen":    inst.Addr(),
		"protocols": []string{"socks5", "socks4", "http"},
	})

	<-sig
	_ = inst.Close()
	return nil
}
