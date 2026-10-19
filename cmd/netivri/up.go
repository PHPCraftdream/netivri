package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
)

type upOptions struct {
	Foreground bool
	Port       int
	Bind       string
	PortSet    bool // --port given explicitly: overrides listen.port
	BindSet    bool // --bind given explicitly: overrides listen.bind
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

// parseUp parses `up` flags. It performs no file or bind validation:
// merging with the config file happens in mergeUp.
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
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			opts.PortSet = true
		case "bind":
			opts.BindSet = true
		}
	})
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

// mergeUp combines the parsed flags with the loaded configuration into
// the final bind address and port. Explicit flags win over the config;
// with no config (defaults) the result is 127.0.0.1:7890. A bind that
// is neither an IP nor "localhost" is a usage error; a non-loopback
// bind is AUTH_REQUIRED unless proxy authentication is enabled.
func mergeUp(opts upOptions, cfg *config.Config) (string, int, error) {
	var bind string
	var port int
	if opts.BindSet {
		bind = opts.Bind
	} else {
		bind = cfg.Listen.Bind
	}
	if opts.PortSet {
		port = opts.Port
	} else {
		port = cfg.Listen.Port
	}
	if bind != "localhost" && net.ParseIP(bind) == nil {
		return "", 0, cli.Usage("up: --bind must be an IP address or \"localhost\", got an invalid value")
	}
	if port != 0 && (port < 1 || port > 65535) {
		return "", 0, cli.Usage("up: --port must be 0 (random) or between 1 and 65535")
	}
	status := authStatus(cfg.Auth.Login, cfg.Auth.Password)
	authEnabled := status == "enabled" ||
		(status == "init-pending" && cfg.Auth.AllowInitRemote)
	if !isLoopbackHost(bind) && !authEnabled {
		return "", 0, &cli.Error{
			Code:    "AUTH_REQUIRED",
			Message: "non-loopback bind requires proxy authentication",
			Hint:    "set auth.login and auth.password in config.ktav (password may be \"init\"); see docs/config.md",
			Exit:    cli.ExitUsage,
		}
	}
	return bind, port, nil
}

// authStatus derives the value of the `auth` field of the `up` success
// output: none, enabled, or init-pending while a placeholder is set.
func authStatus(login, password string) string {
	if login == "" && password == "" {
		return "none"
	}
	if login == config.InitPlaceholder || password == config.InitPlaceholder {
		return "init-pending"
	}
	return "enabled"
}

// persistMu serialises the persist callback: the authenticator calls
// it at most once under its own lock, but the file write must not
// race with any other config writer in this process.
var persistMu sync.Mutex

// newPersist returns the callback that stores the claimed credential
// in the in-memory config and rewrites the config file. It never
// returns or logs the credential.
func newPersist(path string, cfg *config.Config) func(login, passwordHash string) error {
	return func(login, passwordHash string) error {
		persistMu.Lock()
		defer persistMu.Unlock()
		cfg.Auth.Login = login
		cfg.Auth.Password = passwordHash
		return config.Save(path, cfg)
	}
}

// loadConfig loads config.ktav under home; a missing file means the
// default configuration and no file is created. *cli.Error values are
// returned unchanged; anything else becomes INTERNAL.
func loadConfig(home string) (*config.Config, error) {
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, &cli.Error{Code: "INTERNAL", Message: "load config: " + err.Error(), Exit: cli.ExitInternal}
	}
	return cfg, nil
}

// newAuthenticator builds the Mihomo authenticator from the config.
// A nil interface (auth disabled) is returned exactly as auth.New
// produced it. auth.New errors that are not *cli.Error become
// CONFIG_INVALID.
func newAuthenticator(cfg *config.Config, home string) (mihauth.Authenticator, error) {
	a, err := auth.New(auth.Options{
		Login:    cfg.Auth.Login,
		Password: cfg.Auth.Password,
		Persist:  newPersist(config.ConfigPath(home), cfg),
	})
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, &cli.Error{
			Code:    config.CodeConfigInvalid,
			Exit:    cli.ExitUsage,
			Message: err.Error(),
			Hint:    "fix auth.login / auth.password in config.ktav; see docs/config.md",
		}
	}
	return a, nil
}

func runUp(args []string, stdout io.Writer) error {
	opts, err := parseUp(args, os.Getenv, os.UserConfigDir)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(opts.Home)
	if err != nil {
		return err
	}
	bind, port, err := mergeUp(opts, cfg)
	if err != nil {
		return err
	}
	authenticator, err := newAuthenticator(cfg, opts.Home)
	if err != nil {
		return err
	}
	// Taken before Start: once the listener is up, the persist callback may
	// rewrite cfg.Auth from a connection goroutine.
	status := authStatus(cfg.Auth.Login, cfg.Auth.Password)

	// Installed before Start so a signal during startup is not lost.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst, err := core.Start(ctx, core.Options{HomeDir: opts.Home, Bind: bind, Port: port, Authenticator: authenticator})
	if err != nil {
		return err
	}

	cli.Success(stdout, map[string]any{
		"daemon":    "running",
		"pid":       os.Getpid(),
		"listen":    inst.Addr(),
		"protocols": []string{"socks5", "socks4", "http"},
		"auth":      status,
	})

	<-sig
	_ = inst.Close()
	return nil
}
