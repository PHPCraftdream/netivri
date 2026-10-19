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

// defaultHome resolves the state directory: NETIVRI_HOME, then
// os.UserConfigDir()/netivri (docs/cli.md --home default).
func defaultHome() (string, error) {
	if v := os.Getenv("NETIVRI_HOME"); v != "" {
		return v, nil
	}
	dir, err := os.UserConfigDir()
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

func runUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	foreground := fs.Bool("foreground", false, "run in the foreground (required in v1)")
	port := fs.Int("port", 7890, "mixed proxy port, 0 for a random free port")
	bind := fs.String("bind", "127.0.0.1", "bind address")
	homeFlag := fs.String("home", "", "state directory (default NETIVRI_HOME or user config dir)")
	// Global flags from docs/cli.md, accepted for compatibility: --json and
	// --no-color are no-ops (output is always JSON without ANSI); --quiet
	// suppresses nothing today.
	_ = fs.Bool("json", false, "machine output (always on)")
	_ = fs.Bool("quiet", false, "suppress human-oriented stderr output")
	_ = fs.Bool("no-color", false, "disable ANSI colors (never used)")

	if err := fs.Parse(args); err != nil {
		cli.Fail(cli.Usage("up: %v", err))
	}
	if fs.NArg() > 0 {
		cli.Fail(cli.Usage("up: unexpected argument %q", fs.Arg(0)))
	}
	if !*foreground {
		cli.Fail(cli.Usage("daemon mode is not implemented yet; use --foreground"))
	}
	if !isLoopbackHost(*bind) {
		cli.Fail(&cli.Error{
			Code:    "AUTH_REQUIRED",
			Message: "non-loopback bind requires authentication",
			Hint:    "keep --bind on 127.0.0.1 or ::1 until proxy authentication exists",
			Exit:    cli.ExitUsage,
		})
	}
	home := *homeFlag
	if home == "" {
		h, err := defaultHome()
		if err != nil {
			cli.Fail(&cli.Error{Code: "INTERNAL", Message: "resolve home directory: " + err.Error(), Exit: cli.ExitInternal})
		}
		home = h
	}

	// Installed before Start so a signal during startup is not lost.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst, err := core.Start(ctx, core.Options{HomeDir: home, Bind: *bind, Port: *port})
	if err != nil {
		cli.Fail(err)
	}

	cli.Success(os.Stdout, map[string]any{
		"daemon":    "running",
		"pid":       os.Getpid(),
		"listen":    inst.Addr(),
		"protocols": []string{"socks5", "socks4", "http"},
	})

	<-sig
	_ = inst.Close()
}
