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
	"time"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
)

type upOptions struct {
	Foreground bool
	Port       int
	Bind       string
	PortSet    bool // --port given explicitly: overrides listen.port
	BindSet    bool // --bind given explicitly: overrides listen.bind
	Home       string
	Handshake  string // internal: handshake file of the spawning parent (daemon child only)
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
	fs.BoolVar(&opts.Foreground, "foreground", false, "run in the foreground instead of as a background daemon")
	fs.IntVar(&opts.Port, "port", 7890, "mixed proxy port, 0 for a random free port")
	fs.StringVar(&opts.Bind, "bind", "127.0.0.1", "bind address")
	// Internal, not part of the CLI contract: set only by daemon.Spawn.
	fs.StringVar(&opts.Handshake, "handshake", "", "internal: startup handshake file (requires --foreground)")
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
	if opts.Handshake != "" && !opts.Foreground {
		return opts, cli.Usage("up: --handshake requires --foreground")
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
	status := authStatus(cfg.Auth.Login, cfg.Auth.Password, cfg.Accounts)
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
// output: none when neither `auth` nor an account is configured,
// init-pending while any placeholder is set, else enabled.
func authStatus(login, password string, accounts []config.Account) string {
	if login == config.InitPlaceholder || password == config.InitPlaceholder {
		return "init-pending"
	}
	for _, a := range accounts {
		if a.Password == config.InitPlaceholder {
			return "init-pending"
		}
	}
	if login == "" && password == "" && len(accounts) == 0 {
		return "none"
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

func newPersistAccount(path string, cfg *config.Config) func(login, passwordHash string) error {
	return func(login, passwordHash string) error {
		persistMu.Lock()
		defer persistMu.Unlock()
		a, _ := cfg.Account(login)
		if a == nil {
			return errors.New("account no longer exists")
		}
		a.Password = passwordHash
		return config.Save(path, cfg)
	}
}

// liveAuth is the current `auth` value. The persist callbacks rewrite the
// credentials from a connection goroutine, always under persistMu.
func liveAuth(cfg *config.Config) string {
	persistMu.Lock()
	defer persistMu.Unlock()
	return authStatus(cfg.Auth.Login, cfg.Auth.Password, cfg.Accounts)
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
	persistMu.Lock()
	defer persistMu.Unlock()
	legacy := auth.Options{
		Login:    cfg.Auth.Login,
		Password: cfg.Auth.Password,
		Persist:  newPersist(config.ConfigPath(home), cfg),
	}
	accounts := make([]auth.AccountSpec, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		accounts = append(accounts, auth.AccountSpec{
			Login:    a.Login,
			Password: a.Password,
		})
	}
	a, err := auth.NewMulti(auth.MultiOptions{
		Legacy:         legacy,
		Accounts:       accounts,
		PersistAccount: newPersistAccount(config.ConfigPath(home), cfg),
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
		return failStartup(opts.Handshake, err)
	}
	bind, port, err := mergeUp(opts, cfg)
	if err != nil {
		return failStartup(opts.Handshake, err)
	}
	if _, err := accountRoutePool(opts.Home, cfg); err != nil {
		return failStartup(opts.Handshake, err)
	}
	if !opts.Foreground {
		return runUpDaemon(opts, args, stdout)
	}
	return runUpForeground(opts, stdout, bind, port, cfg)
}

// runUpDaemon is plain `up`: a short-lived parent that answers idempotently
// when a daemon already runs in this home and otherwise spawns the detached
// child that does the real work. Config problems were already reported by
// runUp, before anything is spawned.
func runUpDaemon(opts upOptions, args []string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemon.DefaultSpawnTimeout+daemon.ProbeTimeout)
	defer cancel()
	if _, live, err := daemonProbeLive(ctx, opts.Home); err == nil {
		if conflictsWithRunning(opts, live.Listen) {
			return &cli.Error{
				Code:    "ALREADY_RUNNING",
				Message: "a daemon is already running with different settings",
				Hint:    "run netivri down first",
				Exit:    cli.ExitConflict,
			}
		}
		cli.Success(stdout, alreadyRunningData(live, true))
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "resolve executable: " + err.Error(), Exit: cli.ExitInternal}
	}
	st, err := daemonSpawn(ctx, daemon.SpawnOptions{Exe: exe, Args: filterUpArgs(args, opts.Home), Home: opts.Home})
	if err != nil {
		return err
	}
	cli.Success(stdout, alreadyRunningData(statusOf(st), false))
	return nil
}

// runUpForeground runs the proxy in this process: the user's `--foreground`
// run or the daemon child (opts.Handshake set). A failure of the daemon child
// reaches the spawning parent through the handshake file.
func runUpForeground(opts upOptions, stdout io.Writer, bind string, port int, cfg *config.Config) error {
	if err := runForegroundInner(opts, stdout, bind, port, cfg); err != nil {
		return failStartup(opts.Handshake, err)
	}
	return nil
}

// runForegroundInner remains replaceable by tests so runUpForeground can be
// exercised without starting Mihomo.
var runForegroundInner = serve

func serve(opts upOptions, stdout io.Writer, bind string, port int, cfg *config.Config) error {
	pool, err := accountRoutePool(opts.Home, cfg)
	if err != nil {
		return err
	}
	// Foreground cannot be idempotent: refuse a second instance in one home.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), daemon.ProbeTimeout)
	_, err = daemonProbe(probeCtx, opts.Home)
	cancelProbe()
	if err == nil {
		return &cli.Error{
			Code:    "ALREADY_RUNNING",
			Message: "a daemon is already running in this state directory",
			Hint:    "run netivri down first",
			Exit:    cli.ExitConflict,
		}
	}
	authenticator, err := newAuthenticator(cfg, opts.Home)
	if err != nil {
		return err
	}

	// Installed before Start so a signal during startup is not lost.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Only the daemon child has a log; a foreground run stays silent on stderr.
	logf := func(format string, args ...any) {
		if opts.Handshake != "" {
			daemonLog.Printf(format, args...)
		}
	}
	store := openStats(opts.Home, pool.infos, logf)
	defer closeStats(store)
	var observer core.Observer
	if store != nil {
		observer = statsObserver{store: store}
	}
	inst, err := core.Start(ctx, core.Options{
		HomeDir:       opts.Home,
		Bind:          bind,
		Port:          port,
		Authenticator: authenticator,
		RouteNodes:    pool.nodes,
		AccountRoute:  pool.accountRoutes,
		NodeLabels:    pool.labels,
		Observer:      observer,
		Warm:          warmOptions(pool.nodes),
	})
	if err != nil {
		return err
	}

	token, err := daemon.NewToken()
	if err != nil {
		_ = inst.Close()
		return &cli.Error{Code: "INTERNAL", Message: "generate control token: " + err.Error(), Exit: cli.ExitInternal}
	}
	stopRequested := make(chan struct{})
	startedAt := time.Now().UTC().Format(time.RFC3339)
	status := func() daemon.Status {
		return daemon.Status{
			Daemon:     "running",
			Foreground: opts.Handshake == "",
			PID:        os.Getpid(),
			Listen:     inst.Addr(),
			Protocols:  daemonProtocols,
			Auth:       liveAuth(cfg),
			Route:      inst.Route(),
			StartedAt:  startedAt,
			Version:    version,
			Accounts:   accountStatus(cfg, inst.AccountRoutes()),
			Warm:       warmInfo(inst),
			Stats:      statsInfo(store, opts.Home),
			Affinity:   affinityInfo(inst, len(pool.accountRoutes) > 0),
		}
	}
	ctrl, err := daemon.StartServer(token, status, func() { close(stopRequested) })
	if err != nil {
		_ = inst.Close()
		return err
	}

	st := stateOf(status(), ctrl.Addr(), token)
	// Closing order matters for `down`: the core goes down first, the
	// statistics are flushed next (a stats query after `down` sees them), the
	// control server after that, and the state file disappears last.
	shutdown := func() {
		_ = inst.Close()
		closeStats(store)
		_ = ctrl.Close()
		daemon.RemoveStateIf(opts.Home, st.PID, st.Token)
	}
	if err := daemon.WriteState(opts.Home, st); err != nil {
		shutdown()
		return err
	}
	if opts.Handshake != "" {
		if err := daemon.WriteHandshake(opts.Handshake, st); err != nil {
			shutdown()
			return err
		}
		daemonLog.Printf("start pid=%d listen=%s route=%s", st.PID, st.Listen, st.Route)
	} else {
		cli.Success(stdout, map[string]any{
			"daemon":     "running",
			"pid":        st.PID,
			"listen":     st.Listen,
			"protocols":  st.Protocols,
			"auth":       st.Auth,
			"foreground": true,
		})
	}

	reason := "signal"
	select {
	case <-sig:
	case <-stopRequested:
		reason = "control"
	}
	shutdown()
	if opts.Handshake != "" {
		daemonLog.Printf("stop pid=%d reason=%s", st.PID, reason)
	}
	return nil
}
